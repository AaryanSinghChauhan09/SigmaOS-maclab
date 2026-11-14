package repotest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const module = "github.com/omacom/omarchy-maclab"

var (
	asksForAHome = regexp.MustCompile(`\bos\.User(Home|Config|Cache)Dir\(`)
	hasTestMain  = regexp.MustCompile(`(?m)^func TestMain\(`)
)

type goPackage struct {
	imports     map[string]bool // by its own files, tests left out
	testImports map[string]bool
	asksHome    bool // a file of the package, tests left out, asks the system for a home folder
	tests       map[string]string
}

// goPackages reads every Go package of the repository, whatever system its
// files are built for.
func goPackages(t *testing.T) map[string]*goPackage {
	t.Helper()
	top := root(t)
	out := map[string]*goPackage{}
	err := filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(top, path)
		if d.IsDir() {
			switch {
			case rel == "site", rel == "cli", d.Name() == "testdata", d.Name() == "node_modules", strings.HasPrefix(d.Name(), ".") && rel != ".":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, b, parser.ImportsOnly)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		p := out[dir]
		if p == nil {
			p = &goPackage{imports: map[string]bool{}, testImports: map[string]bool{}, tests: map[string]string{}}
			out[dir] = p
		}
		test := strings.HasSuffix(path, "_test.go")
		for _, spec := range f.Imports {
			name, _ := strconv.Unquote(spec.Path.Value)
			if test {
				p.testImports[name] = true
			} else {
				p.imports[name] = true
			}
		}
		if test {
			p.tests[filepath.Base(path)] = string(b)
		} else if asksForAHome.Match(b) {
			p.asksHome = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A test must never read or write the files of whoever runs it: lab's saved
// login, labd's default data, a CLI's state. Every package whose tests can
// reach a home folder, through code that asks the system for one or by
// starting a program, gives its test binary a home of its own.
func TestEveryTestBinaryThatCanReachAHomeHasItsOwn(t *testing.T) {
	packages := goPackages(t)
	var reaches func(dir string, seen map[string]bool) bool
	reaches = func(dir string, seen map[string]bool) bool {
		p := packages[dir]
		if p == nil || seen[dir] {
			return false
		}
		seen[dir] = true
		if p.asksHome {
			return true
		}
		for name := range p.imports {
			if rest, ok := strings.CutPrefix(name, module+"/"); ok && reaches(rest, seen) {
				return true
			}
		}
		return false
	}
	var missing []string
	for dir, p := range packages {
		if len(p.tests) == 0 || dir == "internal/testhome" {
			continue
		}
		why := ""
		seen := map[string]bool{}
		switch {
		case reaches(dir, seen):
			why = "its code asks the system for a home folder"
		case p.testImports["os/exec"]:
			why = "its tests start programs"
		}
		for name := range p.testImports {
			if rest, ok := strings.CutPrefix(name, module+"/"); ok && why == "" && reaches(rest, seen) {
				why = "its tests use code that asks the system for a home folder"
			}
		}
		for _, text := range p.tests {
			if why == "" && asksForAHome.MatchString(text) {
				why = "its tests ask the system for a home folder"
			}
		}
		if why == "" {
			continue
		}
		own := false
		for _, text := range p.tests {
			if hasTestMain.MatchString(text) && strings.Contains(text, "testhome.") {
				own = true
			}
		}
		if !own {
			missing = append(missing, dir+": "+why)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("these packages' tests can reach the home of whoever runs them. Give each a TestMain that calls testhome.Main(m) (internal/testhome):\n  %s", strings.Join(missing, "\n  "))
	}
	for _, dir := range []string{"cmd/lab", "cmd/labd", "internal/client"} {
		if p := packages[dir]; p == nil || len(p.tests) == 0 || !reaches(dir, map[string]bool{}) {
			t.Errorf("%s is not seen as reaching a home folder: the check has gone blind", dir)
		}
	}
}
