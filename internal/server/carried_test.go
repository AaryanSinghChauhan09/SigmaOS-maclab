package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/githubtest"
	"github.com/omacom/omarchy-maclab/internal/reports"
)

func carriedData(t *testing.T) (dir string, st *store) {
	t.Helper()
	dir = t.TempDir()
	st, err := openStore(filepath.Join(dir, "labd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.db.Close() })
	return dir, st
}

func TestCarriedPeopleBecomePeopleWithNoMark(t *testing.T) {
	dir, st := carriedData(t)
	now := time.Date(2026, 11, 15, 1, 0, 0, 0, time.UTC)
	signedIn := time.Date(2026, 10, 30, 23, 59, 59, 999999000, time.UTC)
	// The owner signed in to the lab in Preview, before the import.
	if err := st.signedInAs(1001, "MaralcBR", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.setMark(1001, "maintainer", true); err != nil {
		t.Fatal(err)
	}
	people := []reports.CarriedPerson{
		{AccountID: 1001, Handle: "maralcbr", SignedIn: signedIn},
		{AccountID: 1002, Handle: "scottjones", SignedIn: signedIn},
		{AccountID: 2741, Handle: "dhh"},
	}
	added, err := CarryPeople(dir, people, now)
	if err != nil || added != 2 {
		t.Fatalf("added %d, %v", added, err)
	}
	owner, err := st.knownPerson(1001)
	if err != nil || owner.Handle != "MaralcBR" || !owner.Maintainer || !owner.LastSeen.Equal(now.Add(-time.Hour)) {
		t.Errorf("the person the lab already knew changed: %+v %v", owner, err)
	}
	scott, err := st.knownPerson(1002)
	if err != nil || scott.Handle != "scottjones" || scott.Maintainer || scott.MTeam || scott.MayLend() || !scott.LastSeen.Equal(signedIn) || !scott.Added.Equal(now) {
		t.Errorf("a person with a carried sign-in: %+v %v", scott, err)
	}
	never, err := st.knownPerson(2741)
	if err != nil || never.Handle != "dhh" || never.MTeam || !never.LastSeen.IsZero() {
		t.Errorf("a handle that never signed in: %+v %v", never, err)
	}
	// Again, as after a cut: nobody is added and nobody changes.
	before, _ := st.knownPeople()
	added, err = CarryPeople(dir, people, now.Add(time.Hour))
	after, _ := st.knownPeople()
	if err != nil || added != 0 || !reflect.DeepEqual(before, after) {
		t.Errorf("a second run added %d and changed people: %v", added, err)
	}
	if known, err := KnownAccounts(dir, []int64{1001, 1002, 2741, 77}); err != nil || known != 3 {
		t.Errorf("known %d of 4, %v", known, err)
	}
}

func TestCarriedPeopleAreAllKeptOrNone(t *testing.T) {
	dir, st := carriedData(t)
	_, err := CarryPeople(dir, []reports.CarriedPerson{{AccountID: 5, Handle: "ada"}, {AccountID: 6, Handle: "not a handle"}}, time.Now())
	if err == nil {
		t.Fatal("a handle GitHub could not have was kept")
	}
	if people, _ := st.knownPeople(); len(people) != 0 {
		t.Errorf("%d people kept", len(people))
	}
	if _, err := CarryPeople(t.TempDir(), nil, time.Now()); err == nil {
		t.Error("people were carried into a folder with no labd.db")
	}
}

func TestTheImportAsksGitHubWhoseAHandleIs(t *testing.T) {
	gh := githubtest.New("id", "secret")
	gh.AddUser(3003, "RyanRHughes")
	fake := httptest.NewServer(gh)
	defer fake.Close()
	lookup, err := GitHubLookup(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	id, handle, err := lookup(context.Background(), "ryanrhughes")
	if err != nil || id != 3003 || handle != "RyanRHughes" {
		t.Errorf("%d %s %v", id, handle, err)
	}
	if _, _, err := lookup(context.Background(), "gone-handle"); !errors.Is(err, reports.ErrNoSuchAccount) {
		t.Errorf("a handle GitHub lacks: %v", err)
	}
	gh.Break(503)
	if _, _, err := lookup(context.Background(), "ryanrhughes"); err == nil || errors.Is(err, reports.ErrNoSuchAccount) {
		t.Errorf("GitHub out of reach: %v", err)
	}
	for _, bad := range []string{"https://github.example.net", "http://192.0.2.1:80", "http://127.0.0.1:1/path", "ftp://127.0.0.1"} {
		if _, err := GitHubLookup(bad); err == nil {
			t.Errorf("%s was taken as a stand-in for GitHub", bad)
		}
	}
	if _, err := GitHubLookup(""); err != nil {
		t.Error(err)
	}
}
