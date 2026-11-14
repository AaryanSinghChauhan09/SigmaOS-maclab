package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/oldlabtest"
)

var oldLabShapes = map[string]oldlabtest.Shape{"at the fork": oldlabtest.AtFork, "on omarchy-gpu": oldlabtest.OnOmarchyGPU}

var theOwner = Person{AccountID: ownerID, Handle: "owner"}

var importDay = time.Date(2026, time.November, 20, 3, 0, 0, 0, time.UTC)

// oldRows reads one of labd.db's tables as id to the row's data, read as JSON.
func oldRows(t *testing.T, dir, table string) map[string]map[string]any {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "labd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, data FROM ` + table)
	if err != nil {
		t.Fatalf("%s: %v", table, err)
	}
	defer rows.Close()
	out := map[string]map[string]any{}
	for rows.Next() {
		var id, data string
		if err := rows.Scan(&id, &data); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			t.Fatalf("%s %s: %v", table, id, err)
		}
		out[id] = m
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func oldCount(t *testing.T, dir, query string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "labd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n := -1
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// less is m without the keys named.
func less(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	for _, k := range keys {
		delete(out, k)
	}
	return out
}

// The old lab's data directory is upgraded where it lies. Every record, job
// and build is there afterwards under the id it had, with what the old lab
// wrote in it; the two Macs' records are Retired, the Lease ended and the
// out-of-band entries gone; what was done under the admin token is the
// owner's; and no credential of the old lab is left.
func TestTheOldLabsDataIsUpgradedInPlaceRecordByRecord(t *testing.T) {
	for name, shape := range oldLabShapes {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			old := oldlabtest.Make(t, dir, shape)
			records, jobs, builds := oldRows(t, dir, "devices"), oldRows(t, dir, "jobs"), oldRows(t, dir, "builds")
			files := oldlabtest.Files(t, dir)
			if len(records) != oldlabtest.Records || len(jobs) != oldlabtest.Jobs || len(builds) != oldlabtest.Builds {
				t.Fatalf("the old lab holds %d records, %d jobs and %d builds", len(records), len(jobs), len(builds))
			}

			up, err := UpgradeOldLab(dir, theOwner, importDay, false)
			if err != nil {
				t.Fatal(err)
			}
			want := OldLabCounts{Records: oldlabtest.Records, Jobs: oldlabtest.Jobs, Builds: oldlabtest.Builds}
			if up.Before != want || up.After != want {
				t.Errorf("counts before %+v and after %+v, want %+v both", up.Before, up.After, want)
			}
			if got, err := CountOldLab(dir); err != nil || got != want {
				t.Errorf("counted afterwards: %+v, %v", got, err)
			}
			if !reflect.DeepEqual(up.Retired, old.Names()) {
				t.Errorf("Retired %v, want %v", up.Retired, old.Names())
			}

			// The records: Retired, out of devices, as they were less what a
			// Retired Mac does not hold.
			if n := oldCount(t, dir, `SELECT count(*) FROM devices`); n != 0 {
				t.Errorf("%d records are still in the fleet", n)
			}
			retired := oldRows(t, dir, "retired_macs")
			for id, was := range records {
				now, ok := retired[id]
				if !ok {
					t.Errorf("the record %s is not Retired under its id", id)
					continue
				}
				if !reflect.DeepEqual(now, less(was, "lease", "oob", "active_job", "action")) {
					t.Errorf("the record %s changed:\n was %v\n now %v", id, was, now)
				}
				if now[old.Field] != old.Value {
					t.Errorf("the record %s lost %s", id, old.Field)
				}
				if n := oldCount(t, dir, `SELECT count(*) FROM retired_macs WHERE id = ? AND name = ? AND retired = ?`, id, was["name"], importDay.UnixMicro()); n != 1 {
					t.Errorf("the record %s is not Retired by its name at the import's time", id)
				}
			}
			if n := oldCount(t, dir, `SELECT count(*) FROM retired_macs WHERE how = 'import' AND volunteer_id = 0`); n != len(records) {
				t.Errorf("%d records are Retired as the old lab's Lab-owned Macs, want %d", n, len(records))
			}
			if n := oldCount(t, dir, `SELECT count(*) FROM mac_record WHERE event = 'left' AND (mac, device_id) IN (SELECT name, id FROM retired_macs)`); n != len(records) {
				t.Errorf("%d lines in the Macs' records say they left, want one for each of %d", n, len(records))
			}
			if len(retired) != len(records) {
				t.Errorf("%d Retired records, want %d", len(retired), len(records))
			}
			if !reflect.DeepEqual(up.LeasesEnded, []string{"m1pro"}) || !reflect.DeepEqual(up.OutOfBand, old.Names()) {
				t.Errorf("Leases ended on %v, out-of-band entries cleared on %v", up.LeasesEnded, up.OutOfBand)
			}
			if n := oldCount(t, dir, `SELECT count(*) FROM lease_record WHERE mac = 'm1pro' AND device_id = 'd-3377db835766' AND event = ? AND by_id = ? AND by = 'owner' AND holder = 'marcelo-agent'`, api.LeaseEnded, ownerID); n != 1 {
				t.Errorf("the Lease record has %d lines for the Lease on m1pro ended by the owner, want 1", n)
			}
			if n := oldCount(t, dir, `SELECT count(*) FROM lease_record`); n != 1 {
				t.Errorf("the Lease record has %d lines, want 1", n)
			}

			// The jobs: every one, under its id; an unfinished one ended; a
			// person's job the owner's unless the old lab named somebody else.
			after := oldRows(t, dir, "jobs")
			if len(after) != len(jobs) {
				t.Errorf("%d jobs, want %d", len(after), len(jobs))
			}
			owners, ended := 0, 0
			for _, j := range old.Jobs {
				was, now := jobs[j.ID], after[j.ID]
				if now == nil {
					t.Errorf("the job %s is gone", j.ID)
					continue
				}
				changed := []string{}
				if j.Unfinished {
					ended++
					changed = append(changed, "state", "outcome", "summary", "updated")
					if now["state"] != "done" || now["outcome"] == nil || now["summary"] == nil {
						t.Errorf("the unfinished job %s: %v %v %q", j.ID, now["state"], now["outcome"], now["summary"])
					}
					if n := oldCount(t, dir, `SELECT count(*) FROM jobs WHERE id = ? AND state = 'done'`, j.ID); n != 1 {
						t.Errorf("the unfinished job %s is not done in its row", j.ID)
					}
				}
				switch {
				case j.Own:
					if now["maintainer"] != nil || now["account_id"] != nil {
						t.Errorf("the job %s, which labd queued itself, is %v's", j.ID, now["maintainer"])
					}
				case j.Maintainer == "" || j.Maintainer == old.First:
					owners++
					changed = append(changed, "maintainer", "account_id")
					if now["maintainer"] != "owner" || now["account_id"] != float64(ownerID) {
						t.Errorf("the job %s is %v's (account %v), want the owner's", j.ID, now["maintainer"], now["account_id"])
					}
				default:
					if now["maintainer"] != j.Maintainer || now["account_id"] != nil {
						t.Errorf("the job %s of %s is %v's (account %v)", j.ID, j.Maintainer, now["maintainer"], now["account_id"])
					}
				}
				if !reflect.DeepEqual(less(now, changed...), less(was, changed...)) {
					t.Errorf("the job %s changed beyond %v:\n was %v\n now %v", j.ID, changed, was, now)
				}
			}
			if up.JobsOwners != owners || up.JobsEnded != ended || ended != 2 {
				t.Errorf("said %d jobs the owner's and %d ended, found %d and %d", up.JobsOwners, up.JobsEnded, owners, ended)
			}

			afterBuilds := oldRows(t, dir, "builds")
			if len(afterBuilds) != len(builds) {
				t.Errorf("%d builds, want %d", len(afterBuilds), len(builds))
			}
			ended = 0
			for _, b := range old.Builds {
				was, now := builds[b.ID], afterBuilds[b.ID]
				if now == nil {
					t.Errorf("the build %s is gone", b.ID)
					continue
				}
				changed := []string{"asked_by", "asked_by_id"}
				if now["asked_by"] != "owner" || now["asked_by_id"] != float64(ownerID) {
					t.Errorf("the build %s was asked by %v (account %v), want the owner", b.ID, now["asked_by"], now["asked_by_id"])
				}
				if b.Unfinished {
					ended++
					changed = append(changed, "state", "error", "updated")
					if now["state"] != string(api.BuildCanceled) || now["error"] == nil {
						t.Errorf("the unfinished build %s: %v %v", b.ID, now["state"], now["error"])
					}
					if n := oldCount(t, dir, `SELECT count(*) FROM builds WHERE id = ? AND state = ?`, b.ID, string(api.BuildCanceled)); n != 1 {
						t.Errorf("the unfinished build %s is not ended in its row", b.ID)
					}
				}
				if !reflect.DeepEqual(less(now, changed...), less(was, changed...)) {
					t.Errorf("the build %s changed beyond %v:\n was %v\n now %v", b.ID, changed, was, now)
				}
			}
			if up.BuildsOwners != len(builds) || up.BuildsEnded != ended || ended != 2 {
				t.Errorf("said %d builds the owner's and %d ended, found %d and %d", up.BuildsOwners, up.BuildsEnded, len(builds), ended)
			}

			// No credential of the old lab is left, and no other file changed.
			for _, table := range []string{"tokens", "maintainers", "builders"} {
				if n := oldCount(t, dir, `SELECT count(*) FROM `+table); n != 0 {
					t.Errorf("%d rows left in %s", n, table)
				}
			}
			for _, r := range old.Records {
				if n := oldCount(t, dir, `SELECT count(*) FROM ended WHERE secret_hash = ? AND name = ? AND maintainer = 'owner'`, hashSecret(r.Secret), r.Name); n != 1 {
					t.Errorf("the device secret of %s is not kept as ended by the owner", r.Name)
				}
			}
			for _, f := range []string{"admin.token", "builder.token"} {
				if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
					t.Errorf("%s is still there: %v", f, err)
				}
			}
			if !reflect.DeepEqual(up.TokenFiles, []string{"admin.token", "builder.token"}) || up.EnrollTokens != 1 {
				t.Errorf("said token files %v and %d enrollment tokens", up.TokenFiles, up.EnrollTokens)
			}
			if shape == oldlabtest.AtFork && (up.Maintainers != 2 || up.Builders != 1) {
				t.Errorf("said %d Maintainers' tokens and %d builders' tokens", up.Maintainers, up.Builders)
			}
			if got := oldlabtest.Files(t, dir); !reflect.DeepEqual(got, files) {
				t.Errorf("the directory's other files changed: %d before, %d after", len(files), len(got))
			}
			if n := oldCount(t, dir, `SELECT count(*) FROM people WHERE account_id = ? AND handle = 'owner' AND maintainer = 0 AND last_seen IS NULL`, ownerID); n != 1 {
				t.Errorf("the owner is not a person the lab knows, with no mark and never seen")
			}
		})
	}
}

// labd on the upgraded directory: no Mac in its fleet, the old jobs and
// builds there for a Maintainer with the owner as who asked, nothing queued,
// and nothing the old lab handed out opens anything.
func TestALabdOnTheOldLabsDataHasNoMacAndHonoursNothingOfTheOldLab(t *testing.T) {
	for name, shape := range oldLabShapes {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			old := oldlabtest.Make(t, dir, shape)
			if _, err := UpgradeOldLab(dir, theOwner, importDay, false); err != nil {
				t.Fatal(err)
			}
			// The file come back, as a restore of the old directory would bring it.
			if err := os.WriteFile(filepath.Join(dir, "admin.token"), []byte(old.AdminToken+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			l := newSignInLab(t, func(c *Config) { c.DataDir = dir })
			owner := l.signedIn(ownerID)

			w := owner.do("GET", "/api/devices", "")
			var fleet []api.Device
			if err := json.Unmarshal(w.Body.Bytes(), &fleet); err != nil || w.Code != 200 || len(fleet) != 0 {
				t.Errorf("the fleet list: %d %s (%v)", w.Code, w.Body, err)
			}
			if w := owner.do("GET", "/macs", ""); w.Code != 200 || strings.Contains(w.Body.String(), "m1pro") {
				t.Errorf("Macs names a Retired record: %d", w.Code)
			}
			for _, r := range old.Records {
				if w := owner.do("GET", "/api/devices/"+r.Name, ""); w.Code != http.StatusNotFound {
					t.Errorf("the Retired record %s answers as a Mac of the fleet: %d", r.Name, w.Code)
				}
				if w := owner.write("POST", "/api/devices/"+r.Name+"/lease", `{"holder":"owner"}`); w.Code != http.StatusNotFound {
					t.Errorf("a Lease on the Retired record %s: %d %s", r.Name, w.Code, w.Body)
				}
			}
			if l.s.dev("m1pro") != nil || l.s.dev("m1pro2") != nil {
				t.Error("labd holds a Retired record as a Mac")
			}
			// Maintainers find both among the Retired Macs, each with its line.
			w = owner.do("GET", "/api/retired", "")
			var kept []RetiredMac
			if err := json.Unmarshal(w.Body.Bytes(), &kept); err != nil || len(kept) != 2 {
				t.Fatalf("the Retired Macs: %d %s (%v)", w.Code, w.Body, err)
			}
			for _, k := range kept {
				if k.How != "import" || k.Device.Lease != nil || k.Device.OOB != nil || k.Device.Facts.Hostname != "omarchy-m1-pro" || len(k.Record) != 1 || !strings.Contains(k.Record[0].Line, "left the lab") {
					t.Errorf("the Retired record %s: %+v", k.Name, k)
				}
			}

			// The owner is shown as who asked.
			w = owner.do("GET", "/api/jobs/j-000001", "")
			var j api.Job
			if err := json.Unmarshal(w.Body.Bytes(), &j); err != nil || j.Maintainer != "owner" || j.AccountID != ownerID || j.Spec.Device != "m1pro2" {
				t.Errorf("the job j-000001: %d %s (%v)", w.Code, w.Body, err)
			}
			if w := owner.do("GET", "/jobs/j-000001", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "owner") {
				t.Errorf("the page of j-000001 does not show the owner: %d", w.Code)
			}
			if w := owner.do("GET", "/jobs?mac=m1pro2", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "/jobs/j-000") || !strings.Contains(w.Body.String(), "owner") {
				t.Errorf("Jobs does not list the old lab's jobs: %d", w.Code)
			}
			w = owner.do("GET", "/api/builds", "")
			var builds []api.Build
			if err := json.Unmarshal(w.Body.Bytes(), &builds); err != nil || len(builds) == 0 {
				t.Fatalf("the builds: %d %s (%v)", w.Code, w.Body, err)
			}
			for _, b := range builds {
				if b.AskedBy != "owner" || b.AskedByID != ownerID {
					t.Errorf("the build %s was asked by %q (account %d)", b.ID, b.AskedBy, b.AskedByID)
				}
				if b.State == api.BuildQueued || b.State == api.BuildRunning {
					t.Errorf("the build %s still waits for a builder", b.ID)
				}
			}
			if left, err := l.s.store.jobs("", 1000, true); err != nil || len(left) != 0 {
				t.Errorf("%d jobs of the old lab are unfinished in the lab (%v)", len(left), err)
			}

			// Nothing the old lab handed out opens anything.
			tokens := map[string]string{"the admin token": old.AdminToken, "the shared builder token": old.SharedBuilder, "an enrollment token": old.EnrollToken}
			if shape == oldlabtest.AtFork {
				tokens["a Maintainer's token"] = old.OtherToken
				tokens["a builder's token"] = old.BuilderToken
			}
			network := 0
			for what, token := range tokens {
				network++
				for _, target := range []string{"GET /api/devices", "GET /api/jobs", "GET /api/builds", "POST /api/builder/poll", "GET /api/auth"} {
					method, path, _ := strings.Cut(target, " ")
					// A builder's poll is held for a builder labd knows: this
					// one must not wait.
					r := httptest.NewRequest(method, "http://"+labHost+path, nil)
					ctx, cancel := context.WithTimeout(onTunnel(r.Context()), 5*time.Second)
					r = r.WithContext(ctx)
					r.RemoteAddr = "@"
					r.Header.Set("X-Forwarded-Proto", "https")
					r.Header.Set("CF-Connecting-IP", fmt.Sprintf("198.18.%d.7", network))
					r.Header.Set("Authorization", "Bearer "+token)
					w := httptest.NewRecorder()
					l.h.ServeHTTP(w, r)
					cancel()
					if w.Code != http.StatusUnauthorized {
						t.Errorf("%s on %s: %d %s", what, target, w.Code, w.Body)
					}
				}
			}
			body := fmt.Sprintf(`{"token":%q,"facts":{"hostname":"omarchy-m1-pro"},"activation_key":"k"}`, old.EnrollToken)
			r := httptest.NewRequest("POST", "http://"+labHost+"/api/agent/enroll", strings.NewReader(body))
			r = r.WithContext(onTunnel(r.Context()))
			r.RemoteAddr = "@"
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("CF-Connecting-IP", "198.51.100.9")
			r.Header.Set("User-Agent", "lab-agent/0.7.3")
			r.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			l.h.ServeHTTP(rec, r)
			if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
				t.Errorf("the old lab's enrollment token at enrollment: %d %s", rec.Code, rec.Body)
			}
			for i, rec := range old.Records {
				w := l.asToken(rec.Secret, "POST", "/api/agent/checkin", fmt.Sprintf("198.19.%d.10", i))
				if w.Code != http.StatusUnauthorized || w.Header().Get(api.EndedHeader) != "owner" {
					t.Errorf("a check-in with %s's old secret: %d, ended by %q", rec.Name, w.Code, w.Header().Get(api.EndedHeader))
				}
			}
			if n := len(l.s.allDevs()); n != 0 {
				t.Errorf("the lab has %d Macs after all that", n)
			}
		})
	}
}

// A dry run says what the upgrade would do and leaves every byte as it was.
func TestADryRunOfTheUpgradeChangesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	oldlabtest.Make(t, dir, oldlabtest.AtFork)
	before := everyFile(t, dir)
	would, err := UpgradeOldLab(dir, theOwner, importDay, true)
	if err != nil {
		t.Fatal(err)
	}
	if after := everyFile(t, dir); !reflect.DeepEqual(after, before) {
		t.Errorf("a dry run changed the directory: %v", changedFiles(before, after))
	}
	did, err := UpgradeOldLab(dir, theOwner, importDay, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(would, did) {
		t.Errorf("the dry run said\n %+v\nand the upgrade did\n %+v", would, did)
	}
}

func everyFile(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		out[rel] = hashSecret(string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func changedFiles(before, after map[string]string) []string {
	var out []string
	for name, sum := range after {
		if before[name] != sum {
			out = append(out, name)
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			out = append(out, name+" (gone)")
		}
	}
	sort.Strings(out)
	return out
}

// The upgrade is for the old lab's directory, once: it refuses one it has
// upgraded, the lab's own, one with no labd.db, and an owner it cannot name.
func TestTheUpgradeRunsOnceAndOnlyOnTheOldLabsData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	oldlabtest.Make(t, dir, oldlabtest.OnOmarchyGPU)

	for _, p := range []Person{{}, {AccountID: ownerID}, {Handle: "owner"}, {AccountID: -4, Handle: "owner"}, {AccountID: ownerID, Handle: "not a handle"}} {
		before := everyFile(t, dir)
		if _, err := UpgradeOldLab(dir, p, importDay, false); err == nil {
			t.Errorf("the upgrade ran for the owner %+v", p)
		}
		if after := everyFile(t, dir); !reflect.DeepEqual(after, before) {
			t.Errorf("a refused upgrade changed %v", changedFiles(before, after))
		}
	}

	if _, err := UpgradeOldLab(dir, theOwner, importDay, false); err != nil {
		t.Fatal(err)
	}
	for _, dry := range []bool{true, false} {
		before := everyFile(t, dir)
		_, err := UpgradeOldLab(dir, theOwner, importDay.Add(time.Hour), dry)
		if err == nil || !strings.Contains(err.Error(), "already") {
			t.Errorf("a second upgrade (dry run %v): %v", dry, err)
		}
		if after := everyFile(t, dir); !reflect.DeepEqual(after, before) {
			t.Errorf("a second upgrade (dry run %v) changed %v", dry, changedFiles(before, after))
		}
	}

	// The lab's own directory, with a Mac in its fleet.
	l := newLab(t, false)
	live := l.s.cfg.DataDir
	if _, err := UpgradeOldLab(live, theOwner, importDay, false); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("the upgrade ran on the lab's own data: %v", err)
	}
	if l.s.dev("sim-mac") == nil || oldCount(t, live, `SELECT count(*) FROM devices`) != 1 {
		t.Error("the lab's own Mac was Retired")
	}

	empty := t.TempDir()
	if _, err := UpgradeOldLab(empty, theOwner, importDay, false); err == nil {
		t.Error("the upgrade ran on a directory with no labd.db")
	}
	if entries, _ := os.ReadDir(empty); len(entries) != 0 {
		t.Errorf("the upgrade made %d files in a directory with no labd.db", len(entries))
	}
	if _, err := CountOldLab(empty); err == nil {
		t.Error("a directory with no labd.db was counted")
	}
}
