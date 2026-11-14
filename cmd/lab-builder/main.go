// Command lab-builder is the lab's Builder: it builds kernels and packages on
// the mini, one build at a time. Each build runs `kbuild` or `pkgbuild` in an
// Apple `container` micro-VM, or with --runtime docker in a Docker container
// (Colima, Docker Desktop); the only state it keeps is its cache volumes.
//
// It never starts `container`'s server: a LaunchDaemon of the mini does, at
// boot and again whenever it finds it stopped. A build's source is public,
// so the Builder holds no credential but its own token, in a file only the
// account it runs as (root) can read.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/labhttp"
	"github.com/omacom/omarchy-maclab/internal/oob"
	"github.com/omacom/omarchy-maclab/internal/upload"
)

// The limits of a build: 10 of the mini's 18 cores and 16 of its 64 GB. The
// cores are a setting only so far as the Builder's gate test steps them
// down, 10, then 8, then 6; the memory is not one.
const buildMemory = "16G"

var buildCores = []string{"10", "8", "6"}

// The sizes of what the Builder keeps: the kernel cache volume and the
// package volume. The mini's set-up makes them at these sizes.
const (
	kernelVolumeSize  = "40G"
	packageVolumeSize = "120G"
)

type builder struct {
	server, token, host string
	prefix              string // build VMs are named <prefix>-<build id>
	image, volume       string
	pkgImage, pkgVolume string // package builds (makepkg on Arch Linux ARM); empty image: kernel builds only
	cpus, mem           string
	pkgJobs             string // OMARCHY_BUILD_JOBS for package builds
	io                  string
	runtime             string // "container" (Apple container) or "docker"
	docker              string // the docker CLI
	dockerContext       string // its --context, if any
	asUser              *user.User
	http                *http.Client
	rt                  http.RoundTripper // every request to labd, with labhttp's policy
	// What runs `container`, and what puts it in the user's session.
	containerBin, launchctl string
}

// configure reads the flags. The token comes from --token-file and from
// nowhere else: not from an argument, which ps shows, and not from the
// environment.
func configure(args []string, out io.Writer) (*builder, error) {
	home, _ := os.UserHomeDir()
	b := &builder{mem: buildMemory, containerBin: "/usr/local/bin/container", launchctl: "/bin/launchctl"}
	fl := flag.NewFlagSet("lab-builder", flag.ContinueOnError)
	fl.SetOutput(out)
	fl.StringVar(&b.server, "server", os.Getenv("MACLAB_URL"), "labd URL: on the mini http://127.0.0.1:7770")
	caFile := fl.String("ca-file", os.Getenv("MACLAB_CA_FILE"), "PEM file of CA certificates for labd's https certificate, when the system doesn't trust it")
	plain := fl.Bool("allow-plain-http", os.Getenv("MACLAB_ALLOW_PLAIN_HTTP") == "1", "allow an http:// labd on another machine: the builder token then crosses the network unencrypted")
	tokenFile := fl.String("token-file", "", "file holding this builder's token (from lab builder add -o); it belongs to the account lab-builder runs as and nobody else reads it")
	fl.StringVar(&b.prefix, "container-prefix", "maclab", "build VMs are named <prefix>-<build id>")
	fl.StringVar(&b.image, "image", "maclab-kbuild:1", "builder image")
	fl.StringVar(&b.volume, "volume", "maclab-cache", "kernel cache volume, "+kernelVolumeSize+" (the set-up makes it: container volume create -s "+kernelVolumeSize+" maclab-cache)")
	fl.StringVar(&b.pkgImage, "pkg-image", "maclab-pkgbuild:1", "package builder image (builder/pkg); empty to take kernel builds only")
	fl.StringVar(&b.pkgVolume, "pkg-volume", "maclab-pkgcache", "package build volume, "+packageVolumeSize+": room for a whole kernel tree (container volume create -s "+packageVolumeSize+" maclab-pkgcache)")
	fl.StringVar(&b.cpus, "cpus", buildCores[0], "cores a build gets: "+strings.Join(buildCores, ", then ")+" if the Builder's gate test fails; every build gets "+buildMemory)
	pkgJobs := fl.String("pkg-jobs", "", "parallel jobs for package builds, given to the recipe as OMARCHY_BUILD_JOBS (default: --cpus)")
	fl.StringVar(&b.io, "io", "", "scratch dir for inputs and the artifact; emptied after each build (default ~/maclab/io of the container user)")
	as := fl.String("as-user", "", "run container as this user, in their session. Lets lab-builder run as a root daemon, which macOS Local Network privacy does not block")
	fl.StringVar(&b.runtime, "runtime", "container", "what runs the builds: container (Apple container) or docker (Colima, Docker Desktop)")
	fl.StringVar(&b.docker, "docker", "docker", "the docker CLI, with --runtime docker")
	fl.StringVar(&b.dockerContext, "context", "", "docker context, with --runtime docker (e.g. colima)")
	if err := fl.Parse(args); err != nil {
		return nil, err
	}
	if fl.NArg() > 0 {
		return nil, fmt.Errorf("lab-builder takes flags only, not %q", fl.Arg(0))
	}
	if !slices.Contains(buildCores, b.cpus) {
		return nil, fmt.Errorf("--cpus %s: a build gets %s cores", b.cpus, strings.Join(buildCores, ", 8 or "))
	}
	var err error
	if b.pkgJobs, err = packageJobs(*pkgJobs, b.cpus); err != nil {
		return nil, err
	}
	if !rePrefix.MatchString(b.prefix) {
		return nil, fmt.Errorf("--container-prefix %q: letters, digits, dots, dashes and underscores, up to 32", b.prefix)
	}
	b.server = strings.TrimRight(b.server, "/")
	if b.server == "" || *tokenFile == "" {
		return nil, errors.New("need --server and --token-file: the token is read from a file and from nowhere else")
	}
	if err := labhttp.CheckServer(b.server, *plain); err != nil {
		return nil, err
	}
	if b.token, err = builderToken(*tokenFile); err != nil {
		return nil, err
	}
	pol := labhttp.Policy{AllowPlainHTTP: *plain}
	if *caFile != "" {
		ca, err := labhttp.ReadCAFile(*caFile)
		if err != nil {
			return nil, err
		}
		pol.CAPEM = ca
	}
	rt, err := pol.Transport()
	if err != nil {
		return nil, err
	}
	b.rt, b.http = rt, labhttp.Client(rt, 60*time.Second)
	switch b.runtime {
	case "container":
		if b.dockerContext != "" {
			return nil, errors.New("--context is for --runtime docker")
		}
	case "docker":
		p, err := exec.LookPath(b.docker)
		if err != nil {
			return nil, fmt.Errorf("--runtime docker: %v", err)
		}
		b.docker = p
	default:
		return nil, fmt.Errorf("--runtime %q: want container or docker", b.runtime)
	}
	if *as != "" {
		u, err := user.Lookup(*as)
		if err != nil {
			return nil, err
		}
		b.asUser, home = u, u.HomeDir
	}
	if b.io == "" {
		b.io = filepath.Join(home, "maclab", "io")
	}
	return b, nil
}

func main() {
	b, err := configure(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	b.whoami(ctx)
	log.Printf("lab-builder %s: %s, image %s, volume %s (%s), %s cpus, %s, build VMs %s-*", b.host, b.runtime, b.image, b.volume, kernelVolumeSize, b.cpus, b.mem, b.prefix)
	kinds := "kernel"
	if b.pkgImage != "" {
		kinds += ",package"
		log.Printf("package builds: image %s, volume %s (%s), %s jobs", b.pkgImage, b.pkgVolume, packageVolumeSize, b.pkgJobs)
	}
	for ctx.Err() == nil {
		var a api.BuildAssignment
		code, err := b.call(ctx, "POST", "/api/builder/poll?kinds="+kinds, nil, &a)
		if err != nil {
			log.Printf("poll: %v", err)
			sleep(ctx, 10*time.Second)
			continue
		}
		if code == 204 {
			continue
		}
		b.run(ctx, a)
	}
}

// container runs the runtime's CLI (container, or docker with its context),
// in the configured user's session if any.
func (b *builder) container(ctx context.Context, args ...string) *exec.Cmd {
	bin := b.containerBin
	if b.runtime == "docker" {
		bin = b.docker
		if b.dockerContext != "" {
			args = append([]string{"--context", b.dockerContext}, args...)
		}
	}
	var cmd *exec.Cmd
	if b.asUser == nil {
		cmd = exec.CommandContext(ctx, bin, args...)
	} else {
		full := append([]string{"asuser", b.asUser.Uid, "sudo", "-n", "-u", b.asUser.Username, bin}, args...)
		cmd = exec.CommandContext(ctx, b.launchctl, full...)
	}
	cmd.Env = childEnv(os.Environ())
	return cmd
}

var rePrefix = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,31}$`)

// builderToken reads this builder's token from its file, which must be a
// plain file of the account lab-builder runs as that nobody else can read.
// No error carries the token.
func builderToken(file string) (string, error) {
	if _, err := os.Lstat(file); os.IsNotExist(err) {
		return "", fmt.Errorf("this builder's token is not at %s: a Maintainer makes it with lab builder add, and the mini's set-up puts it there", file)
	}
	return oob.ReadToken(file)
}

// packageJobs is the job count package builds get: --pkg-jobs, else --cpus,
// a whole number from 1 to 1024.
func packageJobs(flagJobs, cpus string) (string, error) {
	v := flagJobs
	if v == "" {
		v = cpus
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 1024 {
		return "", fmt.Errorf("--pkg-jobs %q: a whole number from 1 to 1024 (default: --cpus)", v)
	}
	return strconv.Itoa(n), nil
}

// buildEnv is the environment a build VM gets: kbuild's for a kernel build,
// pkgbuild's for a package build, which includes the job count its recipe
// reads (make -j"${OMARCHY_BUILD_JOBS:-N}").
func (b *builder) buildEnv(bd api.Build, labVer string) []string {
	if bd.Kind == api.BuildPackage {
		return []string{"SRC_REPO=" + bd.Source.Repo, "SRC_SHA=" + bd.Source.SHA, "PKGREL=" + bd.Pkgrel, "OMARCHY_BUILD_JOBS=" + b.pkgJobs}
	}
	return []string{"URL=" + bd.Source.Repo, "SHA=" + bd.Source.SHA, "LABVER=" + labVer}
}

// childEnv is the environment for what lab-builder starts: its own, without
// any MACLAB_ variable.
func childEnv(env []string) []string {
	var out []string
	for _, e := range env {
		if !strings.HasPrefix(e, "MACLAB_") {
			out = append(out, e)
		}
	}
	return out
}

// whoami asks labd who it takes this builder for, and stops if labd refuses
// the token. A labd that can't be reached now is tried again by the poll loop.
func (b *builder) whoami(ctx context.Context) {
	var me api.BuilderSelf
	code, err := b.call(ctx, "GET", "/api/builder/self", nil, &me)
	switch {
	case code == 401 || code == 403:
		log.Fatalf("labd at %s refused this builder's token: %v", b.server, err)
	case err != nil:
		log.Printf("labd at %s: %v (polling anyway)", b.server, err)
	default:
		b.host = me.Name
		log.Printf("labd at %s knows this builder as %s (%s)", b.server, me.Name, strings.Join(me.Kinds, ","))
	}
}

// buildVM runs image with the cache volume on /cache and dir as /io, its
// output to out, and returns kbuild's error. Apple container shares dir
// itself. Docker (Colima) shares only paths under $HOME into its VM, and a
// bind mount of anything else is silently empty, so /io is copied in before
// the build and back out after it.
func (b *builder) buildVM(ctx context.Context, name, image, volume, dir string, env []string, out io.Writer) error {
	args := []string{"--name", name, "--cpus", b.cpus, "--memory", b.mem, "-v", volume + ":/cache"}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	if b.runtime != "docker" {
		cmd := b.container(ctx, append(append([]string{"run", "--rm"}, args...), "-v", dir+":/io", image)...)
		return b.stream(cmd, out)
	}
	defer b.container(context.Background(), "rm", "--force", name).Run()
	step := func(what string, args ...string) error {
		if o, err := b.container(ctx, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %v: %s", what, err, strings.TrimSpace(string(o)))
		}
		return nil
	}
	if err := step("creating the build container", append(append([]string{"create"}, args...), image)...); err != nil {
		return err
	}
	if err := step("copying the inputs in", "cp", dir+"/.", name+":/io"); err != nil {
		return err
	}
	if err := b.stream(b.container(ctx, "start", "--attach", name), out); err != nil {
		return err
	}
	return step("copying the outputs out", "cp", name+":/io/.", dir)
}

// stream runs cmd with its output to out.
func (b *builder) stream(cmd *exec.Cmd, out io.Writer) error {
	cmd.WaitDelay = 10 * time.Second
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the build VM: %w", err)
	}
	return cmd.Wait()
}

func (b *builder) chown(path string) {
	if b.asUser != nil {
		uid, _ := strconv.Atoi(b.asUser.Uid)
		gid, _ := strconv.Atoi(b.asUser.Gid)
		os.Chown(path, uid, gid)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (b *builder) call(ctx context.Context, method, path string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequestWithContext(ctx, method, b.server+path, rd)
	req.Header.Set("Authorization", "Bearer "+b.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return resp.StatusCode, json.Unmarshal(data, out)
	}
	return resp.StatusCode, nil
}

func (b *builder) download(ctx context.Context, sha, dst string) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", b.server+"/api/builder/artifacts/"+sha, nil)
	req.Header.Set("Authorization", "Bearer "+b.token)
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("config %s: %s", sha, resp.Status)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

// upload sends one output for build: in one request up to 32 MiB, in
// numbered parts beyond, since labd reads no body over 90 MB.
func (b *builder) upload(ctx context.Context, build string, f *os.File) (string, error) {
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	return upload.Sender{Client: labhttp.Client(b.rt, 30*time.Minute), URL: b.server + "/api/builder/artifacts",
		Query: url.Values{"build": {build}}, Token: b.token}.Send(ctx, f, st.Size())
}

// run executes one build and always reports back, success or not.
func (b *builder) run(ctx context.Context, a api.BuildAssignment) {
	bd := a.Build
	log.Printf("build %s (%s): %s %s", bd.ID, orStr(bd.Kind, "kernel"), bd.Source.Repo, bd.Source.SHA)
	t0 := time.Now()
	dir := filepath.Join(b.io, bd.ID)
	os.RemoveAll(dir)
	os.MkdirAll(dir, 0o755)
	defer os.RemoveAll(dir) // nothing accumulates on the host
	b.chown(dir)

	// Lines go to labd in batches; a cancel from labd stops the container.
	var mu sync.Mutex
	var pending []string
	push := func(l string) { mu.Lock(); pending = append(pending, l); mu.Unlock() }
	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	flushed := make(chan struct{})
	flush := func() {
		mu.Lock()
		lines := pending
		pending = nil
		mu.Unlock()
		var r struct{ Cancel bool }
		if _, err := b.call(ctx, "POST", "/api/builder/builds/"+bd.ID+"/progress", api.BuildProgress{Lines: lines}, &r); err != nil {
			log.Printf("progress: %v", err)
			mu.Lock()
			pending = append(lines, pending...)
			mu.Unlock()
		}
		if r.Cancel {
			cancel()
		}
	}
	go func() {
		defer close(flushed)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-bctx.Done():
				return
			case <-t.C:
				flush()
			}
		}
	}()

	result := api.BuildResult{}
	defer func() {
		cancel()
		<-flushed
		flush()
		result.Seconds = time.Since(t0).Seconds()
		if _, err := b.call(ctx, "POST", "/api/builder/builds/"+bd.ID+"/done", result, nil); err != nil {
			log.Printf("done: %v", err)
		}
		log.Printf("build %s finished in %s: %s%s", bd.ID, time.Since(t0).Round(time.Second), result.Release, result.Error)
	}()

	pkg := bd.Kind == api.BuildPackage
	image, volume := b.image, b.volume
	env := b.buildEnv(bd, a.LabVer)
	input, inputSHA := "config", bd.ConfigSHA
	if pkg {
		image, volume = b.pkgImage, b.pkgVolume
		input, inputSHA = "recipe.tar", bd.Recipe
		line := fmt.Sprintf("builder %s: packages from %s", b.host, orStr(bd.RecipeDir, "recipe "+bd.Recipe[:12]))
		if bd.Source.SHA != "" {
			line += fmt.Sprintf(" at %s @ %s", bd.Source.Repo, bd.Source.SHA)
		}
		push(line)
	} else {
		push(fmt.Sprintf("builder %s: %s @ %s", b.host, bd.Source.Repo, bd.Source.SHA))
	}
	if err := b.download(ctx, inputSHA, filepath.Join(dir, input)); err != nil {
		result.Error = err.Error()
		return
	}
	b.chown(filepath.Join(dir, input))
	vm := b.prefix + "-" + bd.ID
	pr, pw := io.Pipe()
	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			l := sc.Text()
			if strings.HasPrefix(l, "[") && strings.Contains(l, "/6]") {
				continue // container's own image-pull progress
			}
			push(l)
		}
		io.Copy(io.Discard, pr)
	}()
	err := b.buildVM(bctx, vm, image, volume, dir, env, pw)
	pw.Close()
	<-scanned
	if bctx.Err() != nil {
		if b.runtime == "docker" {
			b.container(context.Background(), "rm", "--force", vm).Run()
		} else {
			b.container(context.Background(), "stop", vm).Run()
			b.container(context.Background(), "delete", "--force", vm).Run()
		}
		result.Error = "canceled"
		return
	}
	if err != nil {
		if errors.As(err, new(*exec.ExitError)) {
			err = fmt.Errorf("kbuild: %w", err)
		}
		result.Error = err.Error()
		return
	}
	b.collect(ctx, bd, dir, pkg, &result, push)
}

// collect reads what a finished build left in its scratch dir and uploads
// it. kbuild always writes the release; pkgbuild writes one only when a
// package holds a kernel, so a package build without one (Mesa, m1n1)
// reports none.
func (b *builder) collect(ctx context.Context, bd api.Build, dir string, pkg bool, result *api.BuildResult, push func(string)) {
	out, err := openOutputs(dir)
	if err != nil {
		result.Error = err.Error()
		return
	}
	defer out.Close()
	rel, err := out.small("release")
	switch {
	case err == nil:
		result.Release = rel
	case pkg && errors.Is(err, fs.ErrNotExist):
	default:
		result.Error = err.Error()
		return
	}
	push("@@stage upload")
	b.uploadPackages(ctx, bd, out, result, push)
}

// uploadPackages uploads everything a build left in out/. The file named in
// kernel becomes the boot artifact; the other files remain named build
// outputs.
func (b *builder) uploadPackages(ctx context.Context, bd api.Build, out *outputs, result *api.BuildResult, push func(string)) {
	kernel, err := out.small("kernel")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		result.Error = err.Error()
		return
	}
	ents, err := out.list("out")
	if err != nil {
		result.Error = "no outputs: " + err.Error()
		return
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		f, st, err := out.regular("out/" + e.Name())
		if err != nil {
			result.Error = err.Error()
			return
		}
		sha, err := b.upload(ctx, bd.ID, f)
		if err != nil {
			result.Error = err.Error()
			return
		}
		push(fmt.Sprintf("uploaded %s sha256 %s", e.Name(), sha))
		result.Files = append(result.Files, api.BuildFile{Name: e.Name(), SHA256: sha, Size: st.Size()})
		if e.Name() == kernel {
			result.Artifact = sha
		}
	}
	if len(result.Files) == 0 {
		result.Error = "build made no outputs"
	}
}

// outputs is a build's scratch dir as lab-builder reads what the build left
// there. The build VM shares that dir, so any name in it may be a link the VM
// made to a file of this host: every output is resolved inside the dir
// (os.Root), never through a symlink, and only as a regular file.
type outputs struct{ root *os.Root }

func openOutputs(dir string) (*outputs, error) {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &outputs{r}, nil
}

func (o *outputs) Close() error { return o.root.Close() }

// regular opens name for reading if it is a regular file, reached through no
// symlink at all.
func (o *outputs) regular(name string) (*os.File, fs.FileInfo, error) {
	if err := o.noLinks(name); err != nil {
		return nil, nil, err
	}
	st, err := o.root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("build output %s is not a regular file (%s)", name, st.Mode().Type())
	}
	f, err := o.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	fst, err := f.Stat()
	if err != nil || !fst.Mode().IsRegular() || !os.SameFile(st, fst) {
		f.Close()
		return nil, nil, fmt.Errorf("build output %s changed while it was opened", name)
	}
	if n, ok := links(fst); !ok || n != 1 {
		f.Close()
		return nil, nil, fmt.Errorf("build output %s has %d links; an output is one file of its own", name, n)
	}
	return f, fst, nil
}

// links is a file's hard-link count.
func links(fi fs.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Nlink), true
}

// noLinks refuses a name any of whose directories is a symlink.
func (o *outputs) noLinks(name string) error {
	dir := filepath.Dir(filepath.Clean(name))
	for dir != "." && dir != "/" {
		st, err := o.root.Lstat(dir)
		if err != nil {
			return err
		}
		if !st.IsDir() {
			return fmt.Errorf("build output dir %s is not a directory (%s)", dir, st.Mode().Type())
		}
		dir = filepath.Dir(dir)
	}
	return nil
}

// small reads a short text output (release, kernel), trimmed.
func (o *outputs) small(name string) (string, error) {
	f, _, err := o.regular(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", err
	}
	if len(b) > 4096 {
		return "", fmt.Errorf("build output %s is longer than 4096 bytes", name)
	}
	return strings.TrimSpace(string(b)), nil
}

// list lists a directory of outputs, which must itself be a directory and
// not a link.
func (o *outputs) list(name string) ([]fs.DirEntry, error) {
	st, err := o.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("build output %s is not a directory (%s)", name, st.Mode().Type())
	}
	if err := o.noLinks(name + "/x"); err != nil {
		return nil, err
	}
	d, err := o.root.Open(name)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return d.ReadDir(-1)
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
