package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// pacmanCache is where pacman keeps the package files it installed or
// downloaded.
var pacmanCache = "/var/cache/pacman/pkg"

// pkgFile splits a package file's name, NAME-PKGVER-PKGREL-ARCH.pkg.tar.*,
// into the package's name and its version (PKGVER-PKGREL, epoch kept).
func pkgFile(file string) (name, version string, ok bool) {
	i := strings.Index(file, ".pkg.tar")
	if i < 0 || strings.HasSuffix(file, ".sig") {
		return "", "", false
	}
	f := strings.Split(file[:i], "-")
	n := len(f)
	if n < 4 {
		return "", "", false
	}
	return strings.Join(f[:n-3], "-"), f[n-3] + "-" + f[n-2], true
}

// cachedPackage is the file in dir of the package at exactly that version.
func cachedPackage(dir, name, version string) (string, bool) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if n, v, ok := pkgFile(e.Name()); ok && n == name && v == version && e.Type().IsRegular() {
			return filepath.Join(dir, e.Name()), true
		}
	}
	return "", false
}

// resolveRepo finds each repo package of a set at its exact version: in
// pacman's cache, else downloaded from the Mac's repositories with
// pacman -Sw, which fetches only the version the repositories hold now.
func resolveRepo(ctx context.Context, cache string, download func(ctx context.Context, name string) error, a api.CandidateResolveArgs) (api.CandidateResolved, error) {
	var out api.CandidateResolved
	for _, p := range a.Packages {
		if !reCandidatePkg.MatchString(p.Name) || p.Version == "" || strings.ContainsAny(p.Version, "/ ") {
			return out, fmt.Errorf("bad package %q version %q", p.Name, p.Version)
		}
		path, ok := cachedPackage(cache, p.Name, p.Version)
		if !ok {
			if err := download(ctx, p.Name); err != nil {
				return out, fmt.Errorf("%s %s is not in pacman's cache, and pacman -Sw failed: %w", p.Name, p.Version, err)
			}
			if path, ok = cachedPackage(cache, p.Name, p.Version); !ok {
				return out, fmt.Errorf("%s %s is not in pacman's cache, and the Mac's repositories hold another version of it now", p.Name, p.Version)
			}
		}
		sum, err := fileSHA256(path)
		if err != nil {
			return out, err
		}
		p.URL, p.SHA256 = path, sum
		out.Packages = append(out.Packages, p)
	}
	return out, nil
}

func (l *Linux) ResolveCandidate(ctx context.Context, a api.CandidateResolveArgs) (api.CandidateResolved, error) {
	return resolveRepo(ctx, pacmanCache, func(ctx context.Context, name string) error {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		out, err := combinedGroup(exec.CommandContext(ctx, "pacman", "-Sw", "--noconfirm", name))
		if err != nil {
			return fmt.Errorf("%v: %s", err, lastLines(string(out), 5))
		}
		return nil
	}, a)
}

// macOSBooter is a System that can boot macOS once for a queued macOS run.
type macOSBooter interface {
	BootMacOS(ctx context.Context, started func() error) error
}

// BootMacOS is boot-macos-once without its reboot, which the agent makes
// once labd has the command's result.
func (l *Linux) BootMacOS(ctx context.Context, started func() error) error {
	return blessMacOSNext(ctx, MacOSBoot{}, started)
}

func (a *Agent) bootMacOS(ctx context.Context) (*afterAck, error) {
	mb, ok := a.sys.(macOSBooter)
	if !ok {
		return nil, errors.New("agent system cannot boot macOS")
	}
	started := func() error {
		var run api.MacOSRun
		if err := postJSON(ctx, a.cfg.client(30*time.Second), strings.TrimRight(a.cfg.Server, "/")+"/api/agent/macos-started", a.cfg.Secret, struct{}{}, &run); err != nil {
			return fmt.Errorf("labd: %w", err)
		}
		a.log.Printf("macOS run %s started: booting macOS once", run.ID)
		return nil
	}
	if err := mb.BootMacOS(ctx, started); err != nil {
		return nil, err
	}
	return &afterAck{run: a.rebootLater}, nil
}
