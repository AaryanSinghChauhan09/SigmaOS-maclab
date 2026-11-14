// Command labd is the Mac test lab controller.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/omacom/omarchy-maclab/internal/oob"
	"github.com/omacom/omarchy-maclab/internal/reports"
	"github.com/omacom/omarchy-maclab/internal/server"
)

func main() {
	home, _ := os.UserHomeDir()
	listen := flag.String("listen", envOr("MACLAB_LISTEN", "127.0.0.1:7770"), "the loopback port, for callers on this machine (the Builder, the Out-of-band controller's client side, break-glass): it believes nothing a caller forwards, and labd listens on no other network address")
	socket := flag.String("socket", "", "the tunnel's Unix socket, which cloudflared dials: made for the account labd runs as alone, and the only place the edge's CF-Connecting-IP and forwarded headers are believed (needs --public-url)")
	publicURL := flag.String("public-url", "", "the Lab address people and Macs reach labd at, e.g. https://lab.omarchy-mac.org: every link labd builds names it, and through the tunnel labd answers to no other name but --alias")
	alias := flag.String("alias", "", "the Alias, e.g. maclab.omarchy.org: the one other name the tunnel carries. A page request on it gets a 308 to the same path at the Lab address; anything under /api/ is refused")
	preview := flag.Bool("preview", false, "the lab is in Preview: answering at the Lab address, closed, before it opens. Every answer tells crawlers not to index it")
	signIn := flag.String("sign-in", "", "the folder with the lab's sign-in configuration: github-client-id and github-client-secret (the OAuth app; the secret readable by labd's account alone) and admins (the Admins' GitHub account ids, one a line). Without the app's two files sign-in is off")
	machineIDKey := flag.String("machine-id-key", "", "the file with the machine-id key, which tells one Mac's reports from another's: 64 bytes as 128 hex digits, readable by labd's account alone. Without the file labd takes no reports")
	githubURL := flag.String("github-url", "", "a stand-in for GitHub on this machine, for a lab under development, e.g. http://127.0.0.1:7791 (tools/fake-github): a loopback address only, and never beside --socket")
	termsFile := flag.String("terms-file", "", "participation terms a Mac's owner accepts before it joins (default: labd's built-in terms)")
	data := flag.String("data", filepath.Join(home, ".local/share/maclab"), "data directory")
	ntfy := flag.String("ntfy", os.Getenv("MACLAB_NTFY"), "ntfy topic URL for notifications, e.g. https://ntfy.sh/<secret-topic>")
	notifyCmd := flag.String("notify-cmd", os.Getenv("MACLAB_NOTIFY_CMD"), `shell command run per notification with $MACLAB_TITLE/$MACLAB_BODY, e.g. 'notify-send "$MACLAB_TITLE" "$MACLAB_BODY"'`)
	bootTimeout := flag.Duration("boot-timeout", 6*time.Minute, "default time a Mac gets to come back after a reboot")
	offlineAfter := flag.Duration("offline-after", 3*time.Minute, "how long an idle Mac may be silent before labd marks it offline")
	aliveWindow := flag.Duration("alive-window", time.Minute, "how stale a heartbeat may be before a Mac counts unresponsive")
	quietGrace := flag.Duration("quiet-grace", 3*time.Minute, "how long a Mac may go silent on the same boot before a command on it counts lost")
	stageTimeout := flag.Duration("stage-timeout", 45*time.Minute, "time a Mac gets for each attempt to stage a kernel, download included")
	watchdogOpenTimeout := flag.Duration("watchdog-open-timeout", 0, "watchdog.open_timeout on every Lab boot: how long the kernel feeds the hardware watchdog before systemd must open it; an early hang resets about 30s after it (0 is half the job's boot timeout, at least 120s)")
	artifactGB := flag.Int64("artifact-budget-gb", 20, "cap on the kernel artifact store; unreferenced artifacts go first")
	partsAtOnce := flag.Int("parts-at-once", 1, "how many parts of an upload labd tells a client to send at the same time: 1 or 4")
	maintainer := flag.String("maintainer", os.Getenv("USER"), "the first Maintainer's name, used once: on a fresh labd, or on the first start after upgrading from the single admin token")
	omt := flag.Bool("omt", true, "run omarchy-m-test (github.com/maralcbr/omarchy-m-testing) in every job on Macs with a desktop user, and compare it with the known-good kernel")
	omtSite := flag.String("omt-site", "https://omarchy-m-testing.org", "where omarchy-m-test reports are published (lab publish, --publish); empty never publishes")
	omtPublish := flag.Bool("omt-publish", false, "publish known-good omarchy-m-test runs (baselines and --omt-every runs) by themselves")
	omtEvery := flag.Duration("omt-every", 0, "re-run omarchy-m-test on each idle Mac's known-good kernel this often, e.g. 24h (reboots the Mac); 0 never")
	oobd := flag.String("oobd", "http://127.0.0.1:7780", "the Out-of-band controller's address, on this machine")
	oobdTokenFile := flag.String("oobd-token-file", "", "labd's copy of the Out-of-band controller's token: a file only the account labd runs as reads. With none, no Mac gets a Hard reset")
	rebootWarning := flag.Duration("reboot-warning", 5*time.Minute, "how long before a Volunteer job's first reboot its Mac shows the Volunteer a notice, which the job then waits for; 0 shows none and waits for none")
	flag.Parse()
	if *partsAtOnce != 1 && *partsAtOnce != 4 {
		log.Fatalf("--parts-at-once is 1 or 4, not %d", *partsAtOnce)
	}
	var pub *url.URL
	if *publicURL != "" {
		u, err := server.ParsePublicURL(*publicURL)
		if err != nil {
			log.Fatal(err)
		}
		pub = u
	}
	if *alias != "" {
		a, err := server.ParseAlias(*alias)
		if err != nil {
			log.Fatal(err)
		}
		*alias = a
	}
	if *socket != "" && pub == nil {
		log.Fatal("--socket needs --public-url: through the tunnel labd answers to its Lab address")
	}
	if *githubURL != "" && *socket != "" {
		log.Fatal("--github-url is for a lab under development: a labd behind the tunnel (--socket) signs people in with GitHub itself")
	}
	app, admins, err := server.ReadSignIn(*signIn, *githubURL)
	if err != nil {
		log.Fatal(err)
	}
	idKey, err := reports.ReadMachineIDKey(*machineIDKey)
	if err != nil {
		log.Fatal(err)
	}
	var terms string
	if *termsFile != "" {
		b, err := os.ReadFile(*termsFile)
		if err != nil {
			log.Fatal(err)
		}
		terms = string(b)
	}
	controller, err := outOfBand(*oobd, *oobdTokenFile)
	if err != nil {
		log.Fatal(err)
	}
	lc := server.ListenConfig{Addr: *listen, Socket: *socket}
	if err := lc.Check(); err != nil {
		log.Fatal(err)
	}

	if err := os.MkdirAll(*data, 0o700); err != nil {
		log.Fatal(err)
	}
	// labd announces the first Maintainer when it makes one.
	tok, _, err := server.FirstMaintainerToken(*data)
	if err != nil {
		log.Fatal(err)
	}

	btokPath := filepath.Join(*data, "builder.token")
	btok, err := os.ReadFile(btokPath)
	if os.IsNotExist(err) {
		btok = []byte("bld_" + randHex())
		if err := os.WriteFile(btokPath, btok, 0o600); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("created builder token in %s (give it to lab-builder)\n", btokPath)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// ./volunteer downloads the lab-agent built alongside labd.
	exe, _ := os.Executable()
	s, err := server.New(ctx, server.Config{DataDir: *data, FirstMaintainer: *maintainer, FirstMaintainerToken: tok,
		PublicURL: pub, Alias: *alias, Preview: *preview, Terms: terms, GitHub: app, Admins: admins, OOB: controller, MachineIDKey: idKey,
		OMT: *omt, OMTSite: *omtSite, OMTPublish: *omtPublish, OMTEvery: *omtEvery,
		BootTimeout: *bootTimeout, OfflineAfter: *offlineAfter, AliveWindow: *aliveWindow, QuietGrace: *quietGrace, StageTimeout: *stageTimeout, WatchdogOpenTimeout: *watchdogOpenTimeout, RebootWarning: *rebootWarning, NtfyURL: *ntfy, NotifyCmd: *notifyCmd,
		BuilderToken: strings.TrimSpace(string(btok)), ArtifactBudget: *artifactGB << 30, Transfers: server.Transfers{AtOnce: *partsAtOnce},
		AgentBinary: filepath.Join(filepath.Dir(exe), "lab-agent-linux-arm64")}, nil)
	if err != nil {
		log.Fatal(err)
	}
	hs, err := lc.Server(s.Handler())
	if err != nil {
		log.Fatal(err)
	}
	lns, err := lc.Listen()
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	where := "http://" + lns[0].Addr().String()
	if *socket != "" {
		where += " and the tunnel's socket " + *socket
	}
	switch {
	case pub != nil && *preview:
		log.Printf("labd listening on %s, served as %s, in Preview, data in %s", where, pub, *data)
	case pub != nil:
		log.Printf("labd listening on %s, served as %s, data in %s", where, pub, *data)
	default:
		log.Printf("labd listening on %s, data in %s", where, *data)
	}
	switch {
	case app == nil:
		log.Printf("sign-in is off: no OAuth app (--sign-in <folder> with github-client-id and github-client-secret)")
	case *githubURL != "":
		log.Printf("sign-in goes to a stand-in for GitHub at %s, not to GitHub: this labd is under development; %d Admin(s)", *githubURL, len(admins))
	default:
		log.Printf("sign-in with GitHub is on; %d Admin(s) named in %s", len(admins), filepath.Join(*signIn, "admins"))
	}
	switch {
	case *preview:
		log.Printf("reports are refused: the lab is in Preview")
	case idKey == nil:
		log.Printf("reports are refused: no machine-id key (--machine-id-key <file>)")
	}
	if err := server.Serve(hs, lns...); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// outOfBand is labd's side of the Out-of-band controller. A token file that
// is not there yet is no error: the mini's set-up makes it, and labd reads it
// when it sends a request. One that others can read stops labd.
func outOfBand(addr, tokenFile string) (*oob.Client, error) {
	if tokenFile == "" {
		return nil, nil
	}
	c, err := oob.NewClient(addr, tokenFile)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(tokenFile); os.IsNotExist(err) {
		log.Printf("the Out-of-band controller's token is not at %s yet: no Hard reset until the mini's set-up makes it", tokenFile)
		return c, nil
	}
	if _, err := oob.ReadToken(tokenFile); err != nil {
		return nil, err
	}
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func randHex() string {
	b := make([]byte, 20)
	f, _ := os.Open("/dev/urandom")
	f.Read(b)
	f.Close()
	return fmt.Sprintf("%x", b)
}
