package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/api"
)

const candidateKG = "root=UUID=abcd rootflags=subvol=@,compress=zstd rd.luks.name=1111=root rd.luks.key=1111=/etc/cryptsetup-keys.d/root.key quiet splash"

type fakeCandidateMac struct {
	t     *testing.T
	h     *candidateHost
	calls [][]string
	envs  map[string][]string
	fail  map[string]error
	krel  string // the kernel the set's linux package installs
}

func newCandidateMac(t *testing.T) *fakeCandidateMac {
	t.Helper()
	dir := t.TempDir()
	top := filepath.Join(dir, "top")
	at := filepath.Join(top, "@")
	for _, d := range []string{"etc", "usr/lib/modules/7.2.11-kg/", "var/log"} {
		os.MkdirAll(filepath.Join(at, d), 0o755)
	}
	os.WriteFile(filepath.Join(at, "usr/lib/modules/7.2.11-kg/vmlinuz"), []byte("kg"), 0o644)
	os.WriteFile(filepath.Join(at, "etc/fstab"), []byte(
		"UUID=abcd / btrfs rw,relatime,compress=zstd,subvol=/@ 0 0\n"+
			"UUID=abcd /home btrfs rw,subvol=/@home 0 0\n"+
			"UUID=abcd /var/log btrfs rw,subvol=/@log 0 0\n"), 0o644)
	esp := filepath.Join(dir, "esp")
	os.MkdirAll(filepath.Join(esp, "m1n1"), 0o755)
	os.WriteFile(filepath.Join(esp, "m1n1", "boot.bin"), []byte("old boot.bin"), 0o644)
	f := &fakeCandidateMac{t: t, envs: map[string][]string{}, fail: map[string]error{}, krel: "7.3.0-cand"}
	scratch := filepath.Join(dir, "scratch")
	os.MkdirAll(scratch, 0o700)
	f.h = &candidateHost{
		Top:        top,
		RootSource: "/dev/mapper/root[/@]",
		Lim:        testLimine(t),
		KnownGood:  &KnownGood{Kernel: "7.2.11-kg", Cmdline: candidateKG},
		GUIUser:    "volunteer",
		M1N1Dir:    filepath.Join(esp, "m1n1"),
		WorkDir:    filepath.Join(dir, "work"),
		DRMDir:     filepath.Join(dir, "drm"),
		Fetch: func(ctx context.Context, sha, dst string) error {
			return errors.New("no labd in this test")
		},
		UKI: ukiWriter{ScratchRoot: scratch,
			Lock: func() (func(), error) { return func() {}, nil },
			Space: func(p string) (ukiSpace, error) {
				dev := uint64(1)
				if strings.HasPrefix(p, scratch) {
					dev = 2
				}
				return ukiSpace{Available: 1 << 30, Allocation: 4096, Device: dev}, nil
			}},
		Run: f.run,
	}
	return f
}

func (f *fakeCandidateMac) run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	f.calls = append(f.calls, call)
	f.envs[name] = env
	if err := f.fail[name]; err != nil {
		return []byte("it broke"), err
	}
	switch name {
	case "btrfs":
		switch {
		case args[1] == "snapshot":
			src, dst := args[2], args[3]
			return nil, copyTree(src, dst)
		case args[1] == "delete":
			return nil, os.RemoveAll(args[2])
		}
	case "pacman":
		root := args[slices.Index(args, "--sysroot")+1]
		mods := filepath.Join(root, "usr/lib/modules", f.krel)
		os.MkdirAll(mods, 0o755)
		os.WriteFile(filepath.Join(mods, "vmlinuz"), []byte("new kernel"), 0o644)
		os.WriteFile(filepath.Join(mods, "pkgbase"), []byte("linux-asahi\n"), 0o644)
	case "mkinitcpio":
		out := args[slices.Index(args, "-U")+1]
		return nil, os.WriteFile(out, []byte("uki for "+strings.Join(args, " ")), 0o644)
	case "update-m1n1":
		return nil, os.WriteFile(filepath.Join(f.h.M1N1Dir, "boot.bin"), []byte("new boot.bin"), 0o644)
	case "journalctl":
		return []byte("candidate journal line\n"), nil
	}
	return nil, nil
}

func (f *fakeCandidateMac) ran(name string) []string {
	for _, c := range f.calls {
		if c[0] == name {
			return c
		}
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, info.Mode())
	})
}

func candidatePkg(t *testing.T, name, body string) api.CandidatePackage {
	t.Helper()
	p := filepath.Join(t.TempDir(), name+"-1-aarch64.pkg.tar.zst")
	os.WriteFile(p, []byte(body), 0o644)
	sum := sha256.Sum256([]byte(body))
	return api.CandidatePackage{Name: name, Version: "1", URL: p, SHA256: hex.EncodeToString(sum[:])}
}

func candidateArgs(t *testing.T, pkgs ...api.CandidatePackage) api.CandidateStageArgs {
	if len(pkgs) == 0 {
		pkgs = []api.CandidatePackage{candidatePkg(t, "linux-asahi", "kernel bytes"), candidatePkg(t, "mesa-aurora", "mesa bytes")}
	}
	return api.CandidateStageArgs{Set: api.CandidateSet{Name: "aurora-7.3", Packages: pkgs, GPUDriver: "aurora"}, BootTimeoutSec: 300}
}

func TestACandidateStageSnapshotsInstallsAndStagesAOneShotEntry(t *testing.T) {
	f := newCandidateMac(t)
	// An old candidate is replaced.
	os.MkdirAll(filepath.Join(f.h.Top, api.CandidateSubvolume, "stale"), 0o755)
	res, err := f.h.stage(context.Background(), "j1", candidateArgs(t))
	if err != nil {
		t.Fatal(err)
	}
	cand := filepath.Join(f.h.Top, api.CandidateSubvolume)
	want := [][]string{
		{"mount", "-o", "subvolid=5", "/dev/mapper/root", f.h.Top},
		{"btrfs", "subvolume", "delete", cand},
		{"btrfs", "subvolume", "snapshot", filepath.Join(f.h.Top, "@"), cand},
	}
	for i, w := range want {
		if !slices.Equal(f.calls[i], w) {
			t.Fatalf("call %d = %q, want %q", i, f.calls[i], w)
		}
	}
	pac := f.ran("pacman")
	if !slices.Equal(pac[:5], []string{"pacman", "--sysroot", cand, "-U", "--noconfirm"}) || len(pac) != 7 {
		t.Fatalf("pacman = %q", pac)
	}
	if _, err := os.Stat(filepath.Join(f.h.Top, "@", "usr/lib/modules", f.krel)); err == nil {
		t.Fatal("pacman changed @")
	}
	mk := f.ran("mkinitcpio")
	if mk == nil || mk[slices.Index(mk, "-r")+1] != cand || mk[slices.Index(mk, "-k")+1] != f.krel ||
		mk[slices.Index(mk, "--kernelimage")+1] != filepath.Join(cand, "usr/lib/modules", f.krel, "vmlinuz") {
		t.Fatalf("mkinitcpio = %q", mk)
	}
	if res.KernelRelease != f.krel || res.Entry != entryPrefix+"j1" || res.BootLoader {
		t.Fatalf("res = %+v", res)
	}
	fields := strings.Fields(res.Cmdline)
	for _, w := range []string{"rootflags=subvol=@lab-candidate,compress=zstd", "rd.luks.name=1111=root", "rd.luks.key=1111=/etc/cryptsetup-keys.d/root.key", "maclab.job=j1", labBootMarker} {
		if !slices.Contains(fields, w) {
			t.Errorf("cmdline %q lacks %q", res.Cmdline, w)
		}
	}
	if strings.Contains(res.Cmdline, "subvol=@,") {
		t.Errorf("cmdline still boots @: %q", res.Cmdline)
	}
	if _, err := os.Stat(filepath.Join(f.h.Lim.Stage, "j1", "uki.efi")); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(f.h.Lim.Conf)
	if !strings.Contains(string(conf), "/"+entryPrefix+"j1\n") {
		t.Fatalf("limine.conf has no entry:\n%s", conf)
	}
	if !strings.Contains(string(conf), "//linux-aurora") {
		t.Fatal("known-good entry lost")
	}
	if f.ran("umount") == nil {
		t.Fatal("top level left mounted")
	}
}

func TestACandidateIsMutedAndKeepsItsJournalInItsOwnSubvolume(t *testing.T) {
	f := newCandidateMac(t)
	if _, err := f.h.stage(context.Background(), "j1", candidateArgs(t)); err != nil {
		t.Fatal(err)
	}
	cand := filepath.Join(f.h.Top, api.CandidateSubvolume)
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(cand, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if wp := read("etc/wireplumber/wireplumber.conf.d/90-lab-candidate-mute.conf"); !strings.Contains(wp, "device.routes.default-sink-volume = 0.0") || !strings.Contains(wp, "device.restore-routes = false") {
		t.Fatalf("wireplumber: %s", wp)
	}
	if u := read("etc/systemd/user/lab-candidate-mute.service"); !strings.Contains(u, "wpctl set-mute @DEFAULT_AUDIO_SINK@ 1") {
		t.Fatalf("mute unit: %s", u)
	}
	if _, err := os.Lstat(filepath.Join(cand, "etc/systemd/user/default.target.wants/lab-candidate-mute.service")); err != nil {
		t.Fatal("mute unit not enabled")
	}
	if a := read("etc/systemd/system/lab-candidate-alsa-mute.service"); !strings.Contains(a, "amixer") {
		t.Fatalf("alsa unit: %s", a)
	}
	if j := read("etc/systemd/journald.conf.d/90-lab-candidate.conf"); !strings.Contains(j, "Storage=persistent") {
		t.Fatal(j)
	}
	if _, err := os.Stat(filepath.Join(cand, "var/log/journal")); err != nil {
		t.Fatal(err)
	}
	fstab := read("etc/fstab")
	if !strings.Contains(fstab, "subvol=/@lab-candidate 0 0") || strings.Contains(fstab, "subvol=/@ 0") {
		t.Fatalf("fstab root: %s", fstab)
	}
	if !strings.Contains(fstab, "# lab candidate: UUID=abcd /var/log") || !strings.Contains(fstab, "\nUUID=abcd /home") {
		t.Fatalf("fstab /var/log: %s", fstab)
	}
	if strings.TrimSpace(read(candidateMarker)) != "aurora-7.3" {
		t.Fatal("no marker")
	}
	// @ itself is untouched.
	if _, err := os.Stat(filepath.Join(f.h.Top, "@/etc/wireplumber")); err == nil {
		t.Fatal("@ was muted")
	}
}

func TestACandidateFileWithTheWrongSHA256IsRefusedBeforeAnythingChanges(t *testing.T) {
	f := newCandidateMac(t)
	p := candidatePkg(t, "linux-asahi", "kernel bytes")
	p.SHA256 = strings.Repeat("0", 64)
	_, err := f.h.stage(context.Background(), "j1", candidateArgs(t, p))
	if err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Fatalf("err = %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("ran %q", f.calls)
	}
}

func TestAFailedInstallLeavesNoCandidateBehind(t *testing.T) {
	f := newCandidateMac(t)
	f.fail["pacman"] = errors.New("exit 1")
	if _, err := f.h.stage(context.Background(), "j1", candidateArgs(t)); err == nil {
		t.Fatal("staged")
	}
	if _, err := os.Stat(filepath.Join(f.h.Top, api.CandidateSubvolume)); err == nil {
		t.Fatal("candidate left behind")
	}
	if _, err := os.Stat(filepath.Join(f.h.Lim.Stage, "j1")); err == nil {
		t.Fatal("stage dir left behind")
	}
}

func TestABootLoaderInASetNeedsASighting(t *testing.T) {
	for _, name := range []string{"m1n1", "uboot-asahi", "uboot-aurora"} {
		f := newCandidateMac(t)
		a := candidateArgs(t, candidatePkg(t, "linux-asahi", "k"), candidatePkg(t, name, "b"))
		_, err := f.h.stage(context.Background(), "j1", a)
		if err == nil || !strings.Contains(err.Error(), "Sighting") || len(f.calls) != 0 {
			t.Fatalf("%s: err = %v, calls %q", name, err, f.calls)
		}
	}
}

func TestABootLoaderSwapKeepsTheOldBootBinAndSaysHowToRestoreIt(t *testing.T) {
	f := newCandidateMac(t)
	a := candidateArgs(t, candidatePkg(t, "linux-asahi", "k"), candidatePkg(t, "m1n1", "b"))
	a.Sighting = true
	res, err := f.h.stage(context.Background(), "j1", a)
	if err != nil {
		t.Fatal(err)
	}
	prev, _ := os.ReadFile(filepath.Join(f.h.M1N1Dir, "boot-prev.bin"))
	now, _ := os.ReadFile(filepath.Join(f.h.M1N1Dir, "boot.bin"))
	if string(prev) != "old boot.bin" || string(now) != "new boot.bin" {
		t.Fatalf("prev %q now %q", prev, now)
	}
	cand := filepath.Join(f.h.Top, api.CandidateSubvolume)
	env := strings.Join(f.envs["update-m1n1"], " ")
	if !strings.Contains(env, "M1N1="+filepath.Join(cand, "usr/lib/asahi-boot/m1n1.bin")) {
		t.Fatalf("update-m1n1 env %q", env)
	}
	if !res.BootLoader || !strings.Contains(res.RestoreSteps, "cp boot-prev.bin boot.bin") || !strings.Contains(res.RestoreSteps, "diskutil mount") {
		t.Fatalf("res = %+v", res)
	}
}

func TestACandidateBootIsConfirmedWithTheDesktopTheGPUDriverAndNoFailedUnits(t *testing.T) {
	f := newCandidateMac(t)
	f.h.Cmdline = "root=UUID=abcd rootflags=subvol=@lab-candidate maclab.job=j1"
	drv := filepath.Join(t.TempDir(), "aurora")
	os.MkdirAll(drv, 0o755)
	os.MkdirAll(filepath.Join(f.h.DRMDir, "renderD128", "device"), 0o755)
	os.Symlink(drv, filepath.Join(f.h.DRMDir, "renderD128", "device", "driver"))
	base := f.h.Run
	failed := ""
	f.h.Run = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		base(ctx, env, name, args...)
		switch {
		case name == "systemctl" && slices.Contains(args, "--failed"):
			return []byte(failed), nil
		case name == "systemctl":
			return []byte("active\n"), nil
		}
		return nil, nil
	}
	c, err := f.h.check(context.Background(), api.CandidateCheckArgs{GPUDriver: "aurora"})
	if err != nil || c.State != api.CandidateConfirmed || !c.Desktop {
		t.Fatalf("%+v %v", c, err)
	}
	failed = "foo.service loaded failed failed Foo\n"
	c, _ = f.h.check(context.Background(), api.CandidateCheckArgs{GPUDriver: "aurora"})
	if c.State != api.CandidateNotYet || !slices.Equal(c.FailedUnits, []string{"foo.service"}) {
		t.Fatalf("%+v", c)
	}
	failed = ""
	c, _ = f.h.check(context.Background(), api.CandidateCheckArgs{GPUDriver: "asahi"})
	if c.State != api.CandidateNotYet || !strings.Contains(strings.Join(c.Problems, " "), "asahi") {
		t.Fatalf("%+v", c)
	}
	f.h.Cmdline = candidateKG
	if _, err := f.h.check(context.Background(), api.CandidateCheckArgs{}); err == nil {
		t.Fatal("checked a known-good boot as a candidate")
	}
}

func TestBackOnKnownGoodTheCandidatesJournalIsReadFromItsSubvolume(t *testing.T) {
	f := newCandidateMac(t)
	if _, err := f.h.stage(context.Background(), "j1", candidateArgs(t)); err != nil {
		t.Fatal(err)
	}
	f.h.Cmdline = candidateKG
	r, err := f.h.result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	jc := f.ran("journalctl")
	if r.State != api.CandidateBootFailed || !strings.Contains(r.Journal, "candidate journal line") ||
		jc[slices.Index(jc, "-D")+1] != filepath.Join(f.h.Top, api.CandidateSubvolume, "var/log/journal") {
		t.Fatalf("%+v %q", r, jc)
	}
}

func TestCandidateCleanupDeletesTheSubvolumeAndEntryUnlessKept(t *testing.T) {
	for _, keep := range []bool{false, true} {
		f := newCandidateMac(t)
		if _, err := f.h.stage(context.Background(), "j1", candidateArgs(t)); err != nil {
			t.Fatal(err)
		}
		if err := f.h.cleanup(context.Background(), "j1", keep); err != nil {
			t.Fatal(err)
		}
		_, err := os.Stat(filepath.Join(f.h.Top, api.CandidateSubvolume))
		if kept := err == nil; kept != keep {
			t.Fatalf("keep %v: subvolume kept %v", keep, kept)
		}
		conf, _ := os.ReadFile(f.h.Lim.Conf)
		if strings.Contains(string(conf), entryPrefix+"j1") {
			t.Fatal("entry left")
		}
		if _, err := os.Stat(filepath.Join(f.h.Lim.Stage, "j1")); err == nil {
			t.Fatal("stage dir left")
		}
	}
}

func TestStagingACandidateNeedsFullAccess(t *testing.T) {
	b, _ := json.Marshal(api.CandidateStageArgs{})
	if !needsFull(api.Command{Kind: api.CmdCandidateStage, Args: b}) {
		t.Fatal("candidate_stage runs at Qualification")
	}
}

func TestTheAuditLogSaysWhatACandidateBootDoes(t *testing.T) {
	b, _ := json.Marshal(candidateArgs(t))
	if d := describe(api.Command{Kind: api.CmdCandidateStage, Args: b}); !strings.Contains(d, "Package set aurora-7.3 (2 packages)") {
		t.Fatal(d)
	}
	if windowGated(api.CmdCandidateCleanup) || windowGated(api.CmdCandidateResult) || !windowGated(api.CmdCandidateStage) {
		t.Fatal("window gating")
	}
}
