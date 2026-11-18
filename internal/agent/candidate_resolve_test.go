package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// A set's repo package is found in pacman's cache at its exact version, else
// downloaded with pacman -Sw; a repository that holds another version now
// leaves it unresolved, and says so.
func TestResolveRepoFindsTheExactVersion(t *testing.T) {
	cache := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(cache, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("linux-aurora-7.2.1.aurora1-1-aarch64.pkg.tar.zst")
	write("linux-aurora-7.2.1.aurora1-1-aarch64.pkg.tar.zst.sig")
	write("linux-aurora-headers-7.2.1.aurora1-1-aarch64.pkg.tar.zst")
	var downloads []string
	download := func(ctx context.Context, name string) error {
		downloads = append(downloads, name)
		switch name {
		case "mesa-aurora":
			write("mesa-aurora-25.3.0-1-aarch64.pkg.tar.zst")
		case "vkmark":
			write("vkmark-1:2025.02-1-aarch64.pkg.tar.zst")
		case "nowhere":
			return errors.New("target not found: nowhere")
		}
		return nil
	}
	got, err := resolveRepo(context.Background(), cache, download, api.CandidateResolveArgs{Packages: []api.CandidatePackage{
		{Name: "linux-aurora", Version: "7.2.1.aurora1-1"}, {Name: "mesa-aurora", Version: "25.3.0-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Packages) != 2 || !strings.HasSuffix(got.Packages[0].URL, "linux-aurora-7.2.1.aurora1-1-aarch64.pkg.tar.zst") || len(got.Packages[0].SHA256) != 64 || got.Packages[1].SHA256 == "" {
		t.Errorf("resolved %+v", got.Packages)
	}
	if strings.Join(downloads, " ") != "mesa-aurora" {
		t.Errorf("downloaded %v, want only what the cache lacked", downloads)
	}
	if _, err := resolveRepo(context.Background(), cache, download, api.CandidateResolveArgs{Packages: []api.CandidatePackage{{Name: "vkmark", Version: "1:2025.01-2"}}}); err == nil || !strings.Contains(err.Error(), "another version") {
		t.Errorf("an old version: %v", err)
	}
	if _, err := resolveRepo(context.Background(), cache, download, api.CandidateResolveArgs{Packages: []api.CandidatePackage{{Name: "nowhere", Version: "1-1"}}}); err == nil || !strings.Contains(err.Error(), "target not found") {
		t.Errorf("a missing package: %v", err)
	}
}

func TestPkgFile(t *testing.T) {
	for file, want := range map[string]string{
		"linux-aurora-headers-7.2.1.aurora1-1-aarch64.pkg.tar.zst": "linux-aurora-headers 7.2.1.aurora1-1",
		"vkmark-1:2025.01-2-aarch64.pkg.tar.xz":                    "vkmark 1:2025.01-2",
		"mesa-25.3.0-1-aarch64.pkg.tar.zst.sig":                    "",
		"notes.txt":                                                "",
	} {
		n, v, ok := pkgFile(file)
		if got := strings.TrimSpace(n + " " + v); ok != (want != "") || (ok && got != want) {
			t.Errorf("%s: %q %v, want %q", file, got, ok, want)
		}
	}
}
