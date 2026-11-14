package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// The pinned elements of each public page, on the seeded corpus, against the
// old site's own pages: every element inside <main> with an id or a data-
// attribute, as tag, id, classes and data- attributes, in the page's order.
// site/test/integration/page_parity_test.rb writes the old site's
// (PAGE_PARITY_DIR/rails-pages.json) from the corpus internal/reports'
// TestTheCountsAreTheOldSites wrote there. Skipped without that folder: the
// old site runs in CI, not here.

type pinnedElement struct {
	Tag   string            `json:"tag"`
	ID    string            `json:"id"`
	Class []string          `json:"class"`
	Data  map[string]string `json:"data"`
}

func pinnedIn(main *node) []pinnedElement {
	var list []pinnedElement
	var walk func(*node)
	walk = func(at *node) {
		for _, c := range at.children {
			if c.tag == "" {
				continue
			}
			e := pinnedElement{Tag: c.tag, ID: c.attrs["id"], Class: c.classes(), Data: map[string]string{}}
			sort.Strings(e.Class)
			if e.Class == nil {
				e.Class = []string{}
			}
			for name, value := range c.attrs {
				if strings.HasPrefix(name, "data-") {
					e.Data[name] = value
				}
			}
			if e.ID != "" || len(e.Data) > 0 {
				list = append(list, e)
			}
			walk(c)
		}
	}
	walk(main)
	return list
}

// notYet are the old pages' elements that wait for a later page of the
// port: a model's benchmark scores come with Benchmarks.
func notYet(e pinnedElement) bool {
	return e.ID == "benchmarks" || contains(e.Class, "score-row")
}

func TestThePagesPinWhatTheOldSitesPin(t *testing.T) {
	dir := os.Getenv("PAGE_PARITY_DIR")
	if dir == "" {
		t.Skip("PAGE_PARITY_DIR is not set")
	}
	data, err := os.ReadFile(filepath.Join(dir, "corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var corpus []struct {
		PublicID  string `json:"public_id"`
		CreatedAt string `json:"created_at"`
		MachineID string `json:"machine_id"`
		Verified  bool   `json:"verified"`
		Body      doc    `json:"body"`
	}
	if err := json.Unmarshal(data, &corpus); err != nil || len(corpus) < 10 {
		t.Fatalf("the corpus: %d reports, %v", len(corpus), err)
	}
	l := newPublicLab(t, nil)
	for _, entry := range corpus {
		at, err := time.Parse(time.RFC3339, entry.CreatedAt)
		if err != nil {
			t.Fatal(err)
		}
		l.store(entry.PublicID, entry.Body, entry.MachineID, at, false)
		if entry.Verified {
			l.exec(`UPDATE reports SET verified = 1, handle = 'demo-tester', account_id = 4242 WHERE public_id = ?`, entry.PublicID)
		}
	}
	old := map[string][]pinnedElement{}
	data, err = os.ReadFile(filepath.Join(dir, "rails-pages.json"))
	if err == nil {
		err = json.Unmarshal(data, &old)
	}
	if err != nil || len(old) < 10 {
		t.Fatalf("the old site's pages: %d, %v", len(old), err)
	}
	for target, want := range old {
		got := pinnedIn(l.page(target).sole(t, "main"))
		var kept []pinnedElement
		for _, e := range want {
			if !notYet(e) {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			t.Errorf("%s: the old page pins nothing", target)
		}
		differ := 0
		for i := 0; i < len(kept) || i < len(got); i++ {
			var a, b string
			if i < len(kept) {
				a = fmt.Sprintf("%+v", kept[i])
			}
			if i < len(got) {
				b = fmt.Sprintf("%+v", got[i])
			}
			if (i >= len(kept) || i >= len(got) || !reflect.DeepEqual(kept[i], got[i])) && differ < 5 {
				differ++
				t.Errorf("%s, pinned element %d:\n  the old site %s\n  the port     %s", target, i, a, b)
			}
		}
	}
}
