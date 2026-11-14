// Command lab is the CLI for Omarchy Mac Lab: enroll Macs, run kernels and
// tests on them, and see what state they are in. `lab mcp` serves the same
// operations to AI agents over MCP.
package main

import (
	"bufio"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"golang.org/x/term"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/client"
	"github.com/omacom/omarchy-maclab/internal/labhttp"
)

const usage = `lab: run kernels and tests on the Macs of Omarchy Mac Lab

  lab boot-test --catalog-descriptor PIN --profile ID --request FILE --review-descriptor PIN --out DIR [--compile | --execute --authorization FILE]
  lab login <url> [--ca-file <pem>] [--allow-plain-http]
                                      log in to Omarchy Mac Lab: asks for a Lab token, which you make on your own
                                      page of the lab and never give as an argument
  lab builder add <name> [--kinds kernel[,package]] [-o FILE] | remove <name> [--generation G] | list
                                      builder hosts with their own token (it may only build)
  lab status [device]                 devices, what they're doing, and anything a human must do
  lab enroll <name> [--label "MacBook Air M1, left desk"] [--model laptop|desktop]
                                      a Lab-owned Mac: prints the Join command to run on it, good for 1 hour
                                      (--revoke: cancel it unused). A Volunteer's Mac is lent by its own
                                      Volunteer, signed in, from "Lend a Mac" on the site
  lab remove <mac>                    end a Mac's enrollment: its device secret stops working at once, and lab-agent
                                      removes the lab from the Mac at its next check-in (a Volunteer is told)
  lab device <name> [--label ..] [--model ..]
  lab baseline <device>               prove the one-shot boot works; required once per Mac
  lab build <github-url> --device <mac> | --config <file>   build a kernel from a repo, branch, commit or PR URL (reused if built before)
  lab package <recipe-dir> [--commit <github-url>] [--pkgrel 11.14] [-o dir]
                                      build release packages from a PKGBUILD recipe with makepkg, as the release machine does
  lab builds | lab build-log <build> [-f] | lab build-get <build> [-o dir]
  lab exec <device> <command...>      run a command on a Mac as root, through lab-agent. It needs your own Lease on
                                      the Mac, a Maintainer's too. The command's text is kept in the Exec record
                                      for 12 months: never put a secret in a command
  lab run <device> [--kernel <file|sha256|build-id|github-url>] [--cmdline ".."] [--cmdline-base known-good|default|"<cmdline>"] [--cmdline-strip "glob .."] [--verbose-boot] [--test boot-health] [--gui-test gui-smoke]
                   [--script t.sh] [--gui-script t.sh] [--boot-timeout 240] [--script-timeout 90m] [--publish] [--omt-allow ids] [--no-wait]
  lab crashtest <device>              deliberately panic the Mac and check it recovers by itself
  lab pack --build <O= dir> [-o kernel.tar.zst]    package a kernel build for lab run
  lab jobs [device] | lab job <id> | lab wait <id> | lab cancel <id> | lab logs <id> [file]
  lab gate <kernel> [--families m1,m2] [--volunteers] [--exclude job,..] [--evidence file]
                                      did a kernel (build id or sha256 prefix) pass on every chip family? exits 3 if not.
                                      A family is pass, fail, flaky (passed and failed: not a pass), stage_failed or untested.
                                      --exclude sets jobs aside for this report only, e.g. a failure the lab caused
  lab exclude <job> --reason "..." | lab exclude <job> --clear
                                      set a finished job aside in every lab gate report, e.g. a failure the lab
                                      caused; the gate lists who set it aside, why and when. --clear counts it again
  lab retry <job> [--no-wait]         run a finished job again: same Mac, kernel (or source commit), tests and cmdline
  lab publish <job>                   upload a known-good run's omarchy-m-test report to omarchy-m-testing.org
  lab lease <device> [--ttl 4h] | lab release <device> [--force]
                                      take a Lease on a Mac, for 4 hours unless --ttl says otherwise (a Maintainer
                                      up to 24h): until it ends only your jobs run on it, and others' queued
                                      jobs wait. Running it again renews the Lease: it ends 4 hours from then.
                                      An M team member holds one Mac of each Configuration. Only you release
                                      your Lease; --force ends another person's (Maintainers), and is recorded
  lab installed list                  every Release with its verdict
  lab installed freeze-manifest --release R <manifest.json> <payload.sha256> [--approved-by a,b] [--for MAC]
                                      from then the Release is listed, with you as who froze it. An M team
                                      member holds a Lease on one of the Release's Macs and names it (--for)
  lab installed freeze-entry <device> --release R    freeze the installer's boot entry, after the install: labd reads
                                      the Mac through its read-only identity check, not an exec. Needs your own
                                      Lease on the Mac; refused outside the Availability window and on a
                                      Volunteer's Mac
  lab installed verdict --release R qualified|not-qualified --note "…" [--yes]
                                      record the verdict: the first is any M team member's, every later one a
                                      Maintainer's. "qualified" needs a passed Installed run of the Release
  lab installed verdict --release R --revoke --note "…" [--yes]      revoke it (Maintainers)
  lab installed promote <device> --release R --job <id> [--yes]      accepted by you (Maintainers)
                                      references + known-good, labd's and the agent's (Mac on that kernel, agent 0.7.3+);
                                      needs that Mac's own passed Installed run; refused on a Volunteer's Mac
  lab installed status --release R                   the Release's Macs, runs and verdict history, and its frozen files
                                      verdict and promote say what they are about to do and ask once; --yes answers
  lab reset <device>                  run the recovery ladder on an idle Mac by hand; a Mac that left
                                      (lab-agent uninstall) goes back in the lab first
  lab oob <device>
  lab audit <device>                  labd's copy of a Volunteer's Mac's Audit log: every Maintainer action on it
  lab upload <file> [--for <mac>]     to the artifact store, in 32 MiB parts; cut, the same command resumes
  lab mcp                             MCP server on stdio, for AI agents
  lab version                         the Lab release this lab came from, and the one Omarchy Mac Lab runs
`

// The tag of the Lab release this build came from, stamped in by the Makefile.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	client.Release = version
	ctx := context.Background()
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "boot-test":
		err = bootTest(args)
	case "login":
		err = login(args, os.Stdin, os.Stdout)
	case "forget", "maintainer", "shell", "console", "serial", "recovery-mode":
		err = retired(cmd)
	case "pack":
		err = pack(args)
	case "mcp":
		err = serveMCP(ctx)
	case "help", "-h", "--help":
		fmt.Print(usage)
	case "version":
		err = showVersion(ctx, os.Stdout)
	default:
		var c *client.Client
		if c, err = client.Load(); err == nil {
			c.Note = func(format string, args ...any) { fmt.Fprintf(os.Stderr, "lab: "+format+"\n", args...) }
			err = dispatch(ctx, c, cmd, args)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lab:", err)
		os.Exit(1)
	}
}

// leaseLine is what lab lease prints for a Lease taken or renewed.
func leaseLine(device string, l *api.Lease) string {
	return fmt.Sprintf("%s leased by %s until %s", device, l.Who(), l.Expires.Local().Format("15:04:05 on Mon 2 Jan"))
}

func holder() string {
	if h := os.Getenv("MACLAB_HOLDER"); h != "" {
		return h
	}
	u, _ := user.Current()
	h, _ := os.Hostname()
	name := "someone"
	if u != nil {
		name = u.Username
	}
	return name + "@" + h
}

// parse lets flags come after positional arguments, as in `lab run m1air --kernel k.tar`.
// enrollOpts are lab enroll's flags. It enrolls Lab-owned Macs only: a
// Volunteer's Mac is lent by its own Volunteer from the site, so nothing
// here names a Volunteer, a contact or a login.
type enrollOpts struct {
	label, model *string
	revoke       *bool
}

func enrollFlags(fs *flag.FlagSet) enrollOpts {
	return enrollOpts{
		label:  fs.String("label", "", "where the Mac physically is, for power-cycle instructions"),
		model:  fs.String("model", api.ModelLaptop, "laptop or desktop"),
		revoke: fs.Bool("revoke", false, "cancel the Join commands for <name>, used or not, instead of making one"),
	}
}

// deviceOpts are lab device's flags: a Mac's label and its model, and
// nothing else. Whose a Mac is and which port resets it are not set here.
type deviceOpts struct{ label, model *string }

func deviceFlags(fs *flag.FlagSet) deviceOpts {
	return deviceOpts{label: fs.String("label", "", ""), model: fs.String("model", "", "")}
}

func parse(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for len(args) > 0 {
		fs.Parse(args)
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	return pos
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func need(pos []string, n int, what string) error {
	if len(pos) < n {
		return fmt.Errorf("usage: %s", what)
	}
	return nil
}

func dispatch(ctx context.Context, c *client.Client, cmd string, args []string) error {
	if err := retired(cmd); err != nil {
		return err
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "status", "devices":
		pos := parse(fs, args)
		if len(pos) > 0 {
			return showDevice(ctx, c, pos[0])
		}
		return showDevices(ctx, c)

	case "enroll":
		o := enrollFlags(fs)
		pos := parse(fs, args)
		if err := need(pos, 1, `lab enroll <name> [--label ..] [--model laptop|desktop] | lab enroll <name> --revoke`); err != nil {
			return err
		}
		if *o.revoke {
			n, err := c.RevokeEnrollTokens(ctx, pos[0])
			if err != nil {
				return err
			}
			fmt.Printf("revoked %d enrollment token(s) for %s\n", n, pos[0])
			return nil
		}
		t, err := c.EnrollToken(ctx, api.EnrollTokenRequest{Name: pos[0], Label: *o.label, Model: *o.model})
		if err != nil {
			return err
		}
		fmt.Printf("Join command for %s, a Lab-owned Mac (one Mac, once; expires %s). Keep it private.\n\n", pos[0], t.Expires.Format(time.DateTime))
		if t.JoinCommand != "" {
			fmt.Printf("Run this in a terminal on the Mac, as yourself; it checks the Mac, shows the lab's terms, and joins once you type yes:\n\n  %s\n\n", t.JoinCommand)
		} else {
			fmt.Printf("labd has no lab-agent to hand out: copy lab-agent to the Mac and run\n\n  printf '%%s\\n' '%s' | sudo ./lab-agent setup --server %s --token-stdin --gui-user \"$USER\"\n\n", t.Token, c.URL)
		}
		fmt.Printf("Cancel it unused: lab enroll %s --revoke. Then: lab baseline %s\n", pos[0], pos[0])
		return nil

	case "device":
		o := deviceFlags(fs)
		pos := parse(fs, args)
		if err := need(pos, 1, "lab device <name> [--label ..] [--model ..]"); err != nil {
			return err
		}
		var p api.DevicePatch
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "label":
				p.Label = o.label
			case "model":
				p.Model = o.model
			}
		})
		if _, err := c.PatchDevice(ctx, pos[0], p); err != nil {
			return err
		}
		return showDevice(ctx, c, pos[0])

	case "baseline":
		mode := fs.String("recovery-mode", "", "network-only (empty inherits current policy)")
		transaction := fs.String("recovery-transaction", "", "selected recovery transaction identity")
		noWait := fs.Bool("no-wait", false, "")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab baseline <device>"); err != nil {
			return err
		}
		return submit(ctx, c, api.JobSpec{Device: pos[0], Baseline: true, RecoveryMode: *mode, RecoveryTransaction: *transaction, Holder: holder()}, *noWait)

	case "crashtest":
		mode := fs.String("recovery-mode", "", "network-only (empty inherits current policy)")
		transaction := fs.String("recovery-transaction", "", "selected recovery transaction identity")
		noWait := fs.Bool("no-wait", false, "")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab crashtest <device>"); err != nil {
			return err
		}
		return submit(ctx, c, api.JobSpec{Device: pos[0], Crash: "panic", RecoveryMode: *mode, RecoveryTransaction: *transaction, Holder: holder()}, *noWait)

	case "run":
		mode := fs.String("recovery-mode", "", "network-only (empty inherits current policy)")
		transaction := fs.String("recovery-transaction", "", "selected recovery transaction identity")
		kernel := fs.String("kernel", "", "kernel artifact: a file to upload, or a sha256 already uploaded")
		cmdline := fs.String("cmdline", "", "extra kernel arguments, appended to the base")
		runConfig := fs.String("config", "", "with a source URL --kernel: build with this .config (file or uploaded sha256) instead of the Mac's own")
		cmdlineBase := fs.String("cmdline-base", "", `cmdline the test boot starts from: known-good (default), "default" (the distro's stock KERNEL_CMDLINE[default]), or a literal cmdline with root=`)
		cmdlineStrip := fs.String("cmdline-strip", "", `globs of base parameters to drop, space- or comma-separated, e.g. 'asahi.* apple_t6030_display.* dcpext_*'`)
		verboseBoot := fs.Bool("verbose-boot", false, "boot with loglevel=7 and without quiet/splash, to watch the console (default: keep the Mac's own)")
		bootTimeout := fs.Int("boot-timeout", 0, "seconds to wait for the Mac to come back (default 240)")
		noWait := fs.Bool("no-wait", false, "queue and return")
		scriptTimeout := fs.Duration("script-timeout", 0, "time each --script/--gui-script test may run, e.g. 90m for a hands-on session (default 5m, at most 4h)")
		omtAllow := fs.String("omt-allow", "", "omarchy-m-test checks this kernel is expected to fail, comma-separated (hardware.drivers:<compatible> for one unbound node)")
		publish := fs.Bool("publish", false, "publish the omarchy-m-test report to omarchy-m-testing.org (known-good kernel runs only)")
		installedMan := fs.String("installed", "", "boot the INSTALLED release through its installer entry and check it against a frozen expected manifest (file or sha256); see `lab installed`")
		mesaMode := fs.String("mesa-mode", api.MesaStaged, "with --mesa-package: staged (a diagnostic of the packages in a private prefix; the desktop keeps the installed Mesa)")
		mesaOfficial := fs.Bool("mesa-official-gpu", false, "optional reviewed official65 GPU suite after staged native provider checks (requires adapter support)")
		mesaPublic := fs.String("mesa-signing-public-sha256", "", "with --mesa-official-gpu: actual existing GUI machine-key.pub file SHA256")
		mesaState := fs.String("mesa-signing-state-home", "", "with --mesa-official-gpu: explicit existing normal XDG_STATE_HOME (never inferred from private job HOME)")
		mesaBuild := fs.String("mesa-build", "", "with --mesa-package: the package build that produced the packages, for the record")
		var tests, guiTests, scripts, guiScripts, mesaPkgs multi
		fs.Var(&mesaPkgs, "mesa-package", "a pinned Mesa split package to test, name=FILE or name=SHA256 (repeatable), e.g. mesa-aurora=mesa-aurora-…pkg.tar.zst")
		fs.Var(&tests, "test", "builtin test to run (repeatable)")
		fs.Var(&guiTests, "gui-test", "builtin GUI test to run in the Wayland session (repeatable)")
		fs.Var(&scripts, "script", "shell script test to upload and run (repeatable)")
		fs.Var(&guiScripts, "gui-script", "shell script test to run in the Wayland session (repeatable)")
		var payload multi
		fs.Var(&payload, "payload", "a file for the --script tests to read from $MACLAB_IN, NAME=FILE or NAME=SHA256 (repeatable), e.g. ane.ko=./ane.ko")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab run <device> [flags]"); err != nil {
			return err
		}
		c.UploadFor = pos[0]
		spec := api.JobSpec{Device: pos[0], Cmdline: *cmdline, RecoveryMode: *mode, RecoveryTransaction: *transaction, BootTimeoutSec: *bootTimeout, Holder: holder(), Publish: *publish}
		spec.OMTAllow = splitAllow(*omtAllow)
		spec.CmdlineBase = *cmdlineBase
		spec.CmdlineStrip = strings.FieldsFunc(*cmdlineStrip, func(r rune) bool { return r == ',' || r == ' ' })
		spec.VerboseBoot = *verboseBoot
		if *installedMan != "" {
			sha, err := manifestSHA(*installedMan)
			if err != nil {
				return err
			}
			spec.InstalledManifest = sha
		}
		if len(mesaPkgs) > 0 {
			m := &api.MesaSpec{Mode: *mesaMode, Build: *mesaBuild, OfficialGPU: *mesaOfficial, SigningPublicSHA256: *mesaPublic, SigningStateHome: *mesaState}
			for _, p := range mesaPkgs {
				name, src, ok := strings.Cut(p, "=")
				if !ok || name == "" || src == "" {
					return fmt.Errorf("--mesa-package %q: want name=FILE or name=SHA256", p)
				}
				sha, err := artifact(ctx, c, src)
				if err != nil {
					return fmt.Errorf("--mesa-package %s: %w", name, err)
				}
				m.Packages = append(m.Packages, api.MesaPackage{Name: name, SHA256: sha})
			}
			spec.Mesa = m
		} else if *mesaBuild != "" || *mesaMode != api.MesaStaged || *mesaOfficial || *mesaPublic != "" || *mesaState != "" {
			return errors.New("--mesa-mode and --mesa-build need at least one --mesa-package")
		}
		if *scriptTimeout > 4*time.Hour {
			return fmt.Errorf("--script-timeout %s: at most 4h", *scriptTimeout)
		}
		switch {
		case isSource(*kernel):
			spec.Source = *kernel
			if *runConfig != "" {
				sha, err := artifact(ctx, c, *runConfig)
				if err != nil {
					return err
				}
				spec.Config = sha
			}
		case reBuildID.MatchString(*kernel):
			b, err := c.GetBuild(ctx, *kernel)
			if err != nil {
				return err
			}
			if b.State != api.BuildDone || b.Artifact == "" {
				return fmt.Errorf("build %s is %s and has no kernel to boot", b.ID, b.State)
			}
			spec.Kernel = b.Artifact
		case *kernel != "":
			sha, err := artifact(ctx, c, *kernel)
			if err != nil {
				return err
			}
			spec.Kernel = sha
		}
		for _, t := range tests {
			spec.Tests = append(spec.Tests, api.TestSpec{Name: t, Builtin: t})
		}
		for _, t := range guiTests {
			spec.Tests = append(spec.Tests, api.TestSpec{Name: t, Builtin: t, GUI: true})
		}
		for i, list := range [][]string{scripts, guiScripts} {
			for _, p := range list {
				sha, err := c.Upload(ctx, p)
				if err != nil {
					return err
				}
				name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
				spec.Tests = append(spec.Tests, api.TestSpec{Name: name, Script: sha, GUI: i == 1, TimeoutSec: int(scriptTimeout.Seconds())})
			}
		}
		files, err := payloadFiles(ctx, c, payload)
		if err != nil {
			return err
		}
		if spec.Tests, err = withPayload(spec.Tests, files); err != nil {
			return err
		}
		return submit(ctx, c, spec, *noWait)

	case "build":
		dev := fs.String("device", "", "build with this Mac's running config")
		config := fs.String("config", "", "build with this .config (a file to upload, or an uploaded sha256) instead of a Mac's; one build boots on every Mac")
		force := fs.Bool("force", false, "rebuild even if this exact kernel was built before")
		noWait := fs.Bool("no-wait", false, "")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab build <github-url> --device <mac> | --config <file>"); err != nil {
			return err
		}
		req := api.BuildRequest{Source: pos[0], Device: *dev, Force: *force}
		c.UploadForBuild = true
		if *config != "" {
			if *dev != "" {
				return fmt.Errorf("give --device or --config, not both")
			}
			sha, err := artifact(ctx, c, *config)
			if err != nil {
				return err
			}
			req.Config, req.ConfigName = sha, filepath.Base(*config)
		}
		b, err := c.Build(ctx, req)
		if err != nil {
			return err
		}
		fmt.Printf("build %s: %s @ %s (%s)\n", b.ID, b.Source.Repo, b.Source.SHA[:12], b.State)
		return followBuild(ctx, c, b, *noWait, "")

	case "package":
		commit := fs.String("commit", "", "point the recipe's _commit at this GitHub commit, branch or PR URL (checksums are refreshed)")
		pkgrel := fs.String("pkgrel", "", "set pkgrel, e.g. 11.14")
		force := fs.Bool("force", false, "rebuild even if this exact recipe was built before")
		noWait := fs.Bool("no-wait", false, "")
		out := fs.String("o", "", "download the packages and the PKGBUILD as built into this directory")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab package <recipe-dir> [--commit <url>] [--pkgrel N] [-o dir]"); err != nil {
			return err
		}
		c.UploadForBuild = true
		sha, names, err := uploadRecipe(ctx, c, pos[0])
		if err != nil {
			return err
		}
		abs, _ := filepath.Abs(pos[0])
		b, err := c.Build(ctx, api.BuildRequest{Recipe: sha, RecipeDir: abs, Source: *commit, Pkgrel: *pkgrel, Force: *force})
		if err != nil {
			return err
		}
		fmt.Printf("build %s: packages from %s (%s) (%s)\n", b.ID, pos[0], strings.Join(names, ", "), b.State)
		if b.Source.SHA != "" {
			fmt.Printf("  _commit -> %s @ %s\n", b.Source.Repo, b.Source.SHA)
		}
		return followBuild(ctx, c, b, *noWait, *out)

	case "build-get":
		out := fs.String("o", ".", "directory")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab build-get <build> [-o dir]"); err != nil {
			return err
		}
		b, err := c.GetBuild(ctx, pos[0])
		if err != nil {
			return err
		}
		return getBuildFiles(ctx, c, b, *out)

	case "installed":
		return installedCmd(ctx, c, args)

	case "builder":
		return builderCmd(ctx, c, args)

	case "builds":
		l, err := c.Builds(ctx, 20)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "BUILD\tSTATE\tSOURCE\tCOMMIT\tRELEASE\tTIME")
		for _, b := range l {
			src, commit := shortSrc(b.Source), ""
			if b.Source.SHA != "" {
				commit = b.Source.SHA[:12]
			}
			if b.Kind == api.BuildPackage {
				src = "pkg " + filepath.Base(b.RecipeDir)
				if b.Pkgrel != "" {
					src += " -" + b.Pkgrel
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", b.ID, buildState(b), trunc(src, 44), commit, b.Release, fmtSecs(b.Seconds))
		}
		return w.Flush()

	case "build-log":
		follow := fs.Bool("f", false, "follow")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab build-log <build> [-f]"); err != nil {
			return err
		}
		return c.BuildLog(ctx, pos[0], *follow, os.Stdout)

	case "exec":
		timeout := fs.Int("timeout", 120, "seconds")
		fs.Usage = func() {
			fmt.Fprintln(fs.Output(), "usage:", execUsage)
			fs.PrintDefaults()
		}
		pos := parse(fs, args)
		if err := need(pos, 2, execUsage); err != nil {
			return err
		}
		res, err := c.Exec(ctx, pos[0], api.ExecArgs{Command: strings.Join(pos[1:], " "), TimeoutSec: *timeout})
		if err != nil {
			return err
		}
		os.Stdout.WriteString(res.Stdout)
		os.Stderr.WriteString(res.Stderr)
		os.Exit(res.ExitCode)
		return nil

	case "jobs":
		pos := parse(fs, args)
		dev := ""
		if len(pos) > 0 {
			dev = pos[0]
		}
		jobs, err := c.Jobs(ctx, dev, 20)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "JOB\tDEVICE\tSTATE\tOUTCOME\tCREATED\tSUMMARY")
		for _, j := range jobs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, j.Spec.Device, j.State, j.Outcome, j.Created.Format("01-02 15:04"), trunc(j.Summary, 70))
		}
		return w.Flush()

	case "job":
		pos := parse(fs, args)
		if err := need(pos, 1, "lab job <id>"); err != nil {
			return err
		}
		j, err := c.Job(ctx, pos[0])
		if err != nil {
			return err
		}
		printJob(j, true)
		return nil

	case "wait":
		pos := parse(fs, args)
		if err := need(pos, 1, "lab wait <id>"); err != nil {
			return err
		}
		return follow(ctx, c, pos[0])

	case "gate":
		fams := fs.String("families", "", "chip families that must pass, comma-separated (default all: m1,m1-pro,m2,m2-pro,m3,m3-pro)")
		evidence := fs.String("evidence", "", "write the report to this file as a JSON evidence record")
		volunteers := fs.Bool("volunteers", false, "count volunteers' Macs (enrolled with ./volunteer) too; by default they're listed, not counted")
		exclude := fs.String("exclude", "", "jobs to set aside for this report only, comma-separated, e.g. a failure the lab caused; they're listed as excluded")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab gate <kernel> [--families m1,m2] [--volunteers] [--exclude job,..] [--evidence file]"); err != nil {
			return err
		}
		var list, excluded []string
		if *fams != "" {
			list = strings.Split(*fams, ",")
		}
		if *exclude != "" {
			excluded = strings.Split(*exclude, ",")
		}
		rep, err := c.Gate(ctx, pos[0], list, *volunteers, excluded)
		if err != nil {
			return err
		}
		if *evidence != "" {
			b, _ := json.MarshalIndent(rep, "", "  ")
			if err := os.WriteFile(*evidence, append(b, '\n'), 0o644); err != nil {
				return err
			}
		}
		printGate(rep)
		if rep.Result != api.GatePass {
			os.Exit(3)
		}
		return nil

	case "exclude":
		reason := fs.String("reason", "", "why the job says nothing about its kernel, e.g. \"the access point went down mid-test\" (required)")
		again := fs.Bool("clear", false, "count the job in lab gate again")
		pos := parse(fs, args)
		const use = `lab exclude <job> --reason "..." | lab exclude <job> --clear`
		if err := need(pos, 1, use); err != nil {
			return err
		}
		if *again {
			if *reason != "" {
				return fmt.Errorf("--clear takes no --reason; usage: %s", use)
			}
			if _, err := c.ClearExclusion(ctx, pos[0]); err != nil {
				return err
			}
			fmt.Printf("job %s counts in lab gate again\n", pos[0])
			return nil
		}
		if strings.TrimSpace(*reason) == "" {
			return fmt.Errorf("say why with --reason; usage: %s", use)
		}
		j, err := c.Exclude(ctx, pos[0], *reason, holder())
		if err != nil {
			return err
		}
		x := j.GateExclusion
		if x == nil {
			return fmt.Errorf("labd answered, but didn't keep the exclusion: is it older than lab exclude?")
		}
		fmt.Printf("job %s (%s on %s) is set aside in every lab gate report, by %s at %s: %s. `lab exclude %s --clear` counts it again.\n",
			j.ID, j.Outcome, j.Spec.Device, x.By, x.Time.Local().Format(time.DateTime), x.Reason, j.ID)
		return nil

	case "publish":
		pos := parse(fs, args)
		if err := need(pos, 1, "lab publish <job>"); err != nil {
			return err
		}
		url, err := c.Publish(ctx, pos[0])
		if err != nil {
			return err
		}
		fmt.Println(url)
		return nil

	case "retry":
		noWait := fs.Bool("no-wait", false, "queue and return")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab retry <job>"); err != nil {
			return err
		}
		j, err := c.Retry(ctx, pos[0], holder())
		if err != nil {
			return err
		}
		fmt.Printf("job %s queued on %s (a retry of %s)\n", j.ID, j.Spec.Device, pos[0])
		if *noWait {
			return nil
		}
		return follow(ctx, c, j.ID)

	case "cancel":
		pos := parse(fs, args)
		if err := need(pos, 1, "lab cancel <id>"); err != nil {
			return err
		}
		out, err := c.Cancel(ctx, pos[0])
		fmt.Print(out)
		return err

	case "logs":
		pos := parse(fs, args)
		if err := need(pos, 1, "lab logs <id> [file]"); err != nil {
			return err
		}
		if len(pos) == 1 {
			j, err := c.Job(ctx, pos[0])
			if err != nil {
				return err
			}
			for _, l := range j.Result.Logs {
				fmt.Println(l)
			}
			for _, t := range j.Result.Tests {
				for _, f := range t.Files {
					fmt.Println(f)
				}
			}
			return nil
		}
		b, err := c.File(ctx, pos[0], pos[1])
		if err != nil {
			return err
		}
		os.Stdout.Write(b)
		return nil

	case "lease":
		ttl := fs.Duration("ttl", 0, "how long the Lease lasts; the lab's 4 hours when not given")
		who := fs.String("holder", holder(), "")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab lease <device> [--ttl 4h]"); err != nil {
			return err
		}
		l, err := c.Lease(ctx, pos[0], *who, *ttl)
		if err != nil {
			return err
		}
		fmt.Println(leaseLine(pos[0], l))
		if l.Warning != "" {
			fmt.Fprintln(os.Stderr, "warning: "+l.Warning)
		}
		return nil

	case "release":
		who := fs.String("holder", holder(), "")
		force := fs.Bool("force", false, "end the Lease even if another person took it (Maintainers); the lab records that you did")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab release <device> [--force]"); err != nil {
			return err
		}
		if *force {
			return c.ForceRelease(ctx, pos[0])
		}
		return c.Release(ctx, pos[0], *who)

	case "reset":
		pos := parse(fs, args)
		if err := need(pos, 1, "lab reset <device>"); err != nil {
			return err
		}
		out, err := c.Reset(ctx, pos[0], holder())
		fmt.Print(out)
		return err

	case "remove":
		pos := parse(fs, args)
		if err := need(pos, 1, "lab remove <mac>"); err != nil {
			return err
		}
		if err := c.EndEnrollment(ctx, pos[0]); err != nil {
			return err
		}
		fmt.Printf("ended %s's enrollment; its device secret no longer works\n", pos[0])
		return nil

	case "audit":
		pos := parse(fs, args)
		if err := need(pos, 1, "lab audit <device>"); err != nil {
			return err
		}
		es, err := c.Audit(ctx, pos[0])
		if err != nil {
			return err
		}
		for _, e := range es {
			fmt.Println(e.Line(time.Local))
		}
		return nil

	case "oob":
		pos := parse(fs, args)
		if err := need(pos, 1, "lab oob <device>"); err != nil {
			return err
		}
		st, err := c.OOBStatus(ctx, pos[0])
		if err != nil {
			return err
		}
		for k, v := range st {
			fmt.Printf("%s: %v\n", k, v)
		}
		return nil

	case "upload":
		fs.StringVar(&c.UploadFor, "for", "", "the Mac the file is for; an M team member uploads only for a job on a Mac they lease")
		pos := parse(fs, args)
		if err := need(pos, 1, "lab upload <file> [--for <mac>]"); err != nil {
			return err
		}
		sha, err := c.Upload(ctx, pos[0])
		if err == nil {
			fmt.Println(sha)
		}
		return err
	}
	return fmt.Errorf("unknown command %q (lab help)", cmd)
}

var reSHA = regexp.MustCompile(`^[0-9a-f]{64}$`)

func artifact(ctx context.Context, c *client.Client, v string) (string, error) {
	if reSHA.MatchString(v) {
		return v, nil
	}
	fmt.Fprintf(os.Stderr, "uploading %s... ", v)
	sha, err := c.Upload(ctx, v)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "%s\n", sha[:12])
	return sha, nil
}

// payloadFiles uploads the files of a --payload list, each NAME=FILE or NAME=SHA256.
func payloadFiles(ctx context.Context, c *client.Client, list []string) ([]api.PayloadFile, error) {
	var files []api.PayloadFile
	for _, p := range list {
		name, src, ok := strings.Cut(p, "=")
		if !ok || name == "" || src == "" {
			return nil, fmt.Errorf("--payload %q: want NAME=FILE or NAME=SHA256", p)
		}
		sha, err := artifact(ctx, c, src)
		if err != nil {
			return nil, fmt.Errorf("--payload %s: %w", name, err)
		}
		files = append(files, api.PayloadFile{Name: name, SHA256: sha})
	}
	return files, nil
}

// withPayload gives the files to every root script test: a GUI script runs as
// the Volunteer, so it takes none. It is an error to give files with no script.
func withPayload(tests []api.TestSpec, files []api.PayloadFile) ([]api.TestSpec, error) {
	if len(files) == 0 {
		return tests, nil
	}
	n := 0
	for i := range tests {
		if tests[i].Script != "" && !tests[i].GUI {
			tests[i].Payload = files
			n++
		}
	}
	if n == 0 {
		return nil, errors.New("--payload needs a --script test to read the files")
	}
	return tests, nil
}

func submit(ctx context.Context, c *client.Client, spec api.JobSpec, noWait bool) error {
	j, err := c.Submit(ctx, spec)
	if err != nil {
		return err
	}
	fmt.Printf("job %s queued on %s\n", j.ID, spec.Device)
	if noWait {
		return nil
	}
	return follow(ctx, c, j.ID)
}

// follow prints a job's events as they happen, then its result. Each wait is
// one call labd holds for at most 50 s, until the job has more to show.
func follow(ctx context.Context, c *client.Client, id string) error {
	seen := 0
	for {
		j, err := c.WaitPast(ctx, id, seen)
		if err != nil {
			return err
		}
		for _, e := range j.Events[min(seen, len(j.Events)):] {
			fmt.Printf("  %s  %s\n", e.Time.Format(time.TimeOnly), e.Msg)
		}
		seen = len(j.Events)
		if j.State == api.JobDone {
			fmt.Println()
			printJob(j, false)
			if j.Outcome != api.OutcomePass {
				os.Exit(3)
			}
			return nil
		}
	}
}

func printJob(j *api.Job, events bool) {
	fmt.Printf("job %s on %s: %s\n", j.ID, j.Spec.Device, strings.ToUpper(orDash(j.Outcome, string(j.State))))
	if j.Summary != "" {
		fmt.Printf("  %s\n", j.Summary)
	}
	if j.Maintainer != "" {
		fmt.Printf("  submitted by %s\n", j.Maintainer)
	}
	if x := j.GateExclusion; x != nil {
		fmt.Printf("  set aside in lab gate by %s at %s: %s\n", x.By, x.Time.Local().Format(time.DateTime), x.Reason)
	}
	r := j.Result
	if r.Booted {
		fmt.Printf("  booted %s in %.0fs\n", r.BootKernel, r.BootSeconds)
		if r.BootCmdline != "" {
			fmt.Printf("  cmdline: %s\n", r.BootCmdline)
		}
	}
	for _, t := range r.Tests {
		fmt.Printf("  test %-16s %s\n", t.Name, verdict(t))
	}
	for _, s := range r.Recovery {
		fmt.Printf("  recovery: %s\n", s)
	}
	if len(r.NewErrorLines) > 0 {
		fmt.Printf("  new kernel warnings/errors vs baseline:\n")
		for i, l := range r.NewErrorLines {
			if i == 15 {
				fmt.Printf("    ... %d more\n", len(r.NewErrorLines)-15)
				break
			}
			fmt.Printf("    %s\n", l)
		}
	}
	if o := r.OMT; o != nil {
		fmt.Printf("  omarchy-m-test %s on %s: %d pass, %d fail, %d skipped\n", o.Tool, o.Kernel, o.Pass, o.Fail, o.Skip)
		fmt.Printf("    compared with %s\n", o.ComparedTo)
		for _, c := range o.Regressions {
			fmt.Printf("    REGRESSED %s: %s\n", c.ID, trunc(c.Evidence, 100))
		}
		for _, c := range o.Fixed {
			fmt.Printf("    fixed     %s\n", c.ID)
		}
		for _, c := range o.Allowed {
			fmt.Printf("    allowed   %s (--omt-allow)\n", c.ID)
		}
		for _, c := range o.LabBoot {
			fmt.Printf("    lab boot  %s (fails for any kernel that isn't an installed package)\n", c.ID)
		}
		switch {
		case o.Published != "":
			fmt.Printf("    published: %s\n", o.Published)
		case o.PublishNote != "":
			fmt.Printf("    not published: %s\n", o.PublishNote)
		}
	}
	for _, e := range r.KernelEvents {
		if e.Kind != "warning" {
			fmt.Printf("  kernel %s (%s): %s\n", e.Kind, e.Source, trunc(e.Line, 120))
		}
	}
	if len(r.Logs) > 0 {
		fmt.Printf("  logs: lab logs %s <file>  (%s)\n", j.ID, strings.Join(r.Logs, ", "))
	}
	if events {
		fmt.Println("  events:")
		for _, e := range j.Events {
			fmt.Printf("    %s  %s\n", e.Time.Format(time.TimeOnly), e.Msg)
		}
	}
}

func printGate(r *api.GateReport) {
	k := r.Kernel
	fmt.Printf("kernel %s", k.SHA256[:12])
	if k.Release != "" || k.Build != "" {
		fmt.Printf(" (%s)", strings.Join(strings.Fields(k.Release+" "+k.Build), ", "))
	}
	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "FAMILY\tCHIP\tRESULT\tDEVICE\tJOB\tOUTCOME")
	var unstaged []string
	for _, f := range r.Families {
		res := f.Result
		if res == api.GateStageFailed {
			res = "couldn't stage"
		}
		if len(f.Runs) == 0 {
			fmt.Fprintf(w, "%s\t%s\t%s\t\t\t\n", f.Family, f.Name, res)
		}
		for i, run := range f.Runs {
			if run.Result == api.GateStageFailed {
				unstaged = append(unstaged, fmt.Sprintf("couldn't stage on %s (%s): %s; run it again once that's fixed", run.Device, run.Job, run.Reason))
			}
			fam, name, res := f.Family, f.Name, res
			if i > 0 {
				fam, name, res = "", "", ""
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", fam, name, res, run.Device, gateJob(run), run.Outcome)
		}
	}
	w.Flush()
	for _, l := range unstaged {
		fmt.Println(l)
	}
	// Runs that decide nothing, the excluded ones and the volunteers' Macs' apart.
	var other, excluded, vol []string
	add := func(fam string, run api.GateRun) {
		l := fmt.Sprintf("  %s\t%s\t%s\t%s\t%s", fam, run.Device, gateJob(run), run.Outcome, run.NotCounted)
		switch {
		case run.Excluded:
			excluded = append(excluded, l)
		case run.Volunteer:
			vol = append(vol, l)
		default:
			other = append(other, l)
		}
	}
	for _, f := range r.Families {
		for _, run := range f.NotCounted {
			add(f.Family, run)
		}
	}
	for _, run := range r.NotCounted {
		add("?", run) // no family known
	}
	section := func(title string, lines []string) {
		if len(lines) == 0 {
			return
		}
		fmt.Println(title)
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		for _, l := range lines {
			fmt.Fprintln(w, l)
		}
		w.Flush()
	}
	section("not counted:", other)
	if len(r.Excluded) > 0 {
		fmt.Printf("excluded for this report (--exclude): %s\n", strings.Join(r.Excluded, ", "))
	}
	if len(r.Exclusions) > 0 {
		fmt.Println("set aside with lab exclude (`lab exclude <job> --clear` counts one again):")
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		for _, x := range r.Exclusions {
			fmt.Fprintf(w, "  %s\t%s\t%s\tby %s\t%s\t%s\n", x.Job, x.Device, x.Outcome, x.By, x.Time.Local().Format(time.DateTime), x.Reason)
		}
		w.Flush()
	}
	section("excluded runs, not counted:", excluded)
	switch {
	case len(r.Volunteers) == 0:
	case r.VolunteersCounted:
		fmt.Printf("volunteers' Macs, counted: %s\n", strings.Join(r.Volunteers, ", "))
	default:
		fmt.Printf("volunteers' Macs, not counted (--volunteers counts them): %s\n", strings.Join(r.Volunteers, ", "))
	}
	section("volunteers' runs, not counted:", vol)
	if len(r.Unclassified) > 0 {
		fmt.Printf("not counted (no supported chip family known): %s\n", strings.Join(r.Unclassified, ", "))
	}
	fmt.Println(r.Summary)
}

// gateJob is a gate run's job, and the job it retries if it is a retry.
func gateJob(run api.GateRun) string {
	if run.RetryOf != "" {
		return run.Job + " (retry of " + run.RetryOf + ")"
	}
	return run.Job
}

func verdict(t api.TestResult) string {
	switch {
	case t.Passed:
		return fmt.Sprintf("pass  (%.0fs)", t.Seconds)
	case t.Skipped:
		return "SKIPPED " + t.Error
	case t.TimedOut:
		return "TIMED OUT"
	case t.Error != "":
		return "ERROR " + t.Error
	}
	return fmt.Sprintf("FAIL  (exit %d)", t.ExitCode)
}

func showDevices(ctx context.Context, c *client.Client) error {
	devs, err := c.Devices(ctx)
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		fmt.Println("no devices yet: lab enroll <name>")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "DEVICE\tSTATE\tTIER\tKERNEL\tLAST SEEN\tJOB\tLEASE")
	for _, d := range devs {
		lease := ""
		if d.Lease != nil {
			lease = d.Lease.Who()
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n", d.Name, d.State, d.Tier, d.Kernel, ago(d.LastSeen), d.ActiveJob, lease)
	}
	w.Flush()
	for _, d := range devs {
		// jobs are refused until these are fixed, so say so before lab run does
		for _, p := range d.Facts.Problems {
			if api.StaleKnownGood(p) {
				p += ": " + api.StaleKnownGoodFix(d.Name, d.Kernel)
			}
			fmt.Printf("%s fails preflight: %s\n", d.Name, p)
		}
	}
	for _, d := range devs {
		if d.Action != nil {
			fmt.Printf("\n>>> %s\n%s\n", strings.ToUpper(d.Action.Title), indent(d.Action.Text()))
		}
	}
	return nil
}

func showDevice(ctx context.Context, c *client.Client, name string) error {
	d, err := c.Device(ctx, name)
	if err != nil {
		return err
	}
	fmt.Printf("%s  %s", d.Name, strings.ToUpper(string(d.State)))
	if d.StateReason != "" {
		fmt.Printf(" (%s)", d.StateReason)
	}
	fmt.Println()
	if d.Action != nil {
		fmt.Printf("\n>>> %s\n%s\n\n", strings.ToUpper(d.Action.Title), indent(d.Action.Text()))
	}
	fmt.Printf("  model:       %s, %s\n", d.Facts.DTModel, orDash(d.Label, "no label"))
	if d.IsVolunteer() {
		fmt.Printf("  kind:        Volunteer's Mac: %s, %s\n", d.VolunteerName, d.Contact)
		if by := cmp.Or(d.VolunteeredBy, d.LinkedLogin); by != "" {
			fmt.Printf("  seen by:     %s on the tailnet\n", by)
		}
	} else {
		fmt.Printf("  kind:        Lab-owned Mac\n")
	}
	if !d.SnapshotEligible {
		fmt.Printf("  installed:   not eligible for Installed runs: @ can't be snapshotted\n")
	}
	if d.Volunteer {
		fmt.Printf("  gate:        a Volunteer's Mac: lab gate counts it only with --volunteers\n")
	}
	if d.Configuration != nil {
		fmt.Printf("  config:      %s\n", d.Configuration)
	}
	fmt.Printf("  kernel:      %s (known-good: %s)\n", d.Kernel, orDash(d.KnownGood, "no baseline yet"))
	fmt.Printf("  last seen:   %s, uptime %.0fs, systemd %s\n", ago(d.LastSeen), d.Health.Uptime, d.Health.SystemState)
	tiers := []string{"panic reboot + one-shot fallback", "+ hardware watchdog", "+ out-of-band reset"}
	fmt.Printf("  recovery:    tier %d (%s)\n", d.Tier, tiers[d.Tier])
	if d.OOB != nil {
		fmt.Printf("  oob:         %s\n", d.OOB.URL)
	}
	if d.Lease != nil {
		fmt.Printf("  lease:       %s until %s\n", d.Lease.Who(), d.Lease.Expires.Format(time.TimeOnly))
	}
	if d.ActiveJob != "" {
		fmt.Printf("  job:         %s (%d queued)\n", d.ActiveJob, d.Queued)
	}
	if len(d.Facts.Problems) > 0 {
		fmt.Printf("  preflight problems:\n")
		for _, p := range d.Facts.Problems {
			fmt.Printf("    - %s\n", p)
			if api.StaleKnownGood(p) {
				fmt.Printf("      %s\n", api.StaleKnownGoodFix(d.Name, d.Kernel))
			}
		}
	}
	if len(d.Facts.Warnings) > 0 {
		fmt.Printf("  preflight warnings:\n")
		for _, w := range d.Facts.Warnings {
			fmt.Printf("    - %s\n", w)
		}
	}
	if len(d.Events) > 0 {
		fmt.Printf("  kernel events (24h):\n")
		for _, e := range d.Events[max(0, len(d.Events)-8):] {
			fmt.Printf("    %s %-11s %s\n", e.Time.Format(time.TimeOnly), e.Kind, trunc(e.Line, 100))
		}
	}
	return nil
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return time.Since(t).Round(time.Second).String() + " ago"
}

func indent(s string) string { return "    " + strings.ReplaceAll(s, "\n", "\n    ") }

func orDash(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// execUsage is lab exec's help.
const execUsage = `lab exec <device> [--timeout 120] <command...>
  Runs the command on the Mac as root, through lab-agent. It needs your own Lease on the Mac
  (lab lease <device>), a Maintainer's too. The command's text, its exit code and how long it ran
  are kept in the Exec record for 12 months, and the text shows on the Mac while it runs:
  never put a secret in a command.`

// retired is what a command the lab no longer has says, nil for any other.
func retired(cmd string) error {
	switch cmd {
	case "shell", "console", "serial", "recovery-mode":
		return errors.New(api.NoSerialConsole)
	case "forget":
		return errors.New("`lab forget` is not part of Omarchy Mac Lab.")
	case "maintainer":
		return errors.New("`lab maintainer` is not part of Omarchy Mac Lab: Maintainer is a mark on People.")
	}
	return nil
}

// showVersion prints lab's release, then the one the lab of the saved login
// runs. With no login it says so and asks nobody: an install and a release
// check run it before anyone has logged in.
func showVersion(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "lab", version)
	c, err := client.Load()
	if errors.Is(err, client.ErrNotLoggedIn) {
		fmt.Fprintln(out, err)
		return nil
	}
	if err != nil {
		return err
	}
	who, err := c.WhoAmI(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, "Omarchy Mac Lab", orDash(who.LabRelease, "did not say which release it runs"))
	return nil
}

// login keeps the one login. The Lab token is asked for, without echo at a
// terminal, or read as the first line of anything else given as input: an
// argument would stay in the shell's history and show in the process list.
func login(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	caFile := fs.String("ca-file", "", "PEM file of CA certificates for the lab's https certificate, when the system doesn't trust it (a private CA, or labd's own certificate)")
	plain := fs.Bool("allow-plain-http", false, "let an http:// URL reach a lab on another machine: the token then crosses the network unencrypted")
	pos := parse(fs, args)
	const use = "usage: lab login <url> [--ca-file <pem>] [--allow-plain-http]"
	switch {
	case len(pos) == 2:
		return errors.New("the Lab token is never an argument: run `lab login <url>` and give it at the prompt. One typed on a command line stays in the shell's history: revoke it on your own page and make another")
	case len(pos) != 1:
		return errors.New(use)
	}
	// Refused before the token is asked for.
	if err := labhttp.CheckServer(pos[0], *plain); err != nil {
		return err
	}
	if *caFile != "" {
		if _, err := labhttp.ReadCAFile(*caFile); err != nil {
			return err
		}
	}
	token, err := askToken(in)
	if err != nil {
		return err
	}
	who, err := client.Login(context.Background(), pos[0], token, *caFile, *plain)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Logged in to Omarchy Mac Lab as @%s.\n", who.Handle)
	if api.ReleaseOlder(version, who.LabRelease) {
		fmt.Fprintf(out, "Omarchy Mac Lab runs a newer release, %s. Update lab: curl -fsSL %s/install/lab | bash\n", who.LabRelease, who.Address)
	}
	return nil
}

// askToken reads the Lab token from in.
func askToken(in io.Reader) (string, error) {
	var line string
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(os.Stderr, "Lab token: ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		line = string(b)
	} else {
		var err error
		if line, err = bufio.NewReader(io.LimitReader(in, 4096)).ReadString('\n'); err != nil && err != io.EOF {
			return "", err
		}
	}
	if line = strings.TrimSpace(line); line == "" {
		return "", errors.New("no Lab token given: make one on your own page of the lab, then give it at the prompt")
	}
	return line, nil
}

// pack builds a kernel artifact in the Arch package layout from an O= build tree.
func pack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	build := fs.String("build", "", "kernel build (O=) directory")
	out := fs.String("o", "", "output file (default kernel-<release>.tar.zst)")
	parse(fs, args)
	if *build == "" {
		return fmt.Errorf("usage: lab pack --build <O= dir> [-o file]")
	}
	b, err := os.ReadFile(filepath.Join(*build, "include/config/kernel.release"))
	if err != nil {
		return fmt.Errorf("not a built kernel tree: %w", err)
	}
	krel := strings.TrimSpace(string(b))
	image := filepath.Join(*build, "arch/arm64/boot/Image")
	if _, err := os.Stat(image); err != nil {
		return fmt.Errorf("no %s: build the kernel first", image)
	}
	stage, err := os.MkdirTemp("", "lab-pack-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	fmt.Fprintf(os.Stderr, "installing modules for %s...\n", krel)
	mk := exec.Command("make", "-C", *build, "-s", "INSTALL_MOD_PATH="+stage, "INSTALL_MOD_STRIP=1", "modules_install")
	mk.Stdout, mk.Stderr = os.Stderr, os.Stderr
	if err := mk.Run(); err != nil {
		return fmt.Errorf("modules_install: %w", err)
	}
	mods := filepath.Join(stage, "usr/lib/modules", krel)
	if _, err := os.Stat(mods); err != nil {
		os.MkdirAll(filepath.Join(stage, "usr/lib/modules"), 0o755)
		if err := os.Rename(filepath.Join(stage, "lib/modules", krel), mods); err != nil {
			return err
		}
	}
	os.Remove(filepath.Join(mods, "build"))
	os.Remove(filepath.Join(mods, "source"))
	if err := exec.Command("cp", image, filepath.Join(mods, "vmlinuz")).Run(); err != nil {
		return err
	}
	if *out == "" {
		*out = "kernel-" + krel + ".tar.zst"
	}
	abs, _ := filepath.Abs(*out)
	tarArgs := []string{"-C", stage, "-cf", abs, "usr"}
	if strings.HasSuffix(abs, ".zst") {
		tarArgs = append([]string{"--zstd"}, tarArgs...)
	} else if strings.HasSuffix(abs, ".gz") {
		tarArgs = append([]string{"-z"}, tarArgs...)
	}
	if o, err := exec.Command("tar", tarArgs...).CombinedOutput(); err != nil {
		return fmt.Errorf("tar: %v: %s", err, o)
	}
	fmt.Printf("%s (kernel release %s)\nnext: lab run <device> --kernel %s\n", *out, krel, *out)
	return nil
}

func isSource(k string) bool {
	return strings.HasPrefix(k, "https://") || strings.HasPrefix(k, "http://") || strings.HasPrefix(k, "github.com/") || strings.HasPrefix(k, "git@")
}

func shortSrc(s api.Source) string {
	r := strings.TrimPrefix(s.Repo, "https://github.com/")
	if s.Ref != "" && s.Ref != s.SHA {
		r += "@" + s.Ref
	}
	return r
}

func buildState(b *api.Build) string {
	if b.State == api.BuildRunning {
		st := b.Stage
		if b.Progress > 0 && b.Stage == "build" {
			st = fmt.Sprintf("build %d%%", int(b.Progress*100))
		}
		return "running: " + st
	}
	return string(b.State)
}

func fmtSecs(s float64) string {
	if s <= 0 {
		return ""
	}
	return (time.Duration(s) * time.Second).String()
}

func printBuild(b *api.Build) {
	fmt.Printf("build %s %s", b.ID, strings.ToUpper(string(b.State)))
	if b.Release != "" {
		fmt.Printf(": %s, %d MB, %s", b.Release, b.Size>>20, fmtSecs(b.Seconds))
	}
	if b.Error != "" {
		fmt.Printf(": %s", b.Error)
	}
	fmt.Println()
	for _, f := range b.Files {
		fmt.Printf("  %s  %s  (%d MB)\n", f.SHA256, f.Name, f.Size>>20)
	}
	if b.State == api.BuildDone && b.Artifact != "" {
		fmt.Printf("  boot it once: lab run <mac> --kernel %s\n", b.ID)
	}
	if len(b.Files) > 0 {
		fmt.Printf("  download: lab build-get %s -o <dir>\n", b.ID)
	}
}

// splitAllow splits --omt-allow on commas. Check ids always have a dot and a
// device-tree compatible's own comma doesn't start one, so a piece without a
// dot belongs to the one before it: hardware.drivers:apple,mesa-fingerprint.
func splitAllow(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		switch {
		case p == "":
		case !strings.Contains(p, ".") && len(out) > 0 && strings.Contains(out[len(out)-1], ":"):
			out[len(out)-1] += "," + p
		default:
			out = append(out, p)
		}
	}
	return out
}

var reBuildID = regexp.MustCompile(`^b[0-9]{4}-[0-9]{6}-[0-9a-f]{4}$`)

// followBuild streams a build's log until it ends, prints the result, and
// downloads its files into out if asked. It exits 3 if the build failed.
func followBuild(ctx context.Context, c *client.Client, b *api.Build, noWait bool, out string) error {
	if noWait {
		printBuild(b)
		return nil
	}
	if b.State != api.BuildDone {
		if err := c.BuildLog(ctx, b.ID, true, os.Stdout); err != nil {
			return err
		}
		var err error
		if b, err = c.GetBuild(ctx, b.ID); err != nil {
			return err
		}
	}
	printBuild(b)
	if b.State != api.BuildDone {
		os.Exit(3)
	}
	if out != "" {
		return getBuildFiles(ctx, c, b, out)
	}
	return nil
}

// getBuildFiles downloads named build outputs, each checked against the
// sha256 the build recorded.
func getBuildFiles(ctx context.Context, c *client.Client, b *api.Build, dir string) error {
	if len(b.Files) == 0 {
		return fmt.Errorf("build %s has no named files to download", b.ID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range b.Files {
		dst := filepath.Join(dir, f.Name)
		if err := c.BuildFile(ctx, b.ID, f.Name, f.SHA256, dst); err != nil {
			return err
		}
		fmt.Printf("%s  %s\n", f.SHA256, dst)
	}
	if b.Recipe != "" {
		if err := getRecipe(ctx, c, b, dir); err != nil {
			fmt.Fprintf(os.Stderr, "lab: the packages are complete, but not the recipe: %v\n", err)
		}
	}
	return nil
}

// getRecipe unpacks the recipe a package build was made from, exactly as
// uploaded (PKGBUILD, patches, data files), into dir/recipe/.
func getRecipe(ctx context.Context, c *client.Client, b *api.Build, dir string) error {
	tarball := filepath.Join(dir, api.RecipeFile)
	if err := c.BuildFile(ctx, b.ID, api.RecipeFile, b.Recipe, tarball); err != nil {
		return err
	}
	defer os.Remove(tarball)
	rdir := filepath.Join(dir, "recipe")
	if err := os.MkdirAll(rdir, 0o755); err != nil {
		return err
	}
	if o, err := exec.Command("tar", "-C", rdir, "--no-same-owner", "-xf", tarball).CombinedOutput(); err != nil {
		return fmt.Errorf("unpacking: %v: %s", err, o)
	}
	fmt.Printf("%s  %s/ (the recipe as uploaded)\n", b.Recipe, rdir)
	return nil
}

// uploadRecipe tars a PKGBUILD directory, with only the files makepkg needs
// from it (PKGBUILD, local sources, install and changelog files), and uploads it.
func uploadRecipe(ctx context.Context, c *client.Client, dir string) (string, []string, error) {
	names, err := recipeFiles(dir)
	if err != nil {
		return "", nil, err
	}
	tmp, err := os.CreateTemp("", "lab-recipe-*.tar")
	if err != nil {
		return "", nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	tarArgs := append([]string{"-C", dir, "--owner=0", "--group=0", "-cf", tmp.Name(), "--"}, names...)
	if o, err := exec.Command("tar", tarArgs...).CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("tar: %v: %s", err, o)
	}
	sha, err := c.Upload(ctx, tmp.Name())
	return sha, names, err
}

// recipeFiles lists the recipe's own files by sourcing the PKGBUILD, as makepkg does.
func recipeFiles(dir string) ([]string, error) {
	if _, err := os.Stat(filepath.Join(dir, "PKGBUILD")); err != nil {
		return nil, fmt.Errorf("%s has no PKGBUILD", dir)
	}
	sh := `cd "$1" && source ./PKGBUILD >/dev/null 2>&1; printf '%s\n' "${source[@]}" "${source_aarch64[@]}" "$install" "$changelog"`
	o, err := exec.Command("bash", "-c", sh, "bash", dir).Output()
	if err != nil {
		return nil, fmt.Errorf("reading %s/PKGBUILD: %w", dir, err)
	}
	names := []string{"PKGBUILD"}
	seen := map[string]bool{"PKGBUILD": true}
	for _, l := range strings.Split(string(o), "\n") {
		name, loc := "", strings.TrimSpace(l)
		if i := strings.Index(loc, "::"); i >= 0 {
			name, loc = loc[:i], loc[i+2:]
		}
		if loc == "" || strings.Contains(loc, "://") {
			continue // downloaded by makepkg
		}
		if name == "" {
			name = filepath.Base(loc)
		}
		if seen[name] {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, loc)); err != nil {
			return nil, fmt.Errorf("the PKGBUILD lists %s, which is not in %s", loc, dir)
		}
		seen[name] = true
		names = append(names, loc)
	}
	return names, nil
}

// manifestSHA accepts a manifest file or its sha256.
func manifestSHA(arg string) (string, error) {
	if b, err := os.ReadFile(arg); err == nil {
		sum := sha256.Sum256(b)
		return hex.EncodeToString(sum[:]), nil
	}
	if len(arg) == 64 && strings.Trim(arg, "0123456789abcdef") == "" {
		return arg, nil
	}
	return "", fmt.Errorf("--installed takes the frozen manifest file or its full sha256, not %q", arg)
}

// installedCmd handles `lab installed ...`: listing Releases, freezing a
// Release's expectation, recording its verdict and promoting an accepted
// installed run.
func installedCmd(ctx context.Context, c *client.Client, args []string) error {
	const usage = "lab installed list|freeze-manifest|freeze-entry|verdict|promote|status ..."
	if len(args) < 1 {
		return errors.New(usage)
	}
	sub := args[0]
	fs := flag.NewFlagSet("installed "+sub, flag.ContinueOnError)
	release := fs.String("release", "", "release name, e.g. 12.0")
	approved := fs.String("approved-by", "", "who approved the manifest, comma-separated")
	job := fs.String("job", "", "the installed job to promote")
	note := fs.String("note", "", "why: a verdict needs a note every time")
	revoke := fs.Bool("revoke", false, "revoke the verdict (Maintainers)")
	yes := fs.Bool("yes", false, "go on without being asked")
	forMac := fs.String("for", "", "freeze-manifest, as an M team member: one of the Release's Macs, which you lease")
	pos := parse(fs, args[1:])
	if sub == "list" {
		all, err := c.Releases(ctx)
		if err != nil {
			return err
		}
		printReleases(os.Stdout, all)
		return nil
	}
	if *release == "" {
		return errors.New("--release is required")
	}
	var out map[string]any
	var err error
	switch sub {
	case "freeze-manifest":
		if err := need(pos, 2, "lab installed freeze-manifest --release R <manifest.json> <payload.sha256>"); err != nil {
			return err
		}
		c.UploadFor = *forMac
		m, err1 := c.Upload(ctx, pos[0])
		p, err2 := c.Upload(ctx, pos[1])
		if err1 != nil || err2 != nil {
			return errors.Join(err1, err2)
		}
		var by []string
		if *approved != "" {
			by = strings.Split(*approved, ",")
		}
		err = c.Installed(ctx, "POST", *release, "manifest", map[string]any{"manifest": m, "payload": p, "approved_by": by}, &out)
	case "freeze-entry":
		if err := need(pos, 1, "lab installed freeze-entry <device> --release R"); err != nil {
			return err
		}
		err = c.Installed(ctx, "POST", *release, "entry/"+pos[0], nil, &out)
	case "verdict":
		req := api.VerdictRequest{Note: *note}
		switch {
		case *revoke && len(pos) == 0:
			req.State = api.ReleaseRevoked
		case !*revoke && len(pos) == 1 && pos[0] == "qualified":
			req.State = api.ReleaseQualified
		case !*revoke && len(pos) == 1 && pos[0] == "not-qualified":
			req.State = api.ReleaseNotQualified
		default:
			return errors.New(`usage: lab installed verdict --release R qualified|not-qualified --note "…", or --revoke --note "…"`)
		}
		var asked api.ReleaseAnswer
		if err := c.Installed(ctx, "POST", *release, "verdict", req, &asked); err != nil {
			return err
		}
		if err := confirmed(asked.Confirm, *yes, os.Stdin); err != nil {
			return err
		}
		req.Confirm = true
		var done api.ReleaseAnswer
		if err := c.Installed(ctx, "POST", *release, "verdict", req, &done); err != nil {
			return err
		}
		if done.Release != nil {
			printReleases(os.Stdout, []api.Release{*done.Release})
		}
		return nil
	case "promote":
		if err := need(pos, 1, "lab installed promote <device> --release R --job <id>"); err != nil {
			return err
		}
		ask := map[string]any{"device": pos[0], "job": *job}
		var asked struct {
			Confirm string `json:"confirm"`
		}
		if err := c.Installed(ctx, "POST", *release, "promote", ask, &asked); err != nil {
			return err
		}
		if err := confirmed(asked.Confirm, *yes, os.Stdin); err != nil {
			return err
		}
		ask["confirm"] = true
		err = c.Installed(ctx, "POST", *release, "promote", ask, &out)
	case "status":
		err = c.Installed(ctx, "GET", *release, "", nil, &out)
	default:
		return fmt.Errorf("unknown: lab installed %s; %s", sub, usage)
	}
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

// confirmed shows the sentence the lab wants confirmed, warnings and all,
// and asks once at a terminal. With yes it only shows it. With neither a
// terminal nor yes nothing goes on.
func confirmed(sentence string, yes bool, in io.Reader) error {
	fmt.Fprintln(os.Stderr, sentence)
	if yes {
		return nil
	}
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return errors.New("nothing was done: run it again with --yes to confirm")
	}
	fmt.Fprint(os.Stderr, "Type yes to go on: ")
	line, _ := bufio.NewReader(io.LimitReader(f, 64)).ReadString('\n')
	if strings.TrimSpace(line) != "yes" {
		return errors.New("nothing was done")
	}
	return nil
}

// releaseState says a Release's state with its date.
func releaseState(r api.Release) string {
	words := map[string]string{api.ReleaseQualified: "qualified", api.ReleaseNotQualified: "not qualified", api.ReleaseRevoked: "revoked"}[r.State]
	if words == "" || r.StateAt == nil {
		return "no verdict"
	}
	return words + " " + r.StateAt.Local().Format("2006-01-02")
}

// printReleases prints each Release with its verdict, as `lab installed
// list` shows them.
func printReleases(w io.Writer, all []api.Release) {
	if len(all) == 0 {
		fmt.Fprintln(w, "no Release yet: one is listed once its manifest is frozen (lab installed freeze-manifest)")
		return
	}
	for _, r := range all {
		by := "@" + r.FrozenBy
		if r.FrozenBy == "" {
			by = "before the lab kept who"
		}
		fmt.Fprintf(w, "%s  %s\n  frozen %s by %s; kernel %s\n  %d of %d Configurations have a passed run; %d promoted\n",
			r.Name, releaseState(r), r.FrozenAt.Local().Format("2006-01-02 15:04"), by, r.Kernel, r.Passed, r.Configurations, r.Promoted)
		for _, m := range r.Macs {
			line := fmt.Sprintf("  %s (%s): %d Installed runs", m.Mac, m.Configuration, m.Runs)
			if m.PassedRun != "" {
				line += ", passed " + m.PassedRun
			} else if m.LatestRun != "" {
				line += ", latest " + m.LatestRun + " " + m.LatestOutcome
			}
			if m.Promoted != nil {
				line += "; promoted by @" + m.Promoted.Handle
				if m.Promoted.Mark != "" {
					line += " (" + m.Promoted.Mark + ")"
				}
			}
			fmt.Fprintln(w, line)
		}
		for _, warn := range r.Warnings {
			fmt.Fprintln(w, "  warning: "+warn)
		}
	}
}
