package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// testBuilder adds the builder the harnesses' made-up builders speak as, by
// the token "bld".
const testBuilderName = "mini"

func testBuilder(tb testing.TB, s *Server) {
	tb.Helper()
	b := api.Builder{Name: testBuilderName, Kinds: []string{api.BuilderKernel, api.BuilderPackage}, Added: s.now(), AddedBy: harnessMaintainer}
	if err := s.store.addBuilder(b, randHex(16), hashSecret("bld")); err != nil && !s.store.hasBuilder(testBuilderName) {
		tb.Fatal(err)
	}
}

// builderLab is a labd with no Macs, for builder tests: the first
// Maintainer's token is "lab_mtok", and hosts are the hosts a source may
// come from beside github.com.
type builderLab struct {
	t   *testing.T
	ctx context.Context
	dir string
	s   *Server
	url string
	n   int
	// transfers and wrap shape the labd start makes: the limits uploads work
	// inside, and a test's transport in front of labd's handler.
	transfers Transfers
	wrap      func(http.Handler) http.Handler
	hosts     []string
	budget    int64
}

func newBuilderLab(t *testing.T) *builderLab {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b := &builderLab{t: t, ctx: ctx, dir: t.TempDir()}
	b.start()
	return b
}

// start (re)starts labd on b.dir.
func (b *builderLab) start() {
	s, err := newWith(harnessMaintainer, "lab_mtok", b.ctx, Config{DataDir: b.dir, Transfers: b.transfers, SourceHosts: b.hosts, ArtifactBudget: b.budget},
		log.New(io.Discard, "", 0))
	if err != nil {
		b.t.Fatal(err)
	}
	h := s.Handler()
	if b.wrap != nil {
		h = b.wrap(h)
	}
	hs := httptest.NewUnstartedServer(h)
	hs.Config.ErrorLog = log.New(io.Discard, "", 0)
	hs.Start()
	b.t.Cleanup(hs.Close)
	b.s, b.url = s, hs.URL
}

// do sends a request with a bearer token and returns the status and body.
func (b *builderLab) do(ctx context.Context, method, path, token string, body io.Reader) (int, []byte) {
	req, _ := http.NewRequestWithContext(ctx, method, b.url+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

func (b *builderLab) call(method, path, token string, body any, out any) int {
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	code, data := b.do(b.ctx, method, path, token, rd)
	if out != nil {
		json.Unmarshal(data, out)
	}
	return code
}

func (b *builderLab) addBuilder(name string, kinds ...string) string {
	var nb api.NewBuilder
	if code := b.call("POST", "/api/builders", "lab_mtok", api.BuilderRequest{Name: name, Kinds: kinds}, &nb); code != 200 {
		b.t.Fatalf("add builder %s: %d", name, code)
	}
	return nb.Token
}

// queue queues a build of kind ("" kernel, "package") with an input artifact
// of its own.
func (b *builderLab) queue(kind string) *api.Build {
	b.n++
	in, _, err := b.s.storeArtifact(strings.NewReader(fmt.Sprintf("input %d", b.n)))
	if err != nil {
		b.t.Fatal(err)
	}
	bd := &api.Build{ID: fmt.Sprintf("b%04d", b.n), Key: fmt.Sprintf("key%04d", b.n), Kind: kind,
		Source: api.Source{Repo: "https://github.com/labtest/linux", SHA: strings.Repeat("a", 40)}, State: api.BuildQueued, Created: b.s.now()}
	if kind == api.BuildPackage {
		bd.Recipe = in
	} else {
		bd.ConfigSHA = in
	}
	if _, err := b.s.queueBuild(bd, true); err != nil {
		b.t.Fatal(err)
	}
	return bd
}

// poll polls once as token, giving up after wait.
func (b *builderLab) poll(token, query string, wait time.Duration) (api.BuildAssignment, int) {
	ctx, cancel := context.WithTimeout(b.ctx, wait)
	defer cancel()
	var a api.BuildAssignment
	code, data := b.do(ctx, "POST", "/api/builder/poll?"+query, token, nil)
	json.Unmarshal(data, &a)
	return a, code
}

func (b *builderLab) build(id string) *api.Build {
	bd, err := b.s.getBuild(id)
	if err != nil {
		b.t.Fatal(err)
	}
	return bd
}

func (b *builderLab) upload(token, build, content string) (string, int) {
	var up struct{ SHA256 string }
	code, data := b.do(b.ctx, "POST", "/api/builder/artifacts?build="+build, token, strings.NewReader(content))
	json.Unmarshal(data, &up)
	return up.SHA256, code
}

func (b *builderLab) done(token, build string, res api.BuildResult) int {
	return b.call("POST", "/api/builder/builds/"+build+"/done", token, res, nil)
}

func TestAddingANamedBuilder(t *testing.T) {
	b := newBuilderLab(t)
	tok := b.addBuilder("m5mbp")
	if !strings.HasPrefix(tok, "bldr_") || len(tok) != 45 {
		t.Fatalf("token %q", tok)
	}
	// labd keeps only the token's hash.
	for _, f := range []string{"labd.db", "labd.db-wal"} {
		if raw, _ := os.ReadFile(filepath.Join(b.dir, f)); bytes.Contains(raw, []byte(tok)) {
			t.Fatalf("%s holds the token", f)
		}
	}
	var me api.BuilderSelf
	if code := b.call("GET", "/api/builder/self", tok, nil, &me); code != 200 || me.Name != "m5mbp" ||
		strings.Join(me.Kinds, ",") != "kernel" {
		t.Fatalf("self: %d %+v", code, me)
	}
	var l []api.BuilderStatus
	if b.call("GET", "/api/builders", "lab_mtok", nil, &l); len(l) != 1 || l[0].Host != "m5mbp" || !l[0].Registered ||
		l[0].AddedBy != harnessMaintainer || l[0].Alive || !l[0].LastSeen.IsZero() {
		t.Fatalf("list %+v", l)
	}
	for _, c := range []struct {
		req  api.BuilderRequest
		code int
	}{
		{api.BuilderRequest{Name: "m5mbp"}, 409},
		{api.BuilderRequest{Name: "../x"}, 400},
		{api.BuilderRequest{Name: ""}, 400},
		{api.BuilderRequest{Name: "ok", Kinds: []string{"kernel", "root"}}, 400},
	} {
		if code := b.call("POST", "/api/builders", "lab_mtok", c.req, nil); code != c.code {
			t.Errorf("%+v: %d, want %d", c.req, code, c.code)
		}
	}
	// Only a Maintainer adds or removes builders.
	for _, token := range []string{"", tok, "bld", "bldr_" + strings.Repeat("0", 40)} {
		if code := b.call("POST", "/api/builders", token, api.BuilderRequest{Name: "x"}, nil); code != 401 {
			t.Errorf("add with %q: %d", token, code)
		}
		if code := b.call("DELETE", "/api/builders/m5mbp", token, nil, nil); code != 401 {
			t.Errorf("remove with %q: %d", token, code)
		}
	}
}

// A named token works on the builder routes only.
func TestNamedBuilderTokenOpensNothingElse(t *testing.T) {
	b := newBuilderLab(t)
	tok := b.addBuilder("m5mbp", "kernel", "package")
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/devices"}, {"GET", "/api/jobs"}, {"POST", "/api/jobs"}, {"POST", "/api/artifacts"},
		{"POST", "/api/builds"}, {"GET", "/api/builds"}, {"GET", "/api/builders"}, {"GET", "/api/maintainers"},
		{"POST", "/api/maintainers"}, {"POST", "/api/enroll-tokens"}, {"POST", "/api/agent/checkin"},
		{"GET", "/api/agent/artifacts/" + strings.Repeat("a", 64)}, {"GET", "/api/join/terms"},
	} {
		if code, _ := b.do(b.ctx, r.method, r.path, tok, strings.NewReader("{}")); code < 400 {
			t.Errorf("%s %s with a named builder's token: %d", r.method, r.path, code)
		}
	}
}

func TestNamedBuilderWorksOnlyOnItsOwnBuild(t *testing.T) {
	b := newBuilderLab(t)
	a, other := b.addBuilder("a"), b.addBuilder("b")
	k := b.queue("")
	stray := b.queue("") // another queued build's input
	got, code := b.poll(a, "host=evil&kinds=kernel", 5*time.Second)
	if code != 200 || got.Build.ID != k.ID || got.Build.Builder != "a" || b.build(k.ID).Builder != "a" {
		t.Fatalf("poll: %d %+v", code, got.Build)
	}
	// The other builder can't touch it.
	if code := b.call("POST", "/api/builder/builds/"+k.ID+"/progress", other, api.BuildProgress{Lines: []string{"x"}}, nil); code != 403 {
		t.Errorf("other's progress: %d", code)
	}
	if code := b.done(other, k.ID, api.BuildResult{Error: "x"}); code != 403 {
		t.Errorf("other's done: %d", code)
	}
	if _, code := b.upload(other, k.ID, "x"); code != 403 {
		t.Errorf("other's upload: %d", code)
	}
	if code, _ := b.do(b.ctx, "GET", "/api/builder/artifacts/"+k.ConfigSHA, other, nil); code != 404 {
		t.Errorf("other read the input: %d", code)
	}
	// Its builder reads its own input and nothing else.
	if code, body := b.do(b.ctx, "GET", "/api/builder/artifacts/"+k.ConfigSHA, a, nil); code != 200 || !strings.HasPrefix(string(body), "input") {
		t.Errorf("own input: %d %q", code, body)
	}
	if code, _ := b.do(b.ctx, "GET", "/api/builder/artifacts/"+stray.ConfigSHA, a, nil); code != 404 {
		t.Errorf("read another build's input: %d", code)
	}
	// An upload must name the build.
	if _, code := b.upload(a, "", "x"); code != 403 {
		t.Errorf("upload naming no build: %d", code)
	}
	if code := b.call("POST", "/api/builder/builds/"+k.ID+"/progress", a, api.BuildProgress{Lines: []string{"@@stage build", "CC x.o"}}, nil); code != 200 {
		t.Errorf("progress: %d", code)
	}
	sha, code := b.upload(a, k.ID, "kernel bytes")
	if code != 200 {
		t.Fatalf("upload: %d", code)
	}
	if code := b.done(a, k.ID, api.BuildResult{Release: "7.2-x", Artifact: sha, Seconds: 1}); code != 204 {
		t.Fatalf("done: %d", code)
	}
	if bd := b.build(k.ID); bd.State != api.BuildDone || bd.Artifact != sha || bd.Builder != "a" {
		t.Fatalf("finished: %+v", bd)
	}
	// Once done, the build and its input are no longer its.
	if code, _ := b.do(b.ctx, "GET", "/api/builder/artifacts/"+k.ConfigSHA, a, nil); code != 404 {
		t.Errorf("input after done: %d", code)
	}
	if _, code := b.upload(a, k.ID, "more"); code != 403 {
		t.Errorf("upload after done: %d", code)
	}
}

// done may name only what the builder uploaded for that build.
func TestNamedBuilderCantNameOthersOutputs(t *testing.T) {
	b := newBuilderLab(t)
	a := b.addBuilder("a")
	k1 := b.queue("")
	b.poll(a, "", 5*time.Second)
	own1, _ := b.upload(a, k1.ID, "k1 kernel")
	b.done(a, k1.ID, api.BuildResult{Error: "made up"})
	elsewhere, _, _ := b.s.storeArtifact(strings.NewReader("someone else's kernel"))
	for _, c := range []struct {
		name string
		res  api.BuildResult
		id   string
	}{
		{"an artifact it never uploaded", api.BuildResult{Artifact: elsewhere}, k1.ID},
		{"its upload for another build", api.BuildResult{Artifact: own1}, k1.ID},
	} {
		k := b.queue("")
		b.poll(a, "", 5*time.Second)
		own, _ := b.upload(a, k.ID, "own "+c.name)
		res := c.res
		res.Files = []api.BuildFile{{Name: "x.pkg", SHA256: own}}
		if code := b.done(a, k.ID, res); code != 204 {
			t.Fatalf("%s: done %d", c.name, code)
		}
		if bd := b.build(k.ID); bd.State != api.BuildFailed || !strings.Contains(bd.Error, "did not upload") {
			t.Errorf("%s: %s %q", c.name, bd.State, bd.Error)
		}
	}
}

func TestNamedBuilderKinds(t *testing.T) {
	b := newBuilderLab(t)
	kernelOnly, pkgOnly := b.addBuilder("k", "kernel"), b.addBuilder("p", "package")
	pkg := b.queue(api.BuildPackage)
	if a, code := b.poll(kernelOnly, "kinds=kernel,package", 1500*time.Millisecond); code == 200 {
		t.Fatalf("kernel-only builder got %+v", a.Build)
	}
	if a, code := b.poll(pkgOnly, "kinds=kernel,package", 5*time.Second); code != 200 || a.Build.ID != pkg.ID {
		t.Fatalf("package builder: %d %+v", code, a.Build)
	}
	if code, _ := b.do(b.ctx, "GET", "/api/builder/artifacts/"+pkg.Recipe, pkgOnly, nil); code != 200 {
		t.Errorf("recipe: %d", code)
	}
	k := b.queue("")
	if a, code := b.poll(pkgOnly, "kinds=kernel,package", 1500*time.Millisecond); code == 200 {
		t.Fatalf("package-only builder got kernel build %+v", a.Build)
	}
	if a, code := b.poll(kernelOnly, "", 5*time.Second); code != 200 || a.Build.ID != k.ID {
		t.Fatalf("kernel builder: %d %+v", code, a.Build)
	}
}

func TestRemovingABuilder(t *testing.T) {
	b := newBuilderLab(t)
	a := b.addBuilder("a")
	k := b.queue("")
	b.poll(a, "", 5*time.Second)
	// An open long poll ends when the builder is removed.
	polled := make(chan int, 1)
	go func() { _, code := b.poll(a, "", 30*time.Second); polled <- code }()
	time.Sleep(300 * time.Millisecond)
	var rm api.RemovedBuilder
	if code := b.call("DELETE", "/api/builders/a", "lab_mtok", nil, &rm); code != 200 || rm.Removed != "a" ||
		strings.Join(rm.FailedBuilds, ",") != k.ID {
		t.Fatalf("remove: %d %+v", code, rm)
	}
	select {
	case code := <-polled:
		if code == 200 {
			t.Fatal("a removed builder's poll got a build")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the open poll outlived its builder")
	}
	if bd := b.build(k.ID); bd.State != api.BuildFailed || bd.Error != "builder a was removed by "+harnessMaintainer {
		t.Fatalf("its build: %s %q", bd.State, bd.Error)
	}
	if code := b.call("GET", "/api/builder/self", a, nil, nil); code != 401 {
		t.Errorf("removed token: %d", code)
	}
	if code := b.call("DELETE", "/api/builders/a", "lab_mtok", nil, nil); code != 404 {
		t.Errorf("remove again: %d", code)
	}
	var l []api.BuilderStatus
	if b.call("GET", "/api/builders", "lab_mtok", nil, &l); len(l) != 0 {
		t.Errorf("a removed builder is still listed: %+v", l)
	}
	// The same name again is a new builder: the old token stays dead, and the
	// new one inherits nothing, not even a build recorded under its name.
	a2 := b.addBuilder("a")
	if code := b.call("GET", "/api/builder/self", a, nil, nil); code != 401 {
		t.Errorf("old token after re-adding the name: %d", code)
	}
	ghost := b.queue("")
	ghost.State, ghost.Builder = api.BuildRunning, "a"
	b.s.saveBuild(ghost)
	if code := b.call("POST", "/api/builder/builds/"+ghost.ID+"/progress", a2, api.BuildProgress{Lines: []string{"x"}}, nil); code != 403 {
		t.Errorf("new builder reported on a build it never took: %d", code)
	}
	if code := b.done(a2, ghost.ID, api.BuildResult{Error: "x"}); code != 403 {
		t.Errorf("new builder finished a build it never took: %d", code)
	}
	if code := b.done(a2, k.ID, api.BuildResult{Error: "x"}); code != 403 {
		t.Errorf("new builder finished the old one's build: %d", code)
	}
}

// Removing a builder ends its upload at once, even one whose sender has
// stalled mid-body and sends nothing more, and records nothing.
func TestRemovingABuilderMidUpload(t *testing.T) {
	b := newBuilderLab(t)
	a := b.addBuilder("a")
	k := b.queue("")
	b.poll(a, "", 5*time.Second)
	pr, pw := io.Pipe()
	defer pw.Close()
	answered := make(chan int, 1)
	go func() {
		code, _ := b.do(b.ctx, "POST", "/api/builder/artifacts?build="+k.ID, a, pr)
		answered <- code
	}()
	pw.Write(bytes.Repeat([]byte("k"), 1<<16))
	time.Sleep(300 * time.Millisecond) // labd has read that and waits for more
	if code := b.call("DELETE", "/api/builders/a", "lab_mtok", nil, nil); code != 200 {
		t.Fatalf("remove: %d", code)
	}
	select {
	case code := <-answered:
		if code == 200 {
			t.Fatal("a removed builder's upload was accepted")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stalled upload outlived its builder")
	}
	b.s.builds.state.Lock()
	cl := b.s.builds.claims[k.ID]
	b.s.builds.state.Unlock()
	if cl != nil {
		t.Fatalf("claim survived removal: %+v", cl)
	}
	if bd := b.build(k.ID); bd.State != api.BuildFailed {
		t.Fatalf("build %s", bd.State)
	}
}

// Removing by generation removes only that generation, never a builder
// added under the same name since.
func TestRemovingABuilderByGeneration(t *testing.T) {
	b := newBuilderLab(t)
	var first api.NewBuilder
	b.call("POST", "/api/builders", "lab_mtok", api.BuilderRequest{Name: "a"}, &first)
	if len(first.Generation) != 32 {
		t.Fatalf("generation %q", first.Generation)
	}
	b.call("DELETE", "/api/builders/a", "lab_mtok", nil, nil)
	second := b.addBuilder("a")
	if code := b.call("DELETE", "/api/builders/a?generation="+first.Generation, "lab_mtok", nil, nil); code != 409 {
		t.Fatalf("stale generation: %d", code)
	}
	if code := b.call("GET", "/api/builder/self", second, nil, nil); code != 200 {
		t.Fatalf("the newer builder after a stale removal: %d", code)
	}
	if code := b.call("DELETE", "/api/builders/zz?generation="+first.Generation, "lab_mtok", nil, nil); code != 404 {
		t.Fatalf("no such builder: %d", code)
	}
	var nb api.NewBuilder
	b.call("POST", "/api/builders", "lab_mtok", api.BuilderRequest{Name: "c"}, &nb)
	if code := b.call("DELETE", "/api/builders/c?generation="+nb.Generation, "lab_mtok", nil, nil); code != 200 {
		t.Fatalf("own generation: %d", code)
	}
	if code := b.call("GET", "/api/builder/self", nb.Token, nil, nil); code != 401 {
		t.Fatalf("removed by generation, token still works: %d", code)
	}
}

// A builder removed while it polls never ends up holding a build.
func TestRemoveRacesClaim(t *testing.T) {
	b := newBuilderLab(t)
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("r%d", i)
		tok := b.addBuilder(name)
		k := b.queue("")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); b.poll(tok, "", 3*time.Second) }()
		go func() { defer wg.Done(); b.call("DELETE", "/api/builders/"+name, "lab_mtok", nil, nil) }()
		wg.Wait()
		if bd := b.build(k.ID); bd.State == api.BuildRunning {
			t.Fatalf("round %d: build %s runs on removed builder %s", i, bd.ID, bd.Builder)
		}
		b.s.builds.state.Lock()
		n := len(b.s.builds.claims)
		b.s.builds.state.Unlock()
		if n != 0 {
			t.Fatalf("round %d: %d claims left", i, n)
		}
		if bd := b.build(k.ID); bd.State == api.BuildQueued {
			bd.State = api.BuildCanceled // out of the next round's way
			b.s.saveBuild(bd)
		}
	}
}

// Builders polling at once never take the same build twice.
func TestConcurrentClaimsTakeEachBuildOnce(t *testing.T) {
	b := newBuilderLab(t)
	toks := []string{b.addBuilder("x"), b.addBuilder("y"), b.addBuilder("z")}
	const n = 12
	for i := 0; i < n; i++ {
		b.queue("")
	}
	var mu sync.Mutex
	took := map[string]int{}
	var wg sync.WaitGroup
	for _, tok := range toks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				a, code := b.poll(tok, "", 1500*time.Millisecond)
				if code != 200 {
					return
				}
				mu.Lock()
				took[a.Build.ID]++
				mu.Unlock()
				b.done(tok, a.Build.ID, api.BuildResult{Error: "made up"})
			}
		}()
	}
	wg.Wait()
	if len(took) != n {
		t.Fatalf("%d builds taken, want %d", len(took), n)
	}
	for id, c := range took {
		if c != 1 {
			t.Errorf("%s taken %d times", id, c)
		}
	}
}

// One build at a time: a builder that runs a build is handed no other until
// that one has ended.
func TestABuilderRunsOneBuildAtATime(t *testing.T) {
	b := newBuilderLab(t)
	tok := b.addBuilder("mini", "kernel", "package")
	first, second := b.queue(""), b.queue(api.BuildPackage)
	if a, code := b.poll(tok, "kinds=kernel,package", 5*time.Second); code != 200 || a.Build.ID != first.ID {
		t.Fatalf("the first poll: %d %+v", code, a.Build)
	}
	if a, code := b.poll(tok, "kinds=kernel,package", 1500*time.Millisecond); code == 200 {
		t.Fatalf("a builder running %s was handed %s as well", first.ID, a.Build.ID)
	}
	if bd := b.build(second.ID); bd.State != api.BuildQueued {
		t.Fatalf("the second build is %s", bd.State)
	}
	b.done(tok, first.ID, api.BuildResult{Error: "made up"})
	if a, code := b.poll(tok, "kinds=kernel,package", 5*time.Second); code != 200 || a.Build.ID != second.ID {
		t.Fatalf("after the first ended: %d %+v", code, a.Build)
	}
}

func TestNamedUploadCap(t *testing.T) {
	old := builderUploadMax
	builderUploadMax = 1024
	defer func() { builderUploadMax = old }()
	b := newBuilderLab(t)
	a := b.addBuilder("a")
	k := b.queue("")
	b.poll(a, "", 5*time.Second)
	// No Content-Length: the cap holds while the body streams.
	pr, pw := io.Pipe()
	go func() { pw.Write(bytes.Repeat([]byte("x"), 4096)); pw.Close() }()
	if code, _ := b.do(b.ctx, "POST", "/api/builder/artifacts?build="+k.ID, a, pr); code != 413 {
		t.Fatalf("over the cap: %d", code)
	}
	b.s.builds.state.Lock()
	n := len(b.s.builds.claims[k.ID].uploads)
	b.s.builds.state.Unlock()
	if n != 0 {
		t.Fatalf("%d uploads recorded", n)
	}
	if _, code := b.upload(a, k.ID, "small"); code != 200 {
		t.Fatalf("under the cap: %d", code)
	}
}

// A builder's claim does not survive a labd restart.
func TestClaimsEndOnRestart(t *testing.T) {
	b := newBuilderLab(t)
	a := b.addBuilder("a")
	named := b.queue("")
	b.poll(a, "", 5*time.Second)
	b.start()
	// Settled before labd serves: no later pass can touch a new claim.
	if bd := b.build(named.ID); bd.State != api.BuildFailed || !strings.Contains(bd.Error, "labd restarted") {
		t.Fatalf("build after restart: %s %q", bd.State, bd.Error)
	}
	fresh := b.queue("")
	if got, code := b.poll(a, "", 5*time.Second); code != 200 || got.Build.ID != fresh.ID {
		t.Fatalf("claim after restart: %d %+v", code, got.Build)
	}
	time.Sleep(300 * time.Millisecond)
	if bd := b.build(fresh.ID); bd.State != api.BuildRunning {
		t.Fatalf("a claim made after the restart: %s %q", bd.State, bd.Error)
	}
	if code := b.call("POST", "/api/builder/builds/"+fresh.ID+"/progress", a, api.BuildProgress{Lines: []string{"x"}}, nil); code != 200 {
		t.Fatalf("progress on the fresh claim: %d", code)
	}
	if code := b.call("POST", "/api/builder/builds/"+named.ID+"/progress", a, api.BuildProgress{Lines: []string{"x"}}, nil); code != 403 {
		t.Errorf("progress after restart: %d", code)
	}
	if code, _ := b.do(b.ctx, "GET", "/api/builder/artifacts/"+named.ConfigSHA, a, nil); code != 404 {
		t.Errorf("input after restart: %d", code)
	}
}

// No shared builder token is made or accepted. What the old lab kept in its
// data directory, and what its builder had in its environment, open
// nothing; a builder's own token is the only thing that does.
func TestNoSharedBuilderToken(t *testing.T) {
	const old = "bld_0123456789abcdef0123456789abcdef"
	t.Setenv("MACLAB_BUILDER"+"_TOKEN", old)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b := &builderLab{t: t, ctx: ctx, dir: t.TempDir()}
	if err := os.WriteFile(filepath.Join(b.dir, "builder.token"), []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.start()
	k := b.queue("")
	routes := []struct{ method, path string }{
		{"GET", "/api/builder/self"},
		{"POST", "/api/builder/poll"},
		{"POST", "/api/builder/builds/" + k.ID + "/progress"},
		{"POST", "/api/builder/builds/" + k.ID + "/done"},
		{"POST", "/api/builder/artifacts"},
		{"POST", "/api/builder/artifacts/uploads"},
		{"GET", "/api/builder/artifacts/" + k.ConfigSHA},
	}
	for _, token := range []string{old, "bld", "", "lab_mtok"} {
		for _, rt := range routes {
			if code, _ := b.do(b.ctx, rt.method, rt.path, token, strings.NewReader("{}")); code != 401 {
				t.Errorf("%s %s with %q: %d, want 401", rt.method, rt.path, token, code)
			}
		}
	}
	if bd := b.build(k.ID); bd.State != api.BuildQueued {
		t.Fatalf("the build is %s", bd.State)
	}
	tok := b.addBuilder("mini")
	if a, code := b.poll(tok, "", 5*time.Second); code != 200 || a.Build.ID != k.ID || b.build(k.ID).Builder != "mini" {
		t.Fatalf("the builder's own token: %d %+v", code, a.Build)
	}
	// A builder is known by its own name, whatever host it says it is.
	other := b.addBuilder("other")
	b.poll(other, "host=mini", 500*time.Millisecond)
	var l []api.BuilderStatus
	b.call("GET", "/api/builders", "lab_mtok", nil, &l)
	if len(l) != 2 || l[0].Host != "mini" || l[0].Build != k.ID || l[1].Host != "other" || !l[1].Alive || l[1].Build != "" {
		t.Fatalf("list %+v", l)
	}
	// labd made no token of its own, and left the old file as it was.
	entries, _ := os.ReadDir(b.dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "token") && e.Name() != "builder.token" {
			t.Errorf("labd made %s", e.Name())
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(b.dir, "builder.token")); string(raw) != old+"\n" {
		t.Errorf("the old lab's file was rewritten")
	}
}

// An artifact fetch must prove a running build whose input the artifact is;
// uploads and the result are tied to that build.
func TestAFetchNeedsARunningBuildWithThatInput(t *testing.T) {
	b := newBuilderLab(t)
	tok := b.addBuilder("mini", "kernel", "package")
	mine, later := b.queue(""), b.queue("")
	other, _, _ := b.s.storeArtifact(strings.NewReader("somebody's kernel"))
	fetch := func(sha string) int {
		code, _ := b.do(b.ctx, "GET", "/api/builder/artifacts/"+sha, tok, nil)
		return code
	}
	if fetch(mine.ConfigSHA) != 404 {
		t.Error("an input was served before its build was taken")
	}
	b.poll(tok, "", 5*time.Second)
	for sha, want := range map[string]int{mine.ConfigSHA: 200, later.ConfigSHA: 404, other: 404, strings.Repeat("0", 64): 404} {
		if got := fetch(sha); got != want {
			t.Errorf("fetch of %s while running %s: %d, want %d", short(sha), mine.ID, got, want)
		}
	}
	if _, code := b.upload(tok, later.ID, "x"); code != 403 {
		t.Errorf("an upload for a build it does not run: %d", code)
	}
	if _, code := b.upload(tok, "", "x"); code != 403 {
		t.Errorf("an upload for no build: %d", code)
	}
	sha, code := b.upload(tok, mine.ID, "kernel")
	if code != 200 {
		t.Fatalf("its own upload: %d", code)
	}
	// A result naming what it did not upload for this build fails the build.
	if code := b.done(tok, mine.ID, api.BuildResult{Artifact: other, Release: "7.2-x"}); code != 204 || b.build(mine.ID).State != api.BuildFailed {
		t.Fatalf("a result naming another's artifact: %d %s", code, b.build(mine.ID).State)
	}
	if fetch(mine.ConfigSHA) != 404 {
		t.Error("an input was served after its build ended")
	}
	_ = sha
}

func TestNeverUsed(t *testing.T) {
	st, err := openStore(filepath.Join(t.TempDir(), "lab.db"))
	if err != nil {
		t.Fatal(err)
	}
	pristine := api.Device{ID: "d1", Name: "m1", State: api.StateNew}
	if !neverUsed(st, pristine) {
		t.Fatal("a pristine enrollment counts as used")
	}
	for _, c := range []struct {
		name string
		d    func(*api.Device)
	}{
		{"active job", func(d *api.Device) { d.ActiveJob = "j1" }},
		{"known-good kernel", func(d *api.Device) { d.KnownGood = "7.2.11" }},
		{"ready", func(d *api.Device) { d.State = api.StateReady }},
		{"busy", func(d *api.Device) { d.State = api.StateBusy }},
		{"recovering", func(d *api.Device) { d.State = api.StateRecovering }},
		{"offline", func(d *api.Device) { d.State = api.StateOffline }},
		{"no name", func(d *api.Device) { d.Name = "" }},
	} {
		d := pristine
		c.d(&d)
		if neverUsed(st, d) {
			t.Errorf("%s: counts as never used", c.name)
		}
	}
	for i, j := range []api.Job{
		{State: api.JobDone, Outcome: api.OutcomePass},
		{State: api.JobDone, Outcome: api.OutcomeCanceled},
		{State: api.JobQueued},
	} {
		st2, _ := openStore(filepath.Join(t.TempDir(), "lab.db"))
		j.ID, j.Spec.Device, j.Created = fmt.Sprintf("j%d", i), "m1", time.Now()
		st2.saveJob(&j)
		if neverUsed(st2, pristine) {
			t.Errorf("history %s/%s: counts as never used", j.State, j.Outcome)
		}
	}
	st.db.Close()
	if neverUsed(st, pristine) {
		t.Error("a failed lookup counts as never used")
	}
}

// labd's startup decides from the stored record, before it clears the active
// job and turns busy Macs offline.
func TestStartupCleanupSkipsOnlyNeverUsedMacs(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(filepath.Join(dir, "labd.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []api.Device{
		{ID: "1", Name: "pristine", State: api.StateNew},
		{ID: "2", Name: "midjob", State: api.StateBusy, ActiveJob: "j9"},
		{ID: "3", Name: "newwithjob", State: api.StateNew, ActiveJob: "j8"},
		{ID: "4", Name: "ready", State: api.StateReady, KnownGood: "7.2"},
		{ID: "5", Name: "history", State: api.StateNew},
	} {
		st.saveDevice(&d, "h"+d.ID)
	}
	st.saveJob(&api.Job{ID: "j1", Spec: api.JobSpec{Device: "history"}, State: api.JobDone, Outcome: api.OutcomeCanceled, Created: time.Now()})
	st.db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := newWith(harnessMaintainer, "lab_mtok", ctx, Config{DataDir: dir}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"pristine": false, "midjob": true, "newwithjob": true, "ready": true, "history": true} {
		v := s.dev(name)
		v.mu.Lock()
		got := v.dirty
		v.mu.Unlock()
		if got != want {
			t.Errorf("%s: dirty %v, want %v", name, got, want)
		}
	}
}

// teamLab is a lab with a Maintainer (ravi), two M team members (grace and
// linus), each with a Lab token as lab holds one, and two Macs.
type teamLab struct {
	*signInLab
	ravi, grace, linus string
}

func newTeamLab(t *testing.T, config func(*Config)) *teamLab {
	t.Helper()
	l := &teamLab{signInLab: pagesLab(t, config)}
	l.gh.AddUser(lrGrace, "grace")
	l.gh.AddUser(lrLinus, "linus")
	l.ravi = l.lrMaintainer().makeToken("laptop", "90")
	grace, linus := l.lrMember(lrGrace), l.lrMember(lrLinus)
	l.grace, l.linus = grace.makeToken("laptop", "90"), linus.makeToken("laptop", "90")
	l.lrMac("m1-pro", "j314s", nil)
	l.lrMac("m2-mini", "j473", nil)
	grace.mustLease("m1-pro", "grace@laptop", 0)
	linus.mustLease("m2-mini", "linus@laptop", 0)
	return l
}

func (l *teamLab) artifact(text string) string {
	l.t.Helper()
	sha, _, err := l.s.storeArtifact(strings.NewReader(text))
	if err != nil {
		l.t.Fatal(err)
	}
	return sha
}

// as sends a request with a Lab token, as lab does.
func (l *teamLab) as(token, method, path, body string) (int, string) {
	l.t.Helper()
	hdr := []string{"Content-Type", "application/json"}
	if token != "" {
		hdr = append(hdr, "Authorization", "Bearer "+token)
	}
	w := l.browser().do(method, path, body, hdr...)
	return w.Code, w.Body.String()
}

func (l *teamLab) askBuild(token string, req api.BuildRequest) (int, string) {
	l.t.Helper()
	body, _ := json.Marshal(req)
	return l.as(token, "POST", "/api/builds", string(body))
}

func (l *teamLab) allBuilds() []*api.Build {
	l.t.Helper()
	all, err := l.s.store.builds(`1=1 ORDER BY created ASC`)
	if err != nil {
		l.t.Fatal(err)
	}
	return all
}

func inOneLine(body string) bool { return strings.Count(strings.TrimSpace(body), "\n") == 0 }

// M team builds are not open: an M team member's lab build and lab package
// are refused in one line, whatever Mac they lease, and nothing is queued.
// A Maintainer builds, and the build records who asked.
func TestAnMTeamMembersBuildIsRefused(t *testing.T) {
	l := newTeamLab(t, nil)
	recipe := l.artifact("a recipe")
	for what, req := range map[string]api.BuildRequest{
		"lab build":             {Source: "https://github.com/o/r", Device: "m1-pro"},
		"lab build --config":    {Source: "https://github.com/o/r", Config: l.artifact("CONFIG_X=y")},
		"lab package":           {Recipe: recipe},
		"lab package --source":  {Recipe: recipe, Source: "https://github.com/o/r"},
		"a source off the list": {Source: "https://gitlab.com/o/r", Device: "m1-pro"},
	} {
		code, body := l.askBuild(l.grace, req)
		if code != 403 || strings.TrimSpace(body) != teamBuildsClosed || !inOneLine(body) {
			t.Errorf("%s by an M team member: %d %q", what, code, body)
		}
	}
	if all := l.allBuilds(); len(all) != 0 {
		t.Fatalf("%d builds were queued for an M team member", len(all))
	}

	code, body := l.askBuild(l.ravi, api.BuildRequest{Recipe: recipe})
	var b api.Build
	json.Unmarshal([]byte(body), &b)
	if code != 200 || b.State != api.BuildQueued || b.AskedBy != "ravi" || b.AskedByID != maintainerID || b.ByTeam {
		t.Fatalf("a Maintainer's build: %d %s", code, body)
	}
	// Who asked is labd's to say, not the request's.
	code, body = l.as(l.ravi, "POST", "/api/builds", `{"recipe":"`+recipe+`","pkgrel":"2","asked_by":"grace","asked_by_id":8101,"by_team":true,"AskedBy":"grace"}`)
	json.Unmarshal([]byte(body), &b)
	if code != 200 || b.AskedBy != "ravi" || b.AskedByID != maintainerID || b.ByTeam {
		t.Fatalf("a request that names who asked: %d %s", code, body)
	}

	// Nobody else reaches the route.
	for who, token := range map[string]string{"nobody": "", "a person with no role": l.signedIn(nobodyID).makeToken("mine", "90")} {
		if code, body := l.askBuild(token, api.BuildRequest{Recipe: recipe}); (code != 401 && code != 403) || !inOneLine(body) {
			t.Errorf("%s: %d %q", who, code, body)
		}
	}
}

// Once M team builds open: a member asks only for a build for a Mac they
// lease, has one build queued or running at a time, and waits behind the
// Maintainers' builds.
func TestTheMTeamsQueueRule(t *testing.T) {
	l := newTeamLab(t, func(c *Config) { c.MTeamBuilds = true })
	testBuilder(t, l.s)
	recipe := l.artifact("a recipe")
	l.s.store.setConfig("m1-pro", lrKnownGood, l.artifact("CONFIG_X=y"))
	l.s.store.setConfig("m2-mini", lrKnownGood, l.artifact("CONFIG_X=y"))

	for what, req := range map[string]api.BuildRequest{
		"no Mac":            {Recipe: recipe},
		"a Mac of another":  {Source: gitRepo(t), Device: "m2-mini"},
		"a Mac that is not": {Source: gitRepo(t), Device: "m9"},
	} {
		if code, body := l.askBuild(l.grace, req); code != 403 || !strings.Contains(body, "a Mac they lease") || !inOneLine(body) {
			t.Errorf("%s: %d %q", what, code, body)
		}
	}
	var first api.Build
	code, body := l.askBuild(l.grace, api.BuildRequest{Source: gitRepo(t), Device: "m1-pro"})
	json.Unmarshal([]byte(body), &first)
	if code != 200 || first.State != api.BuildQueued || first.AskedBy != "grace" || first.AskedByID != lrGrace || !first.ByTeam {
		t.Fatalf("a member's build for the Mac they lease: %d %s", code, body)
	}
	if code, body := l.askBuild(l.grace, api.BuildRequest{Source: gitRepo(t), Device: "m1-pro"}); code != 409 ||
		!strings.Contains(body, first.ID) || !strings.Contains(body, "one build queued or running") || !inOneLine(body) {
		t.Fatalf("a second build while the first is queued: %d %q", code, body)
	}
	// Another member's queue is their own.
	var theirs api.Build
	code, body = l.askBuild(l.linus, api.BuildRequest{Source: gitRepo(t), Device: "m2-mini"})
	json.Unmarshal([]byte(body), &theirs)
	if code != 200 || theirs.AskedBy != "linus" {
		t.Fatalf("another member's build: %d %s", code, body)
	}

	// A Maintainer's build, asked for last, is built first.
	var mine api.Build
	code, body = l.askBuild(l.ravi, api.BuildRequest{Recipe: recipe})
	json.Unmarshal([]byte(body), &mine)
	if code != 200 {
		t.Fatalf("a Maintainer's build: %d %s", code, body)
	}
	take := func() string {
		t.Helper()
		code, body := l.as("bld", "POST", "/api/builder/poll?kinds=kernel,package", "")
		var a api.BuildAssignment
		json.Unmarshal([]byte(body), &a)
		if code != 200 {
			t.Fatalf("poll: %d %s", code, body)
		}
		return a.Build.ID
	}
	fail := func(id string) {
		t.Helper()
		if code, body := l.as("bld", "POST", "/api/builder/builds/"+id+"/done", `{"error":"made up"}`); code != 204 {
			t.Fatalf("done: %d %s", code, body)
		}
	}
	if got := take(); got != mine.ID {
		t.Fatalf("the Builder took %s first, want the Maintainer's %s", got, mine.ID)
	}
	// Still running counts as the member's one.
	fail(mine.ID)
	if got := take(); got != first.ID {
		t.Fatalf("then the Builder took %s, want %s", got, first.ID)
	}
	if code, _ := l.askBuild(l.grace, api.BuildRequest{Source: gitRepo(t), Device: "m1-pro"}); code != 409 {
		t.Fatalf("a second build while the first runs: %d", code)
	}
	fail(first.ID)
	if code, body := l.askBuild(l.grace, api.BuildRequest{Source: gitRepo(t), Device: "m1-pro"}); code != 200 {
		t.Fatalf("a build after the first ended: %d %s", code, body)
	}
	// The Lease gone, so is the build.
	if code, body := l.as(l.grace, "DELETE", "/api/devices/m1-pro/lease", ""); code != 200 {
		t.Fatalf("release: %d %s", code, body)
	}
	fail(take())
	fail(take())
	if code, body := l.askBuild(l.grace, api.BuildRequest{Source: gitRepo(t), Device: "m1-pro"}); code != 403 || !strings.Contains(body, "a Mac they lease") {
		t.Fatalf("a build for a Mac no longer leased: %d %q", code, body)
	}
}

// An M team member uploads an artifact only for a job on a Mac they lease;
// a Maintainer uploads as before.
func TestAnMTeamMemberUploadsOnlyForAMacTheyLease(t *testing.T) {
	l := newTeamLab(t, nil)
	sum := func(text string) string {
		h := sha256.Sum256([]byte(text))
		return hex.EncodeToString(h[:])
	}
	start := func(text string) string {
		return `{"sha256":"` + sum(text) + `","size":` + strconv.Itoa(len(text)) + `}`
	}
	for what, query := range map[string]string{"no Mac": "", "another member's Mac": "?device=m2-mini", "no such Mac": "?device=m9"} {
		for _, c := range []struct{ path, body string }{
			{"/api/artifacts" + query, "a kernel"},
			{"/api/artifacts/uploads" + query, start("a kernel")},
			{"/api/artifacts/uploads/" + sum("a kernel") + "/close" + query, ""},
		} {
			code, body := l.as(l.grace, "POST", c.path, c.body)
			if code != 403 || strings.TrimSpace(body) != teamUploadsNeed || !inOneLine(body) {
				t.Errorf("%s, POST %s: %d %q", what, c.path, code, body)
			}
		}
		if code, _ := l.as(l.grace, "PUT", "/api/artifacts/uploads/"+sum("a kernel")+"/parts/0"+query, "a kernel"); code != 403 {
			t.Errorf("%s, a part: %d", what, code)
		}
	}
	// What lab build and lab package upload first is refused as the build is.
	for _, query := range []string{"?purpose=build", "?purpose=build&device=m1-pro"} {
		if code, body := l.as(l.grace, "POST", "/api/artifacts"+query, "a kernel"); code != 403 || strings.TrimSpace(body) != teamBuildsClosed {
			t.Errorf("a build's input %s: %d %q", query, code, body)
		}
	}
	if _, err := os.Stat(l.s.artifactPath(sum("a kernel"))); err == nil {
		t.Fatal("a refused upload was stored")
	}
	code, body := l.as(l.grace, "POST", "/api/artifacts?device=m1-pro", "a kernel")
	if code != 200 || !strings.Contains(body, sum("a kernel")) {
		t.Fatalf("an upload for the Mac they lease: %d %q", code, body)
	}
	if code, body := l.as(l.grace, "POST", "/api/artifacts/uploads?device=m1-pro", start("a bigger kernel")); code != 200 {
		t.Fatalf("an upload in parts for the Mac they lease: %d %q", code, body)
	}
	if code, body := l.as(l.grace, "DELETE", "/api/devices/m1-pro/lease", ""); code != 200 {
		t.Fatalf("release: %d %s", code, body)
	}
	if code, _ := l.as(l.grace, "POST", "/api/artifacts?device=m1-pro", "another kernel"); code != 403 {
		t.Errorf("an upload after the Lease ended: %d", code)
	}
	if code, body := l.as(l.ravi, "POST", "/api/artifacts", "a Maintainer's kernel"); code != 200 {
		t.Errorf("a Maintainer's upload: %d %q", code, body)
	}
	if code, _ := l.as(l.signedIn(nobodyID).makeToken("mine", "90"), "POST", "/api/artifacts?device=m1-pro", "x"); code != 403 {
		t.Errorf("an upload by a person with no role: %d", code)
	}
}

// A poll whose builder has gone away takes no build off the queue: the
// build would read as running on a builder that never got it. The poll is
// cut while labd is busy with its builds, and the build is queued after.
func TestAnAbandonedPollTakesNoBuild(t *testing.T) {
	b := newBuilderLab(t)
	token := b.addBuilder("mini", api.BuilderKernel)
	ctx, cut := context.WithCancel(b.ctx)
	defer cut()
	r := httptest.NewRequest("POST", "http://127.0.0.1/api/builder/poll", nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("User-Agent", "lab-builder/test")
	answered := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		b.s.Handler().ServeHTTP(w, r)
		answered <- w.Code
	}()
	time.Sleep(200 * time.Millisecond)
	b.s.builds.state.Lock()
	cut()
	time.Sleep(200 * time.Millisecond)
	queued := b.queue("")
	b.s.builds.state.Unlock()
	select {
	case code := <-answered:
		if code == 200 {
			t.Errorf("the abandoned poll was handed a build")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the abandoned poll never ended")
	}
	if got := b.build(queued.ID).State; got != api.BuildQueued {
		t.Errorf("the build is %s after an abandoned poll, want it still queued", got)
	}
	if a, code := b.poll(token, "", 5*time.Second); code != 200 || a.Build.ID != queued.ID {
		t.Errorf("the builder that asks next got no build: %d %+v", code, a)
	}
}
