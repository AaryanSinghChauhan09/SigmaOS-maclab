package repotest

import (
	"go/scanner"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// What a person reads from lab and lab-agent comes from these folders: the
// two commands, what they share, and the wording labd and they both use.
var messageFolders = []string{"cmd/lab/", "cmd/lab-agent/", "internal/client/", "internal/labhttp/", "internal/agent/", "internal/api/"}

// Kept names a message may carry besides the ones TestKeptNames lists: the
// repository, and what an enrolled Mac or a person's machine already holds
// under the old name and is found again by it. On a Mac: the marker lines of
// the lab's blocks in the bootloader's files, the backups setup leaves, the
// folder of staged kernels and the title of a staged boot entry.
var keptInMessages = []string{
	"omarchy-maclab",
	"maclab/omarchy-mac-lab.json",
	"maclab/client.json",
	"X-Maclab-",
	"# maclab:",
	"pre-maclab",
	"stay-awake.maclab",
	"/boot/maclab",
	"maclab %s: ",
	"maclab-",
	"maclab_",
	"maclab.",
}

// A literal that is the one word is a folder's name: lab's own under the
// config and share directories, and the lab's folder on a Mac's ESP.
var keptFolders = map[string]int{
	"cmd/lab/boot_test_front.go": 1,
	"internal/client/login.go":   1,
	"internal/agent/limine.go":   1,
}

var maclabRE = regexp.MustCompile(`(?i)maclab`)

// The product is Omarchy Mac Lab, and "maclab" alone is another project
// (spec, section 2, "Names"): no message of a command says it. Kept names
// stay as they are.
func TestNoMessageSaysMaclabAlone(t *testing.T) {
	kept := append(lines(keptStrings), keptInMessages...)
	sort.Slice(kept, func(i, j int) bool { return len(kept[i]) > len(kept[j]) })
	folders := map[string]int{}
	for path, text := range sources(t) {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		in := false
		for _, dir := range messageFolders {
			in = in || strings.HasPrefix(path, dir)
		}
		if !in {
			continue
		}
		fset := token.NewFileSet()
		file := fset.AddFile(path, fset.Base(), len(text))
		var s scanner.Scanner
		s.Init(file, []byte(text), nil, 0)
		for {
			pos, tok, lit := s.Scan()
			if tok == token.EOF {
				break
			}
			if tok != token.STRING {
				continue
			}
			said, err := strconv.Unquote(lit)
			if err != nil {
				said = lit
			}
			if said == "maclab" {
				folders[path]++
				continue
			}
			rest := variableRE.ReplaceAllString(said, "")
			rest = schemaIDRE.ReplaceAllString(rest, "")
			rest = unitRE.ReplaceAllString(rest, "")
			for _, name := range kept {
				rest = strings.ReplaceAll(rest, name, "")
			}
			if maclabRE.MatchString(rest) {
				t.Errorf("%s: says maclab alone: %s", fset.Position(pos), strings.TrimSpace(trunc(said, 160)))
			}
		}
	}
	for path, n := range folders {
		if n != keptFolders[path] {
			t.Errorf("%s: %d literals are the one word maclab, want %d: a folder's name is kept, a message never says it", path, n, keptFolders[path])
		}
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
