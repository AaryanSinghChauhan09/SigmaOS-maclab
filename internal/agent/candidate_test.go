package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	// journal is what the candidate's journal holds when read back: "" is a
	// candidate that never ran.
	journal string
	// bootBin builds what update-m1n1 writes from its env's parts; nil
	// builds it as update-m1n1 does.
	bootBin func(m1n1, dtbs, uboot []byte) []byte
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
	f := &fakeCandidateMac{t: t, envs: map[string][]string{}, fail: map[string]error{}, krel: "7.3.0-cand", journal: "candidate journal line\n"}
	scratch := filepath.Join(dir, "scratch")
	os.MkdirAll(scratch, 0o700)
	f.h = &candidateHost{
		Top:        top,
		RootSource: "/dev/mapper/root[/@]",
		Lim:        testLimine(t),
		KnownGood:  &KnownGood{Kernel: "7.2.11-kg", Cmdline: candidateKG},
		GUIUser:    "volunteer",
		M1N1Dir:    filepath.Join(esp, "m1n1"),
		PacmanDB:   filepath.Join(dir, "pacman-local"),
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
		db := filepath.Join(root, "var/lib/pacman/local/linux-asahi-1")
		os.MkdirAll(db, 0o755)
		os.WriteFile(filepath.Join(db, "desc"), []byte("%NAME%\nlinux-asahi\n\n%VERSION%\n1\n\n"), 0o644)
		os.WriteFile(filepath.Join(db, "files"), []byte("%FILES%\nusr/\nusr/lib/\nusr/lib/modules/\nusr/lib/modules/"+f.krel+"/\nusr/lib/modules/"+f.krel+"/pkgbase\nusr/lib/modules/"+f.krel+"/vmlinuz\n\n"), 0o644)
		boot := filepath.Join(root, "usr/lib/asahi-boot")
		os.MkdirAll(boot, 0o755)
		os.WriteFile(filepath.Join(boot, "m1n1.bin"), testM1N1, 0o644)
		os.WriteFile(filepath.Join(boot, "u-boot-nodtb.bin"), testUBoot, 0o644)
		os.MkdirAll(filepath.Join(mods, "dtbs"), 0o755)
		os.WriteFile(filepath.Join(mods, "dtbs", "t6021-j416c.dtb"), testDTB(100), 0o644)
		os.WriteFile(filepath.Join(mods, "dtbs", "t6021-j414c.dtb"), testDTB(64), 0o644)
	case "mkinitcpio":
		out := args[slices.Index(args, "-U")+1]
		return nil, os.WriteFile(out, []byte("uki for "+strings.Join(args, " ")), 0o644)
	case "update-m1n1":
		get := func(k string) string {
			for _, e := range env {
				if v, ok := strings.CutPrefix(e, k+"="); ok {
					return v
				}
			}
			return ""
		}
		m, err := os.ReadFile(get("M1N1"))
		if err != nil {
			return nil, err
		}
		u, err := os.ReadFile(get("U_BOOT"))
		if err != nil {
			return nil, err
		}
		paths, _ := filepath.Glob(get("DTBS"))
		var dtbs []byte
		for _, p := range paths {
			b, _ := os.ReadFile(p)
			dtbs = append(dtbs, b...)
		}
		build := f.bootBin
		if build == nil {
			build = func(m, d, u []byte) []byte { return testBootBin(m, d, u, "chosen.foo=1\n") }
		}
		return nil, os.WriteFile(args[0], build(m, dtbs, u), 0o644)
	case "journalctl":
		if f.journal == "" {
			return []byte("-- No entries --\n"), nil
		}
		return []byte(f.journal), nil
	}
	return nil, nil
}

var (
	testM1N1  = bytes.Repeat([]byte{0x09, 0x06, 0x80, 0xd2}, 256)
	testUBoot = bytes.Repeat([]byte("u-boot "), 300)
)

// testDTB is a device tree of n bytes: its header's magic and totalsize.
func testDTB(n int) []byte {
	b := make([]byte, n)
	binary.BigEndian.PutUint32(b, 0xd00dfeed)
	binary.BigEndian.PutUint32(b[4:], uint32(n))
	return b
}

// testBootBin is update-m1n1's boot.bin: m1n1, the device trees, U-Boot
// gzipped and the m1n1.conf options.
func testBootBin(m1n1, dtbs, uboot []byte, conf string) []byte {
	var z bytes.Buffer
	w := gzip.NewWriter(&z)
	w.Write(uboot)
	w.Close()
	out := append(append(append([]byte{}, m1n1...), dtbs...), z.Bytes()...)
	return append(out, conf...)
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
	for _, name := range []string{"m1n1", "m1n1-aurora", "uboot-asahi", "uboot-aurora"} {
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
	want := testBootBin(testM1N1, append(testDTB(64), testDTB(100)...), testUBoot, "chosen.foo=1\n")
	if string(prev) != "old boot.bin" || !bytes.Equal(now, want) {
		t.Fatalf("prev %q, now %d bytes, want %d", prev, len(now), len(want))
	}
	if _, err := os.Stat(filepath.Join(f.h.M1N1Dir, "boot-candidate.bin")); err == nil {
		t.Error("the staged boot.bin was left behind")
	}
	cand := filepath.Join(f.h.Top, api.CandidateSubvolume)
	env := strings.Join(f.envs["update-m1n1"], " ")
	for _, w := range []string{
		"M1N1=" + filepath.Join(cand, "usr/lib/asahi-boot/m1n1.bin"),
		"U_BOOT=" + filepath.Join(cand, "usr/lib/asahi-boot/u-boot-nodtb.bin"),
		"DTBS=" + filepath.Join(cand, "usr/lib/modules", f.krel, "dtbs", "*.dtb"),
		"CONFIG=" + filepath.Join(cand, "etc/m1n1.conf"),
	} {
		if !strings.Contains(env, w) {
			t.Errorf("update-m1n1 env has no %s: %q", w, env)
		}
	}
	if c := f.ran("update-m1n1"); len(c) != 2 || c[1] != filepath.Join(f.h.M1N1Dir, "boot-candidate.bin") {
		t.Errorf("update-m1n1 ran as %q: it builds beside boot.bin, never over it", c)
	}
	if err := f.h.cleanup(context.Background(), "j1", false); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.ReadFile(filepath.Join(f.h.M1N1Dir, "boot.bin")); string(now) != "old boot.bin" {
		t.Errorf("candidate_cleanup left boot.bin %d bytes, not the known-good one", len(now))
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

// pacman's hooks keep the running kernel's modules through a kernel upgrade
// (kernel-modules-hook: uname -r, which in pacman's chroot is the Known-good
// kernel), so the candidate still holds the Known-good release, pkgbase and
// vmlinuz included, and it sorts before the set's. The candidate boots the
// kernel the set's package installed, as pacman's database lists it.
func TestACandidateBootsTheKernelItsSetInstalledNotTheOneKeptForTheRunningSystem(t *testing.T) {
	f := newCandidateMac(t)
	os.WriteFile(filepath.Join(f.h.Top, "@/usr/lib/modules/7.2.11-kg/pkgbase"), []byte("linux-asahi\n"), 0o644)
	res, err := f.h.stage(context.Background(), "j1", candidateArgs(t))
	if err != nil {
		t.Fatal(err)
	}
	mk := f.ran("mkinitcpio")
	if res.KernelRelease != f.krel || mk[slices.Index(mk, "-k")+1] != f.krel {
		t.Fatalf("the candidate boots %s (mkinitcpio %q), want the set's %s", res.KernelRelease, mk, f.krel)
	}
}

// A set without a kernel boots the Known-good kernel from the candidate.
func TestACandidateWithoutAKernelInItsSetBootsTheKnownGoodKernel(t *testing.T) {
	f := newCandidateMac(t)
	res, err := f.h.stage(context.Background(), "j1", candidateArgs(t, candidatePkg(t, "mesa-aurora", "mesa bytes")))
	if err != nil {
		t.Fatal(err)
	}
	if res.KernelRelease != "7.2.11-kg" {
		t.Fatalf("kernel %s", res.KernelRelease)
	}
}

// The candidate is a snapshot of @ taken before boot_once writes the Lab
// boot's record, so it holds the last Lab boot's: its early unit would see
// that record's deadlines long past and reboot the candidate as soon as it
// starts, back to the default entry. boot_once writes the record it arms
// into the candidate too, and forgets the last boot's contact and note there.
func TestArmingACandidateWritesItsLabBootRecordIntoTheCandidate(t *testing.T) {
	f := newCandidateMac(t)
	old := filepath.Join(f.h.Top, "@", f.h.WorkDir, labBootDir)
	os.MkdirAll(old, 0o755)
	stale := labBootDeadlines{Armed: time.Unix(1000, 0), NoContact: time.Unix(2000, 0), Ceiling: time.Unix(3000, 0)}
	writeLabBootDeadlines(filepath.Join(old, labBootDeadlineFile), stale)
	os.WriteFile(filepath.Join(old, labBootContactFile), []byte("old-boot\n"), 0o644)
	os.WriteFile(filepath.Join(old, labBootSeenFile), []byte(`{"boot_id":"old-boot","deadline":"ceiling"}`), 0o644)
	if _, err := f.h.stage(context.Background(), "j1", candidateArgs(t)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	d := newLabBootDeadlines(now, 5*time.Minute, now.Add(time.Hour))
	f.calls = nil
	if err := f.h.armLabBoot(context.Background(), "j1", func(dir string) error { return writeLabBootRecord(dir, d) }); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(f.h.Top, api.CandidateSubvolume, f.h.WorkDir, labBootDir)
	got, err := readLabBootDeadlines(filepath.Join(in, labBootDeadlineFile))
	if err != nil || !got.Ceiling.Equal(d.Ceiling) || !got.NoContact.Equal(d.NoContact) {
		t.Fatalf("the candidate's record is %+v (%v), want %+v", got, err, d)
	}
	for _, n := range []string{labBootContactFile, labBootSeenFile} {
		if _, err := os.Stat(filepath.Join(in, n)); err == nil {
			t.Errorf("the candidate keeps the last Lab boot's %s", n)
		}
	}
	if f.ran("mount") == nil || f.ran("umount") == nil {
		t.Fatalf("calls %q", f.calls)
	}
	// Another job's entry is no candidate: nothing is mounted or written.
	f.calls = nil
	called := false
	if err := f.h.armLabBoot(context.Background(), "j2", func(string) error { called = true; return nil }); err != nil || called || len(f.calls) != 0 {
		t.Fatalf("armed j2: %v, wrote %v, calls %q", err, called, f.calls)
	}
}

// Back on known-good, a candidate whose journal is empty and whose Lab boot
// record no early unit ever noted never ran: the result says so.
func TestACandidateThatNeverRanIsReadBackAsNotBooted(t *testing.T) {
	f := newCandidateMac(t)
	f.journal = ""
	if _, err := f.h.stage(context.Background(), "j1", candidateArgs(t)); err != nil {
		t.Fatal(err)
	}
	f.h.Cmdline = candidateKG
	r, err := f.h.result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !r.NotBooted || r.State != api.CandidateBootFailed {
		t.Fatalf("%+v", r)
	}
}

// A candidate that ran but was rebooted by its early unit says which
// deadline did it.
func TestACandidateRebootedAtADeadlineSaysWhich(t *testing.T) {
	f := newCandidateMac(t)
	f.journal = ""
	if _, err := f.h.stage(context.Background(), "j1", candidateArgs(t)); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(f.h.Top, api.CandidateSubvolume, f.h.WorkDir, labBootDir)
	os.MkdirAll(in, 0o755)
	os.WriteFile(filepath.Join(in, labBootSeenFile), []byte(`{"boot_id":"cand-boot","deadline":"no_contact"}`), 0o644)
	f.h.Cmdline = candidateKG
	r, err := f.h.result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.NotBooted || r.Deadline != api.DeadlineNoContact || r.BootID != "cand-boot" {
		t.Fatalf("%+v", r)
	}
}

// candidateArmSystem is a Sim whose staged entry is a Candidate boot with
// its Lab boot directory at dir.
type candidateArmSystem struct {
	*Sim
	dir   string
	entry string
	err   error
}

func (s *candidateArmSystem) ArmCandidateLabBoot(ctx context.Context, entry string, write func(string) error) error {
	s.entry = entry
	if s.err != nil {
		return s.err
	}
	return write(s.dir)
}

// boot_once writes the same Lab boot record into the candidate as its own,
// and fails, as a failed arm does, if it cannot.
func TestBootOnceGivesACandidateTheLabBootRecordItArms(t *testing.T) {
	work := t.TempDir()
	sys := &candidateArmSystem{Sim: NewSim(t.TempDir()), dir: filepath.Join(t.TempDir(), "cand", labBootDir)}
	os.MkdirAll(sys.dir, 0o755)
	os.WriteFile(filepath.Join(sys.dir, labBootContactFile), []byte("old-boot\n"), 0o644)
	a := New(&Config{Server: "http://127.0.0.1:1", Secret: "test", WorkDir: work}, sys, log.New(io.Discard, "", 0))
	args := api.BootOnceArgs{Entry: entryPrefix + "j1", BootTimeoutSec: 300, JobSec: 3600}
	if err := a.armLabBoot(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	mine, _ := os.ReadFile(filepath.Join(work, labBootDir, labBootDeadlineFile))
	theirs, _ := os.ReadFile(filepath.Join(sys.dir, labBootDeadlineFile))
	if len(mine) == 0 || string(mine) != string(theirs) || sys.entry != args.Entry {
		t.Fatalf("own record %s, candidate's %s, entry %q", mine, theirs, sys.entry)
	}
	if _, err := os.Stat(filepath.Join(sys.dir, labBootContactFile)); err == nil {
		t.Fatal("the candidate keeps the last Lab boot's contact")
	}
	sys.err = errors.New("the candidate subvolume is gone")
	if err := a.armLabBoot(context.Background(), args); err == nil || !strings.Contains(err.Error(), "the candidate subvolume is gone") {
		t.Fatalf("arm: %v", err)
	}
}

// candidate_result keeps the candidate's journal with the job, as a job file
// under the name labd gives (one per side of a Compare).
func TestCandidateResultKeepsTheJournalWithTheJob(t *testing.T) {
	var (
		mu    sync.Mutex
		files = map[string]string{}
	)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/api/agent/upload" && r.URL.Query().Get("job") == "j1" {
			mu.Lock()
			files[r.URL.Query().Get("name")] = string(b)
			mu.Unlock()
		}
		w.Write([]byte("ok"))
	}))
	t.Cleanup(hs.Close)
	a := New(&Config{Server: hs.URL, Secret: "test", WorkDir: t.TempDir()}, NewSim(t.TempDir()), log.New(io.Discard, "", 0))
	args, _ := json.Marshal(api.CandidateResultArgs{Name: "candidate-b"})
	out, err := a.candidate(context.Background(), api.Command{ID: "c1", Kind: api.CmdCandidateResult, JobID: "j1", Args: args})
	if err != nil {
		t.Fatal(err)
	}
	f := out.(api.CandidateFailure)
	mu.Lock()
	got := files["candidate-b/journal.txt"]
	mu.Unlock()
	if !slices.Equal(f.Files, []string{"candidate-b/journal.txt"}) || !strings.Contains(got, "gdm: no session") {
		t.Fatalf("files %q, uploaded %q", f.Files, got)
	}
	// A name that is no plain file name keeps the journal under "candidate".
	args, _ = json.Marshal(api.CandidateResultArgs{Name: "../x"})
	out, _ = a.candidate(context.Background(), api.Command{ID: "c2", Kind: api.CmdCandidateResult, JobID: "j1", Args: args})
	if f := out.(api.CandidateFailure); !slices.Equal(f.Files, []string{"candidate/journal.txt"}) {
		t.Fatalf("files %q", f.Files)
	}
}

func TestASetAtTheInstalledBootLoaderVersionDoesNotChangeTheBootLoader(t *testing.T) {
	f := newCandidateMac(t)
	os.MkdirAll(filepath.Join(f.h.PacmanDB, "m1n1-aurora-1"), 0o755)
	os.WriteFile(filepath.Join(f.h.PacmanDB, "m1n1-aurora-1", "desc"), []byte("%NAME%\nm1n1-aurora\n"), 0o644)
	a := candidateArgs(t, candidatePkg(t, "linux-asahi", "k"), candidatePkg(t, "m1n1-aurora", "b"))
	res, err := f.h.stage(context.Background(), "j1", a)
	if err != nil {
		t.Fatal(err)
	}
	if res.BootLoader || f.ran("update-m1n1") != nil {
		t.Fatalf("a captured set's own m1n1 changed the boot loader: %+v", res)
	}
}

func TestANewBootBinThatIsNotSoundIsRefusedAndBootBinStays(t *testing.T) {
	gz := func(u []byte) []byte {
		var z bytes.Buffer
		w := gzip.NewWriter(&z)
		w.Write(u)
		w.Close()
		return z.Bytes()
	}
	for _, c := range []struct {
		name, want string
		build      func(m, d, u []byte) []byte
	}{
		{"empty", "it is empty", func(m, d, u []byte) []byte { return nil }},
		{"cut in m1n1", "does not start with the candidate's m1n1", func(m, d, u []byte) []byte { return m[:100] }},
		{"another m1n1", "does not start with the candidate's m1n1", func(m, d, u []byte) []byte {
			return testBootBin(append([]byte("x"), m[1:]...), d, u, "")
		}},
		{"no device tree", "no device tree follows m1n1", func(m, d, u []byte) []byte { return testBootBin(m, nil, u, "") }},
		{"cut in a device tree", "device tree 2 is cut short", func(m, d, u []byte) []byte { return append(append([]byte{}, m...), d[:100]...) }},
		{"no U-Boot", "no gzipped U-Boot", func(m, d, u []byte) []byte { return append(append([]byte{}, m...), d...) }},
		{"cut in U-Boot", "U-Boot is cut short", func(m, d, u []byte) []byte {
			b := testBootBin(m, d, u, "")
			return b[:len(b)-10]
		}},
		{"another U-Boot", "U-Boot is not the candidate's", func(m, d, u []byte) []byte { return testBootBin(m, d, u[:10], "") }},
		{"junk after U-Boot", "unexpected bytes after U-Boot", func(m, d, u []byte) []byte {
			return append(append(append([]byte{}, m...), d...), append(gz(u), "rm -rf /\n"...)...)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newCandidateMac(t)
			f.bootBin = c.build
			a := candidateArgs(t, candidatePkg(t, "linux-asahi", "k"), candidatePkg(t, "m1n1-aurora", "b"))
			a.Sighting = true
			_, err := f.h.stage(context.Background(), "j1", a)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "boot.bin is unchanged") {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if now, _ := os.ReadFile(filepath.Join(f.h.M1N1Dir, "boot.bin")); string(now) != "old boot.bin" {
				t.Errorf("boot.bin changed: %d bytes", len(now))
			}
			if _, err := os.Stat(filepath.Join(f.h.M1N1Dir, "boot-candidate.bin")); err == nil {
				t.Error("the refused boot.bin was left behind")
			}
		})
	}
}

func TestACandidateWithoutUBootIsRefusedBeforeBuildingABootBin(t *testing.T) {
	f := newCandidateMac(t)
	a := candidateArgs(t, candidatePkg(t, "linux-asahi", "k"), candidatePkg(t, "uboot-aurora", "b"))
	a.Sighting = true
	orig := f.h.Run
	f.h.Run = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		out, err := orig(ctx, env, name, args...)
		if name == "pacman" {
			os.Remove(filepath.Join(args[slices.Index(args, "--sysroot")+1], "usr/lib/asahi-boot/u-boot-nodtb.bin"))
		}
		return out, err
	}
	_, err := f.h.stage(context.Background(), "j1", a)
	if err == nil || !strings.Contains(err.Error(), "no usr/lib/asahi-boot/u-boot-nodtb.bin") || f.ran("update-m1n1") != nil {
		t.Fatalf("err = %v, update-m1n1 %q", err, f.ran("update-m1n1"))
	}
	if now, _ := os.ReadFile(filepath.Join(f.h.M1N1Dir, "boot.bin")); string(now) != "old boot.bin" {
		t.Errorf("boot.bin changed")
	}
}

// On a Candidate boot the running root is @lab-candidate, whose kernels are
// the set's: preflight must not call the known-good kernel gone, nor send
// anyone to lab baseline, which would make the candidate's kernel known-good.
func TestPreflightOnACandidateBootDoesNotCallTheKnownGoodKernelGone(t *testing.T) {
	kg := &KnownGood{Kernel: "7.2.11-15-1-ARCH"}
	none := func(string) bool { return false }
	cand := "root=UUID=abcd rootflags=subvol=@lab-candidate,compress=zstd maclab.job=j1"
	problem, note := knownGoodCheck(cand, "kernel-aurora16", kg, nil, none)
	if problem != "" || api.StaleKnownGood(problem) {
		t.Errorf("problem %q on a Candidate boot", problem)
	}
	if note != "Candidate boot of set kernel-aurora16: the known-good system is untouched in @ (known-good kernel 7.2.11-15-1-ARCH)" {
		t.Errorf("note %q", note)
	}
	problem, note = knownGoodCheck(candidateKG, "", kg, nil, none)
	if !api.StaleKnownGood(problem) || note != "" {
		t.Errorf("on @: problem %q, note %q", problem, note)
	}
	if problem, _ = knownGoodCheck(candidateKG, "", kg, nil, func(string) bool { return true }); problem != "" {
		t.Errorf("installed known-good: %q", problem)
	}
	if problem, _ = knownGoodCheck(candidateKG, "", nil, errors.New("no known-good.json"), none); problem != "no known-good.json" {
		t.Errorf("unreadable known-good: %q", problem)
	}
}
