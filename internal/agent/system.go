package agent

import (
	"context"
	"io"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// Fetcher downloads an artifact by sha256 into dst and verifies it.
type Fetcher func(ctx context.Context, sha, dst string) error

// Quieter tells labd, before the Mac goes off the network, how long it may be
// off on this boot, so that labd doesn't count it as lost meanwhile; 0, once
// it is back, ends that spell.
type Quieter func(ctx context.Context, d time.Duration) error

// System is everything the agent does to the machine. Linux is the real
// implementation; Sim stands in for a Mac in tests.
type System interface {
	Identity() (bootID, kernel, cmdline string)
	Facts() api.Facts
	Health() api.Health
	// WatchKernel feeds every kernel log line of this boot to fn until ctx ends.
	WatchKernel(ctx context.Context, fn func(line string))
	// WatchSleep calls fn(true) just before the machine suspends and
	// fn(false) once it has resumed.
	WatchSleep(ctx context.Context, fn func(sleeping bool))

	Stage(ctx context.Context, job string, a api.StageArgs, fetch Fetcher) (api.StageResult, error)
	BootOnce(entry string) error
	// CancelBootOnce clears an armed one-shot, so the next reboot boots the
	// Mac's default entry.
	CancelBootOnce() error
	// QuiesceBootSync stops, until the next boot, what rewrites the boot menu
	// behind the lab (limine-snapper-sync), letting a rewrite under way end
	// first. resume starts it again, for when the reboot doesn't happen.
	QuiesceBootSync() (resume func())
	// Install installs a Release's packages as its installer does, fetching
	// them from labd by sha256.
	Install(ctx context.Context, job string, a api.InstallArgs, fetch Fetcher) error
	// RunTest runs a test; a test that takes the network down (suspend-devices)
	// tells labd first with quiet.
	RunTest(ctx context.Context, job string, t api.TestSpec, fetch Fetcher, quiet Quieter, outDir string) api.TestResult
	Collect(ctx context.Context, boot, outDir string) error
	// PreviousBoot is the ID of the boot before this one, if the journal saw
	// it start after since; "" if not.
	PreviousBoot(since time.Time) string
	// CollectPstore copies the pstore/ramoops records of crashes since since
	// into outDir and returns their names.
	CollectPstore(since time.Time, outDir string) ([]string, error)
	Cleanup(job string, all bool) error
	// Screenshot captures the GUI session as JPEG to path.
	Screenshot(ctx context.Context, path string, wait time.Duration) (w, h int, err error)
	Logs(ctx context.Context, a api.LogsArgs) (string, error)
	// KernelConfig is the running kernel's .config (/proc/config.gz).
	KernelConfig() (string, error)
	Exec(ctx context.Context, a api.ExecArgs) api.ExecResult
	// RecordKnownGood records the running kernel as known-good, as setup does,
	// if it is kernel and not a lab boot.
	RecordKnownGood(kernel string) error

	// Reboot and Crash run after the command's result has been reported.
	Reboot() error
	Crash(mode string) error

	// OpenWatchdog takes the hardware watchdog for the agent to feed on a
	// test boot. Nothing stops it again: once the agent exits, the machine
	// resets unless something else feeds it.
	OpenWatchdog() (io.Writer, error)
	// PingInit returns once the init system (PID 1) answers a question, or
	// an error if it doesn't before ctx ends. The agent feeds the watchdog
	// only while it answers.
	PingInit(ctx context.Context) error
	// InhibitSuspend keeps the machine from suspending, for the reason why,
	// until release; a boot ends it too. It never inhibits idle, so the idle
	// lock and screensaver carry on.
	InhibitSuspend(why string) (release func(), err error)

	// Alive is false while a simulated machine is hung. Always true on Linux.
	Alive() bool

	// LeaveLab removes the lab from the Mac: its boot entries, units, autologin
	// drop-in and device secret. keepAgent keeps lab-agent itself until labd
	// has been told the Mac left.
	LeaveLab(keepAgent bool) error

	// SnapshotEligible is nil when @ can be snapshotted, else why not.
	SnapshotEligible() error
}

// SetupMac is the Mac under `lab-agent setup`: what the Join command reads
// before it changes anything, and the changes joining makes. Linux is a real
// Mac; Sim stands in for one in tests and in `lab-agent sim`.
type SetupMac interface {
	// JoinFacts reads what the pre-checks judge. It changes nothing and
	// reaches no lab. token is the Join command's, to tell a setup that was
	// interrupted from a Mac already in the lab.
	JoinFacts(token string) JoinFacts
	// Facts are the hardware facts an enrollment carries.
	Facts() api.Facts
	// Home is where the Mac keeps what makes it a lab device: agent.json and
	// the work directory.
	Home() (configPath, workDir string)
	// LeaveOther leaves the lab cfg names, properly: that lab is told, then
	// its boot entries, units, settings files and device secret go.
	LeaveOther(ctx context.Context, cfg *Config) error
	// SetUp makes the Mac a lab device enrolled as cfg and starts lab-agent.
	SetUp(ctx context.Context, o SetupOptions, cfg *Config) error
}

// JoinFacts is a Mac as the Join command finds it.
type JoinFacts struct {
	OS         string   // runtime.GOOS
	Compatible []string // the device tree's compatible strings
	NotOmarchy string   // why the system is not Omarchy; "" when it is
	EUID       int
	SudoUID    string // sudo's SUDO_UID, who ran the Join command; "" without sudo
	// Enrolled is agent.json as it is; nil on a Mac in no lab. Resuming is
	// an earlier setup with this very token that never finished.
	Enrolled *Config
	Resuming bool
	Cmdline  string
	// Passphrase is why every start waits for a disk passphrase, Bootloader
	// why the bootloader cannot restart the Mac unattended, Missing the tools
	// the lab needs, Space why test kernels have no room; empty when fine.
	Passphrase string
	Bootloader []string
	Missing    []string
	Space      string
	Snapshot   error // why the system cannot be snapshotted; nil when it can
	OnBattery  bool
	// Installed is the number of the lab-agent installed on the Mac, ""
	// when there is none or it is this very program.
	Installed string
}
