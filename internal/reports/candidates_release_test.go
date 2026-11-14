package reports

import (
	"reflect"
	"testing"
)

func onSetNamed(t testing.TB, name string) *Object {
	t.Helper()
	doc := imageRun(t, nil)
	at(doc, "system").(*Object).set("candidate_set", name)
	return doc
}

// A Release and a candidate set are two records, linked when the Release's
// packages are the set's: every package of the Release, at its version, on
// a Verified run of the set.
func TestAReleaseIsLinkedToTheSetWhoseVerifiedRunsCarryItsPackages(t *testing.T) {
	s := newShelf(t)
	s.add(onSetNamed(t, "edge-1"), "verified:a")
	set := s.published().CandidateSet("edge-1")
	kernel, boot := "7.1.12.aurora2-10", "20260926-1.362376005140001"
	for _, c := range []struct {
		what     string
		packages map[string]string
		want     bool
	}{
		{"its kernel package", map[string]string{"linux-aurora": kernel}, true},
		{"its kernel and boot packages", map[string]string{"linux-aurora": kernel, "omarchy-mac-boot": boot}, true},
		{"another build of the kernel", map[string]string{"linux-aurora": "7.1.12.aurora2-11"}, false},
		{"one package of two", map[string]string{"linux-aurora": kernel, "omarchy-mac-boot": "20260927-1"}, false},
		{"a package the run does not list", map[string]string{"linux-aurora": kernel, "linux-firmware": "1-1"}, false},
		{"no package", map[string]string{}, false},
	} {
		if got := set.Carries(c.packages); got != c.want {
			t.Errorf("a Release of %s: linked %v, want %v", c.what, got, c.want)
		}
	}
}

// Community runs are listed and do not count: what one says of its set's
// packages links nothing.
func TestACommunityRunLinksNoRelease(t *testing.T) {
	s := newShelf(t)
	s.add(onSetNamed(t, "edge-1"), "a")
	p := s.published()
	packages := map[string]string{"linux-aurora": "7.1.12.aurora2-10"}
	if p.CandidateSet("edge-1").Carries(packages) || len(p.SetsCarrying(packages)) != 0 {
		t.Error("a Community run's packages linked a Release to its set")
	}
}

// A set the lab knows by its omarchy package (candidate_sets.yml) is the
// set of a Release that carries exactly that package.
func TestAReleaseWithAKnownSetsOmarchyPackageIsThatSets(t *testing.T) {
	const set = "apple-test-1937418f520b-20260926"
	known := ""
	for _, k := range KnownSets() {
		if k.Name == set {
			known = k.Omarchy
		}
	}
	if known == "" {
		t.Fatalf("candidate_sets.yml no longer has %s", set)
	}
	s := newShelf(t)
	s.add(imageRun(t, nil), "a")
	s.add(onSetNamed(t, "edge-1"), "verified:b")
	p := s.published()
	if !p.CandidateSet(set).Carries(map[string]string{"omarchy": known, "linux-aurora": "9.9-1"}) {
		t.Error("a Release with the set's own omarchy package is not the set's")
	}
	if p.CandidateSet(set).Carries(map[string]string{"omarchy": known + "0"}) {
		t.Error("a Release with another omarchy package is the set's")
	}

	// Every set a Release's packages are on, in the page's order.
	if got := p.SetsCarrying(map[string]string{"omarchy": known}); !reflect.DeepEqual(got, []string{"edge-1", set}) {
		t.Errorf("the sets of a Release on both: %v", got)
	}
	if got := p.SetsCarrying(map[string]string{"linux-aurora": "7.1.12.aurora2-10"}); !reflect.DeepEqual(got, []string{"edge-1"}) {
		t.Errorf("the sets of a Release of the kernel alone: %v", got)
	}
}
