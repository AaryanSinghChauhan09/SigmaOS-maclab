// Package oldlabtest makes a data directory as the old lab's labd kept it,
// for tests of its move to the mini. Nothing here is the real one: every
// token, secret and record is made up.
//
// The format is maclab's at the fork (the hub's main, a5d8134): the tables
// are internal/server/store.go's openStore there, statement for statement,
// and each row's data is the JSON its api types wrote. The labd that ran
// on omarchy-gpu was older than that: it had no named Maintainers, builders
// or ended enrollments, and its tokens table had only its first columns.
package oldlabtest

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Shape is which labd made the directory.
type Shape int

const (
	// AtFork is maclab's labd at the fork: every table it makes.
	AtFork Shape = iota
	// OnOmarchyGPU is the older labd of the old lab itself: one admin
	// token, nobody named.
	OnOmarchyGPU
)

// The counts are the old lab's when the spec was written.
const (
	Records = 2
	Jobs    = 253
	Builds  = 90
)

// maclab's own name for the jobs it queued itself.
const ownHolder = "maclab-omt"

const tablesOnOmarchyGPU = `
CREATE TABLE IF NOT EXISTS devices (id TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL, secret_hash TEXT NOT NULL, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS tokens (token_hash TEXT PRIMARY KEY, name TEXT NOT NULL, label TEXT, model TEXT, expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS jobs (id TEXT PRIMARY KEY, device TEXT NOT NULL, created INTEGER NOT NULL, state TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS jobs_device ON jobs(device, created);
CREATE TABLE IF NOT EXISTS builds (id TEXT PRIMARY KEY, key TEXT NOT NULL, created INTEGER NOT NULL, state TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS builds_key ON builds(key, created);
CREATE TABLE IF NOT EXISTS configs (device TEXT NOT NULL, kernel TEXT NOT NULL, sha TEXT NOT NULL, PRIMARY KEY(device, kernel));`

const tablesAtFork = `
CREATE TABLE IF NOT EXISTS maintainers (name TEXT PRIMARY KEY, token_hash TEXT UNIQUE NOT NULL, added INTEGER NOT NULL, added_by TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS ended (secret_hash TEXT PRIMARY KEY, name TEXT NOT NULL, maintainer TEXT NOT NULL, at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS builders (name TEXT PRIMARY KEY, gen TEXT UNIQUE NOT NULL, token_hash TEXT UNIQUE NOT NULL, kinds TEXT NOT NULL, added INTEGER NOT NULL, added_by TEXT NOT NULL);
ALTER TABLE tokens ADD COLUMN volunteer INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tokens ADD COLUMN requester TEXT NOT NULL DEFAULT '';
ALTER TABLE tokens ADD COLUMN volunteer_name TEXT NOT NULL DEFAULT '';
ALTER TABLE tokens ADD COLUMN contact TEXT NOT NULL DEFAULT '';
ALTER TABLE tokens ADD COLUMN activated_by TEXT NOT NULL DEFAULT '';
ALTER TABLE tokens ADD COLUMN volunteer_login TEXT NOT NULL DEFAULT '';`

// Record is one Mac's record as the old lab kept it.
type Record struct {
	ID, Name string
	// Secret is the device secret its Mac checked in with.
	Secret string
	// Leased is whether the record still carries a Lease.
	Leased bool
}

// Job is one job of the old lab.
type Job struct {
	ID, Device string
	// Unfinished is whether labd stopped before the job was done.
	Unfinished bool
	// Maintainer is the name the old lab recorded with the job: "" for one
	// from before it named anybody, and for a job it queued itself.
	Maintainer string
	// Own is whether labd queued the job itself.
	Own bool
}

// Build is one build of the old lab.
type Build struct {
	ID         string
	Unfinished bool
}

// Lab is the made-up old lab and what a test may ask of it.
type Lab struct {
	Dir   string
	Shape Shape
	// AdminToken is what admin.token holds; at the fork it is also the
	// first Maintainer's token.
	AdminToken string
	// First is the Maintainer the admin token became at the fork, Other one
	// they added, with a token of their own. Both are "" on omarchy-gpu.
	First, Other string
	OtherToken   string
	// BuilderToken is a named builder's token (at the fork), SharedBuilder
	// what builder.token holds.
	BuilderToken, SharedBuilder string
	// EnrollToken is an enrollment token nobody used.
	EnrollToken string
	Records     []Record
	Jobs        []Job
	Builds      []Build
	// Field is a key of every record, job and build that no labd of today
	// reads, and Value what it holds.
	Field, Value string
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func at(day, hour, minute int) time.Time {
	return time.Date(2026, time.October, day, hour, minute, 0, 0, time.UTC)
}

// Make writes the old lab's data directory at dir, which it makes.
func Make(t testing.TB, dir string, shape Shape) *Lab {
	t.Helper()
	l := &Lab{
		Dir: dir, Shape: shape,
		AdminToken:    "mnt_0000000000000000000000000000000000000001",
		SharedBuilder: "bld_0000000000000000000000000000000000000002",
		EnrollToken:   "enr_0000000000000000000000000000000000000003",
		Field:         "kept_by_the_old_lab", Value: "as it was",
	}
	if shape == AtFork {
		l.First, l.Other = "marcelo", "ryan"
		l.OtherToken = "mnt_0000000000000000000000000000000000000004"
		l.BuilderToken = "bld_0000000000000000000000000000000000000005"
	}
	for _, d := range []string{"artifacts", "jobs", "builds", "serial", "screens/m1pro", "baselines", "audit", "frozen"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "labd.db")+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("the old lab's labd.db: %v\n%s", err, q)
		}
	}
	exec(tablesOnOmarchyGPU)
	if shape == AtFork {
		exec(tablesAtFork)
	}

	l.Records = []Record{
		{ID: "d-3377db835766", Name: "m1pro", Secret: "dev_00000000000000000000000000000000000000a1", Leased: true},
		{ID: "d-6bf346f0ac99", Name: "m1pro2", Secret: "dev_00000000000000000000000000000000000000a2"},
	}
	for i, r := range l.Records {
		d := map[string]any{
			"id": r.ID, "name": r.Name, "label": "MacBook Pro (14-inch, M1 Pro, 2021)", "model": "laptop",
			"oob":   map[string]any{"driver": "oobd", "url": "http://100.125.212.71:7780", "token": "oob_00000000000000000000000000000000000000b" + fmt.Sprint(i)},
			"state": "offline", "state_reason": "no heartbeat since 17:38:02",
			"last_seen": at(13+i*10, 17, 38), "boot_id": "boot-" + r.Name, "kernel": "6.17.7-asahi", "cmdline": "root=/dev/mapper/root rw",
			"known_good": "6.17.7-asahi",
			"facts": map[string]any{"hostname": "omarchy-m1-pro", "machine": "aarch64", "dt_model": "Apple MacBook Pro (14-inch, M1 Pro, 2021)",
				"compatible": []string{"apple,j314s", "apple,t6000"}, "bootloader": "limine", "initramfs": "mkinitcpio",
				"watchdog": true, "watchdog_armed": true, "panic_timeout": 10, "agent_version": "0.7.3", "preflight_ok": true},
			"health":            map[string]any{"uptime": 4211.5, "system_state": "running", "tainted": 0},
			"snapshot_eligible": false,
			l.Field:             l.Value,
		}
		if shape == AtFork {
			d["kind"] = "lab-owned"
		}
		if r.Leased {
			lease := map[string]any{"holder": "marcelo-agent", "expires": at(13, 21, 38)}
			if shape == AtFork {
				lease["maintainer"] = l.First
			}
			d["lease"] = lease
			d["active_job"] = "j-000252"
		}
		exec(`INSERT INTO devices(id,name,secret_hash,data) VALUES(?,?,?,?)`, r.ID, r.Name, hash(r.Secret), doc(t, d))
		l.file(t, "serial/"+r.Name+".log", "[    0.000000] Booting Linux on physical CPU 0x0\n")
		l.file(t, "baselines/"+r.Name+".omt.json", `{"checks":[]}`+"\n")
	}
	l.file(t, "screens/m1pro/20261013-173000.png", "\x89PNG\r\n")

	for i := 1; i <= Jobs; i++ {
		j := Job{ID: fmt.Sprintf("j-%06d", i), Device: l.Records[i%2].Name}
		created := at(1+i%14, i%24, i%60)
		state, outcome, summary := "done", "pass", "booted and ran its tests"
		switch i {
		case 249:
			state, outcome, summary, j.Unfinished = "queued", "", "", true
		case 252:
			state, outcome, summary, j.Unfinished = "testing", "", "", true
		}
		spec := map[string]any{"device": j.Device, "kernel": hash(fmt.Sprint("kernel", i%7)), "tests": []any{map[string]any{"kind": "boot"}}}
		switch {
		case i%10 == 0:
			j.Own = true
			spec["holder"] = ownHolder
		case shape == AtFork && i%7 == 0:
			j.Maintainer = l.Other
		case shape == AtFork && i%3 != 0:
			j.Maintainer = l.First
		}
		d := map[string]any{
			"id": j.ID, "spec": spec, "state": state, "created": created, "updated": created.Add(9 * time.Minute),
			"events": []any{map[string]any{"time": created, "state": "queued", "msg": "queued"}},
			"result": map[string]any{"booted": state == "done"},
			l.Field:  l.Value,
		}
		if outcome != "" {
			d["outcome"], d["summary"] = outcome, summary
		}
		if j.Maintainer != "" {
			d["maintainer"] = j.Maintainer
		}
		if shape == AtFork {
			for _, r := range l.Records {
				if r.Name == j.Device {
					d["device_id"] = r.ID
				}
			}
		}
		exec(`INSERT INTO jobs(id,device,created,state,data) VALUES(?,?,?,?,?)`, j.ID, j.Device, created.UnixNano(), state, doc(t, d))
		if i%25 == 0 {
			l.file(t, "jobs/"+j.ID+"/boot0/kernel.txt", "smp: Brought up 1 node, 10 CPUs\n")
		}
		l.Jobs = append(l.Jobs, j)
	}

	for i := 1; i <= Builds; i++ {
		b := Build{ID: fmt.Sprintf("b-%06d", i)}
		created := at(1+i%14, i%24, i%60)
		state := "done"
		switch i {
		case 88:
			state, b.Unfinished = "queued", true
		case 89:
			state, b.Unfinished = "running", true
		case 87:
			state = "failed"
		}
		key := hash(fmt.Sprint("build", i))
		d := map[string]any{
			"id": b.ID, "key": key, "source": map[string]any{"url": "https://github.com/AsahiLinux/linux", "ref": "asahi-wip", "commit": hash(fmt.Sprint("commit", i))[:40]},
			"device": "m1pro", "config_sha": hash("config"), "state": state, "created": created, "updated": created.Add(20 * time.Minute),
			"log_lines": 12 + i,
			l.Field:     l.Value,
		}
		if state == "done" {
			d["artifact"], d["size"] = hash(fmt.Sprint("kernel", i%7)), 1024
		}
		exec(`INSERT INTO builds(id,key,created,state,data) VALUES(?,?,?,?,?)`, b.ID, key, created.UnixNano(), state, doc(t, d))
		if i%30 == 0 {
			l.file(t, "builds/"+b.ID+"/log", "CC      init/main.o\n")
		}
		l.Builds = append(l.Builds, b)
	}
	for i := range 7 {
		l.file(t, "artifacts/"+hash(fmt.Sprint("kernel", i)), fmt.Sprint("a kernel image ", i, "\n"))
	}
	exec(`INSERT INTO configs(device,kernel,sha) VALUES('m1pro','6.17.7-asahi',?)`, hash("config"))

	later := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()
	exec(`INSERT INTO tokens(token_hash,name,label,model,expires) VALUES(?,?,?,?,?)`, hash(l.EnrollToken), "m2max", "a Mac that never came", "laptop", later)
	if shape == AtFork {
		exec(`INSERT INTO maintainers(name,token_hash,added,added_by) VALUES(?,?,?,'')`, l.First, hash(l.AdminToken), at(1, 9, 0).Unix())
		exec(`INSERT INTO maintainers(name,token_hash,added,added_by) VALUES(?,?,?,?)`, l.Other, hash(l.OtherToken), at(2, 9, 0).Unix(), l.First)
		exec(`INSERT INTO builders(name,gen,token_hash,kinds,added,added_by) VALUES('gpu','g1',?,'kernel,package',?,?)`, hash(l.BuilderToken), at(2, 10, 0).Unix(), l.First)
		exec(`INSERT INTO ended(secret_hash,name,maintainer,at) VALUES(?,'an-old-mac',?,?)`, hash("dev_00000000000000000000000000000000000000a0"), l.First, at(3, 9, 0).Unix())
		l.file(t, "audit/m1pro.jsonl", `{"action":"exec"}`+"\n")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	l.secret(t, "admin.token", l.AdminToken)
	l.secret(t, "builder.token", l.SharedBuilder)
	return l
}

func doc(t testing.TB, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (l *Lab) file(t testing.TB, name, text string) {
	t.Helper()
	path := filepath.Join(l.Dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (l *Lab) secret(t testing.TB, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(l.Dir, name), []byte(text+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Files is every file under dir but the database's own and the two token
// files, by its path there, with the SHA-256 of what it holds.
func Files(t testing.TB, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		switch rel {
		case "labd.db", "labd.db-wal", "labd.db-shm", "admin.token", "builder.token":
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = hash(string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Names are the Mac records' names, in order.
func (l *Lab) Names() []string {
	var out []string
	for _, r := range l.Records {
		out = append(out, r.Name)
	}
	sort.Strings(out)
	return out
}
