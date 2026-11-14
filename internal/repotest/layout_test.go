package repotest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The paths of the spec's repository table (section 11).
const present = `
cmd
internal
internal/reports
builder
specs
tools
cli
schema
catalogue
installer
release
deploy
scripts/cutover
scripts/boot-test
scripts/workflow
docs/adr
docs/acceptance
docs/spec
docs/history
GLOSSARY.md
LICENSE
THIRD_PARTY_NOTICES.md
AGENTS.md
scripts/ci-local
`

// Docker and Kubernetes went with the fork; the rest are the paths the two
// trees were joined under.
const gone = `
Dockerfile
.dockerignore
deploy/k8s
deploy/labd.service
.github/workflows/image.yml
.github/workflows/publish.yml
.github/scripts/image-smoke.sh
CONTEXT.md
docs/tickets
.held
`

func TestLayout(t *testing.T) {
	top := root(t)
	for _, p := range lines(present) {
		if !exists(top, p) {
			t.Errorf("%s is missing", p)
		}
	}
	for _, p := range lines(gone) {
		if exists(top, p) {
			t.Errorf("%s is back", p)
		}
	}
}

func TestNoDockerFileAtTheRoot(t *testing.T) {
	top := root(t)
	entries, err := os.ReadDir(top)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if strings.Contains(name, "docker") || strings.HasPrefix(name, "compose.") || name == "containerfile" {
			t.Errorf("%s at the root: the lab ships no image", e.Name())
		}
	}
}

func TestThreeWorkflows(t *testing.T) {
	top := root(t)
	got, err := filepath.Glob(filepath.Join(top, ".github", "workflows", "*"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range got {
		got[i] = filepath.Base(got[i])
	}
	sort.Strings(got)
	want := "catalogue-drift.yml release.yml tests.yml"
	if strings.Join(got, " ") != want {
		t.Errorf("workflows: %s, want %s", strings.Join(got, " "), want)
	}
}

func TestLicenceKeepsBothCopyrightLines(t *testing.T) {
	licence := read(t, root(t), "LICENSE")
	for _, line := range []string{
		"MIT License",
		"Copyright (c) 2026 Chris Kearney",
		"Copyright (c) 2026 Marcelo Alcantara and contributors",
	} {
		if !strings.Contains(licence, line+"\n") {
			t.Errorf("LICENSE lost %q", line)
		}
	}
}

func TestReadmeSaysWhereTheCodeBegan(t *testing.T) {
	if !strings.Contains(read(t, root(t), "README.md"), "The code began as [maclab](https://github.com/chriskearney/maclab)") {
		t.Error("README.md no longer says the code began as maclab")
	}
}
