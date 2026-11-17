package repotest

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The table of the old site's test cases (docs/acceptance/rails-cases.md):
// each of the 225 is held by a Go test that exists, or dropped by a section
// of the spec. While site/ is there the table is held against its tests,
// so the Rails reference cannot be deleted with a case unaccounted for.

const railsCasesTable = "docs/acceptance/rails-cases.md"

// The two compare jobs written with the port: they are no case of the old site.
var portsOwnRailsTests = map[string]bool{"report_parity_test.rb": true, "page_parity_test.rb": true}

var (
	reRailsCase   = regexp.MustCompile(`(?m)^\s*test "(.*)" do\s*$`)
	reTableFile   = regexp.MustCompile(`^## (\S+_test\.rb)$`)
	reTableRow    = regexp.MustCompile(`^\| (.+) \| (.+) \|$`)
	reBackticked  = regexp.MustCompile("`([^`]+)`")
	reGoTestNamed = regexp.MustCompile(`^(?:([a-z/]+):)?(Test[A-Za-z0-9_]+)$`)
	reSpecSection = regexp.MustCompile(`spec \d+`)
)

func TestEveryRailsCaseIsHeldOrDropped(t *testing.T) {
	top := root(t)
	type row struct{ file, name, held string }
	var rows []row
	file := ""
	for _, line := range strings.Split(read(t, top, railsCasesTable), "\n") {
		if m := reTableFile.FindStringSubmatch(line); m != nil {
			file = m[1]
			continue
		}
		m := reTableRow.FindStringSubmatch(line)
		if m == nil || file == "" || m[1] == "Case" || strings.HasPrefix(m[1], "---") {
			continue
		}
		rows = append(rows, row{file, strings.ReplaceAll(m[1], `\|`, "|"), m[2]})
	}
	if len(rows) != 225 {
		t.Errorf("the table has %d cases, want the old site's 225", len(rows))
	}

	goTests := map[string]map[string]bool{}
	testsOf := func(pkg string) map[string]bool {
		if goTests[pkg] == nil {
			goTests[pkg] = map[string]bool{}
			files, _ := filepath.Glob(filepath.Join(top, filepath.FromSlash(pkg), "*_test.go"))
			for _, f := range files {
				rel, _ := filepath.Rel(top, f)
				for _, m := range regexp.MustCompile(`(?m)^func (Test\w+)\(`).FindAllStringSubmatch(read(t, top, filepath.ToSlash(rel)), -1) {
					goTests[pkg][m[1]] = true
				}
			}
		}
		return goTests[pkg]
	}
	listed := map[[2]string]int{}
	for _, r := range rows {
		listed[[2]string{r.file, r.name}]++
		if rest, dropped := strings.CutPrefix(r.held, "Dropped. "); dropped {
			if !reSpecSection.MatchString(rest) {
				t.Errorf("%s, %q: dropped without a section of the spec", r.file, r.name)
			}
			continue
		}
		named := reBackticked.FindAllStringSubmatch(r.held, -1)
		if len(named) < 2 || !strings.HasPrefix(named[0][1], "internal/") {
			t.Errorf("%s, %q: the row names no package and Go test", r.file, r.name)
			continue
		}
		for _, n := range named[1:] {
			m := reGoTestNamed.FindStringSubmatch(n[1])
			if m == nil {
				t.Errorf("%s, %q: %q is no Go test's name", r.file, r.name, n[1])
				continue
			}
			pkg := named[0][1]
			if m[1] != "" {
				pkg = m[1]
			}
			if !testsOf(pkg)[m[2]] {
				t.Errorf("%s, %q: %s has no %s", r.file, r.name, pkg, m[2])
			}
		}
	}

	if !exists(top, "site/test") {
		return
	}
	asked := map[[2]string]int{}
	err := filepath.WalkDir(filepath.Join(top, "site", "test"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.rb") || portsOwnRailsTests[filepath.Base(path)] {
			return err
		}
		rel, _ := filepath.Rel(top, path)
		for _, m := range reRailsCase.FindAllStringSubmatch(read(t, top, filepath.ToSlash(rel)), -1) {
			asked[[2]string{filepath.Base(path), m[1]}]++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for k, n := range asked {
		if listed[k] != n {
			t.Errorf("%s, %q: %d in site/test, %d in the table", k[0], k[1], n, listed[k])
		}
	}
	for k := range listed {
		if asked[k] == 0 {
			t.Errorf("%s, %q: in the table and not in site/test", k[0], k[1])
		}
	}
}
