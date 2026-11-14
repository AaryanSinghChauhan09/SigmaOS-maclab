package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/sqlitedb"
)

const (
	senderNumber = "+61491570156"
	memberNumber = "+61491570157"
	// heard is a message a member wrote in the group, as signal-cli hands
	// it to whoever is connected.
	heard = `{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"` + memberNumber + `","sourceNumber":"` + memberNumber + `","sourceName":"Ada","timestamp":1,"dataMessage":{"message":"the launch code is tangerine","groupInfo":{"groupId":"` + testLabGroup + `"}}},"account":"` + senderNumber + `"}}`
)

// fakeSignalCLI plays signal-cli's daemon socket: it answers listAccounts
// and send, writes down what it was asked, and before every answer passes
// on a message it "received", as the real one does.
type fakeSignalCLI struct {
	socket string
	mu     sync.Mutex
	asked  []rpcRequest
	// accounts is the result of listAccounts.
	accounts string
	// send answers the nth send, from 1: a JSON member such as
	// `"result":{"timestamp":1}`; "" hangs up without an answer.
	send func(n int) string
	sent int
}

func newFakeSignalCLI(t *testing.T) *fakeSignalCLI {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "labsig")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeSignalCLI{socket: filepath.Join(dir, "s"), accounts: `[{"number":"` + senderNumber + `"}]`}
	ln, err := net.Listen("unix", f.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSignalCLI) serve(conn net.Conn) {
	defer conn.Close()
	rd := bufio.NewReader(conn)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var req rpcRequest
		if json.Unmarshal(line, &req) != nil {
			return
		}
		f.mu.Lock()
		f.asked = append(f.asked, req)
		answer := `"result":` + f.accounts
		if req.Method == "send" {
			f.sent++
			answer = `"result":{"timestamp":1763089200000,"results":[{"recipientAddress":{"number":"` + memberNumber + `"},"type":"SUCCESS"}]}`
			if f.send != nil {
				answer = f.send(f.sent)
			}
		} else if req.Method != "listAccounts" {
			answer = `"error":{"code":-32601,"message":"Method not implemented"}`
		}
		f.mu.Unlock()
		if answer == "" {
			return
		}
		fmt.Fprintf(conn, "%s\n{\"jsonrpc\":\"2.0\",%s,\"id\":%q}\n", heard, answer, req.ID)
	}
}

func (f *fakeSignalCLI) requests() []rpcRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rpcRequest(nil), f.asked...)
}

func TestTheSenderAsksSignalCLIForItsAccountAndSendsToTheGroup(t *testing.T) {
	f := newFakeSignalCLI(t)
	text := "ada made the Lab token \"läptop 💻\", which never expires.\nMacs: https://lab.omarchy-mac.org/macs"
	if err := NewSignalSender(f.socket).Send(context.Background(), testLabGroup, text); err != nil {
		t.Fatal(err)
	}
	asked := f.requests()
	if len(asked) != 2 || asked[0].Method != "listAccounts" || asked[1].Method != "send" {
		t.Fatalf("signal-cli was asked %+v, want listAccounts and one send", asked)
	}
	want := map[string]any{"account": senderNumber, "groupId": testLabGroup, "message": text}
	if got := asked[1].Params; fmt.Sprint(got) != fmt.Sprint(want) || len(got) != 3 {
		t.Errorf("send was asked with %v, want %v", got, want)
	}
}

// An account without a number goes by its ACI.
func TestTheSenderNamesANumberlessAccountByItsACI(t *testing.T) {
	f := newFakeSignalCLI(t)
	f.accounts = `[{"number":null,"aci":"11111111-1111-4111-8111-111111111111"}]`
	if err := NewSignalSender(f.socket).Send(context.Background(), testAgentsGroup, "grace blocked ada."); err != nil {
		t.Fatal(err)
	}
	if got := f.requests()[1].Params["account"]; got != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("account %v", got)
	}
}

// Only a send that failed before the message was handed over may be tried
// again; and whatever signal-cli says, no phone number comes back.
func TestTheSenderSaysNotReachedOnlyBeforeTheMessageIsHandedOver(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	send := func(f *fakeSignalCLI) error {
		return NewSignalSender(f.socket).Send(ctx, testLabGroup, "ada released m1-pro.")
	}

	err := NewSignalSender(filepath.Join(t.TempDir(), "no-socket")).Send(ctx, testLabGroup, "ada released m1-pro.")
	if !errors.Is(err, ErrNotReached) {
		t.Errorf("no socket: %v, want it not reached", err)
	}

	for accounts, word := range map[string]string{
		`[]`: "not registered",
		`[{"number":"` + senderNumber + `"},{"number":"` + memberNumber + `"}]`: "2 accounts",
	} {
		f := newFakeSignalCLI(t)
		f.accounts = accounts
		err := send(f)
		if !errors.Is(err, ErrNotReached) || !strings.Contains(err.Error(), word) {
			t.Errorf("accounts %s: %v, want it not reached, saying %q", accounts, err, word)
		}
		for _, req := range f.requests() {
			if req.Method == "send" {
				t.Errorf("accounts %s: a send was asked for", accounts)
			}
		}
	}

	f := newFakeSignalCLI(t)
	f.send = func(int) string {
		return `"error":{"code":-1,"message":"Failed to send message to ` + memberNumber + `: rate limited"}`
	}
	err = send(f)
	if err == nil || errors.Is(err, ErrNotReached) || !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("an error from signal-cli: %v, want it final", err)
	}
	if strings.Contains(err.Error(), memberNumber) || strings.Contains(err.Error(), senderNumber) {
		t.Errorf("the error carries a phone number: %v", err)
	}

	f = newFakeSignalCLI(t)
	f.send = func(int) string { return "" }
	if err := send(f); err == nil || errors.Is(err, ErrNotReached) {
		t.Errorf("signal-cli hung up after taking the message: %v, want it final", err)
	}
}

func TestTheTestLineGoesToEachGroupThatHasAnID(t *testing.T) {
	sig := &fakeSignal{}
	dir := t.TempDir()
	var out strings.Builder
	if err := SignalTest(context.Background(), sig, dir, &out); err == nil || sig.tried() != 0 {
		t.Fatalf("with no group id: %v, %d sends\n%s", err, sig.tried(), out.String())
	}
	if err := os.WriteFile(filepath.Join(dir, "signal-agents-group"), []byte(testAgentsGroup+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := SignalTest(context.Background(), sig, dir, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if got := sig.lines(); len(got) != 1 || got[0].group != testAgentsGroup || got[0].text != signalTestLine {
		t.Errorf("sent %v", got)
	}
	if !strings.Contains(out.String(), `"Agents": sent`) || !strings.Contains(out.String(), "signal-lab-group: nothing sent") {
		t.Errorf("said:\n%s", out.String())
	}
	if strings.Contains(out.String(), testAgentsGroup) {
		t.Errorf("the group id was printed:\n%s", out.String())
	}

	failing := &fakeSignal{answer: func(int) error { return errors.New("signal-cli: rate limited") }}
	out.Reset()
	if err := SignalTest(context.Background(), failing, signalDir(t), &out); err == nil || strings.Count(out.String(), "not sent: signal-cli: rate limited") != 2 {
		t.Errorf("failed sends: %v\n%s", err, out.String())
	}
}

// dumpDB is every table of a database as text: its schema and its values.
func dumpDB(t *testing.T, db *sqlitedb.DB) string {
	t.Helper()
	var b strings.Builder
	var tables []string
	rows, err := db.Query(`SELECT name, sql FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, schema string
		if err := rows.Scan(&name, &schema); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
		b.WriteString(schema + "\n")
	}
	rows.Close()
	for _, table := range tables {
		rows, err := db.Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				if raw, ok := v.([]byte); ok {
					v = string(raw)
				}
				fmt.Fprintf(&b, "%v\n", v)
			}
		}
		rows.Close()
	}
	return b.String()
}

var phoneLike = regexp.MustCompile(`\+\d{6,}`)

// The lab only sends. What members write in a group reaches signal-cli and
// goes no further; and neither the sender's number nor a member's is in a
// table or in labd's log.
func TestTheLabReadsNothingFromAGroupAndKeepsNoPhoneNumber(t *testing.T) {
	f := newFakeSignalCLI(t)
	f.send = func(n int) string {
		if n == 2 {
			return `"error":{"code":-1,"message":"Failed to send message to ` + memberNumber + `: untrusted identity"}`
		}
		return `"result":{"timestamp":1763089200000,"results":[{"recipientAddress":{"number":"` + memberNumber + `"},"type":"SUCCESS"}]}`
	}
	dir := signalDir(t)
	l := newLabWith(t, labConfig{config: func(c *Config) {
		c.Signal = NewSignalSender(f.socket)
		c.SignalDir = dir
	}})
	l.s.labGroup.LeaseTaken("ada", "m1-pro", time.Date(2026, 11, 14, 4, 30, 0, 0, time.UTC))
	l.s.agents.Blocked("grace", "ada")
	l.s.agents.LabTokenMade("ada", "laptop", time.Time{})
	l.s.labGroup.MacJoined("m2-air", "", "volunteer")
	deadline := time.Now().Add(10 * time.Second)
	for {
		var busy, sent, failed int
		if err := l.s.store.db.QueryRow(`SELECT count(*) FILTER (WHERE state IN ('waiting', 'sending')), count(*) FILTER (WHERE state = 'sent'), count(*) FILTER (WHERE state = 'failed') FROM lines`).Scan(&busy, &sent, &failed); err != nil {
			t.Fatal(err)
		}
		if busy == 0 {
			// The fourth sent is the line for the lab's own Mac, which Joined.
			if sent != 4 || failed != 1 {
				t.Fatalf("%d lines sent and %d failed, want 4 and 1", sent, failed)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the lines did not go")
		}
		time.Sleep(20 * time.Millisecond)
	}

	for _, req := range f.requests() {
		if req.Method != "listAccounts" && req.Method != "send" {
			t.Errorf("labd asked signal-cli for %s: it only sends", req.Method)
		}
	}
	reports, err := sqlitedb.Open(filepath.Join(l.s.cfg.DataDir, "reports.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reports.Close()
	for name, text := range map[string]string{
		"labd.db":    dumpDB(t, l.s.store.db),
		"reports.db": dumpDB(t, reports),
		"labd's log": l.logs.String(),
	} {
		if strings.Contains(text, "tangerine") || strings.Contains(text, "Ada") {
			t.Errorf("%s holds what a member wrote in the group", name)
		}
		if strings.Contains(text, senderNumber) || strings.Contains(text, memberNumber) || phoneLike.MatchString(text) {
			t.Errorf("%s holds a phone number", name)
		}
		if name != "labd's log" && regexp.MustCompile(`(?i)phone|msisdn|signal_id|\bnumber\b`).MatchString(text) {
			t.Errorf("%s has a column or a value for a phone number or a Signal id", name)
		}
	}
	if !strings.Contains(dumpDB(t, l.s.store.db), "untrusted identity") {
		t.Error("the failed line's reason is not recorded")
	}
}

// While a send hangs, labd goes on serving.
func TestAHangingSendHoldsNoRequest(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	sig := &fakeSignal{answer: func(int) error { <-release; return errors.New("signal-cli: gone") }}
	dir := signalDir(t)
	l := newLabWith(t, labConfig{config: func(c *Config) { c.Signal, c.SignalDir = sig, dir }})
	l.s.labGroup.LeaseTaken("ada", "sim-mac", time.Now().Add(4*time.Hour))
	l.s.agents.Blocked("grace", "ada")
	if r, body := l.get("GET", "/api/devices/sim-mac"); r.StatusCode != 200 {
		t.Fatalf("with a send hanging: %s %s", r.Status, body)
	}
	var n int
	// Two, and the line for the lab's own Mac, which Joined.
	if err := l.s.store.db.QueryRow(`SELECT count(*) FROM lines`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("%d lines recorded (%v), want 3", n, err)
	}
}
