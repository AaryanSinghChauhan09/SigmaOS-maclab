package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLeaseWho(t *testing.T) {
	for _, c := range []struct{ holder, by, want string }{
		{"w7C-reload", "josh", "w7C-reload (josh)"},
		{"scott@pancake", "", "scott@pancake"},
		{"chris", "chris", "chris"},
	} {
		if got := (&Lease{Holder: c.holder, By: c.by}).Who(); got != c.want {
			t.Errorf("Who(%q, %q) = %q, want %q", c.holder, c.by, got, c.want)
		}
	}
}

func TestLeaseRecordedByOlderControllerKeepsItsOwner(t *testing.T) {
	lease := &Lease{Holder: "review-worker", By: "ada"}
	if !lease.HeldBy("review-worker", "ada") {
		t.Fatal("recorded owner lost access to its lease")
	}
	if lease.HeldBy("review-worker", "other") {
		t.Fatal("matching holder string bypassed the recorded owner")
	}
	lease.Maintainer = "new-owner"
	if lease.HeldBy("review-worker", "ada") || !lease.HeldBy("review-worker", "new-owner") {
		t.Fatal("explicit ownership must take precedence over legacy attribution")
	}
}

// A Lease is a person's: the handle sits in the field the agent's commands
// already name, the account id beside it, the holder label inside it.
func TestALeaseIsAPersonsWithTheHolderLabelInside(t *testing.T) {
	l := Lease{Holder: "grace@laptop", Maintainer: "grace", AccountID: 8, Expires: time.Unix(1, 0)}
	if !l.HeldBy("grace@laptop", "grace") || l.HeldBy("grace@mini", "grace") || l.HeldBy("grace@laptop", "ravi") {
		t.Fatal("a Lease is held by its person under its holder label, and by nobody else")
	}
	if l.Owner() != "grace" || l.Who() != "grace@laptop (grace)" {
		t.Fatalf("owner %q, who %q", l.Owner(), l.Who())
	}
	if site := (Lease{Holder: "grace", Maintainer: "grace"}); site.Who() != "grace" {
		t.Fatalf("a Lease taken on the site reads %q", site.Who())
	}
	b, _ := json.Marshal(l)
	for _, want := range []string{`"holder":"grace@laptop"`, `"maintainer":"grace"`, `"account_id":8`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("a stored Lease lacks %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), "warning") {
		t.Errorf("a Lease with no warning carries one: %s", b)
	}
	var old Lease
	if err := json.Unmarshal([]byte(`{"holder":"ada@laptop","by":"ada","expires":"2026-11-14T00:00:00Z"}`), &old); err != nil || old.Owner() != "ada" || old.AccountID != 0 {
		t.Fatalf("a Lease stored before people: %+v %v", old, err)
	}
}

func TestAConfigurationIsABoardWithItsCoreCounts(t *testing.T) {
	pro := Configuration{Board: "j314s", CPU: 10, GPU: 16}
	for _, c := range []struct {
		a, b Configuration
		same bool
		why  string
	}{
		{pro, Configuration{Board: "j314s", CPU: 10, GPU: 16}, true, "equal"},
		{pro, Configuration{Board: "j314s", CPU: 10, GPU: 14}, false, "another GPU count"},
		{pro, Configuration{Board: "j314s", CPU: 8, GPU: 14}, false, "another CPU count"},
		{pro, Configuration{Board: "j316s", CPU: 10, GPU: 16}, false, "another board"},
		{pro, Configuration{Board: "j314s"}, true, "unknown counts on the same board"},
		{Configuration{Board: "j314s"}, Configuration{Board: "j314s"}, true, "both unknown"},
		{Configuration{Board: "j314s"}, Configuration{Board: "j414c"}, false, "unknown on two boards"},
		{pro, Configuration{Board: "j314s", CPU: 10}, true, "the GPU count unknown"},
		{pro, Configuration{Board: "j314s", CPU: 8}, false, "the GPU count unknown, the CPU count different"},
	} {
		if c.a.Same(c.b) != c.same || c.b.Same(c.a) != c.same {
			t.Errorf("%s: %v and %v, same = %v", c.why, c.a, c.b, !c.same)
		}
	}
	for c, want := range map[Configuration]string{
		pro:                       "j314s, 10 CPU and 16 GPU cores",
		{Board: "j314s"}:          "j314s, core counts unknown",
		{Board: "j613", CPU: 8}:   "j613, 8 CPU cores, GPU cores unknown",
		{}:                        "board unknown, core counts unknown",
		{Board: "j314s", GPU: 16}: "j314s, CPU cores unknown, 16 GPU cores",
	} {
		if c.String() != want {
			t.Errorf("%+v reads %q, want %q", c, c.String(), want)
		}
	}
	if pro.Known() == false || (Configuration{Board: "j613", CPU: 8}).Known() {
		t.Error("a Configuration is known when both counts are")
	}
}
