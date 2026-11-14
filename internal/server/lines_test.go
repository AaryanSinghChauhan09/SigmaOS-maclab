package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
)

type sentLine struct{ group, text string }

// fakeSignal stands in for signal-cli: it writes down what would be sent.
type fakeSignal struct {
	mu    sync.Mutex
	sent  []sentLine
	tries int
	// answer decides each send by its number, from 1; nil sends every one.
	answer func(try int) error
}

func (f *fakeSignal) Send(_ context.Context, group, text string) error {
	f.mu.Lock()
	f.tries++
	try, answer := f.tries, f.answer
	f.mu.Unlock()
	if answer != nil {
		if err := answer(try); err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.sent = append(f.sent, sentLine{group, text})
	f.mu.Unlock()
	return nil
}

func (f *fakeSignal) lines() []sentLine {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentLine(nil), f.sent...)
}

func (f *fakeSignal) tried() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tries
}

const (
	testLabGroup    = "bGFiLWdyb3VwLWlkLWxhYi1ncm91cC1pZC1sYWItZ3I="
	testAgentsGroup = "YWdlbnRzLWdyb3VwLWlkLWFnZW50cy1ncm91cC1pZC0="
)

// signalDir is a folder as labd's --signal names it, with both group ids.
func signalDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, id := range map[string]string{"signal-lab-group": testLabGroup, "signal-agents-group": testAgentsGroup} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(id+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type linesHarness struct {
	t     *testing.T
	l     *groupLines
	st    *store
	clock *agent.FakeClock
	logs  *logBuf
	stop  context.CancelFunc
}

// newLinesAt starts the record of lines on the labd.db in data, as labd does.
func newLinesAt(t *testing.T, data string, sender Sender, dir string) *linesHarness {
	t.Helper()
	st, err := openStore(filepath.Join(data, "labd.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &linesHarness{t: t, st: st, clock: agent.NewFakeClock(time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)), logs: &logBuf{}, stop: cancel}
	h.l = newGroupLines(st, h.clock, log.New(h.logs, "", 0), sender, dir)
	done := make(chan struct{})
	go func() { h.l.run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; st.db.Close() })
	return h
}

func newLines4Test(t *testing.T, sender Sender, dir string) *linesHarness {
	return newLinesAt(t, t.TempDir(), sender, dir)
}

type recordedLine struct{ channel, text, state, err string }

func (h *linesHarness) recorded() []recordedLine {
	h.t.Helper()
	rows, err := h.st.db.Query(`SELECT channel, text, state, error FROM lines ORDER BY id`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []recordedLine
	for rows.Next() {
		var r recordedLine
		if err := rows.Scan(&r.channel, &r.text, &r.state, &r.err); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// settle waits until no line is waiting or being sent, moving the clock on
// so that a line held back is tried again.
func (h *linesHarness) settle() []recordedLine {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rec := h.recorded()
		busy := false
		for _, r := range rec {
			busy = busy || r.state == "waiting" || r.state == "sending"
		}
		if !busy {
			return rec
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("lines still on their way: %+v", rec)
		}
		h.clock.Advance(time.Minute)
		time.Sleep(5 * time.Millisecond)
	}
}

func TestALineIsRecordedAndSentToItsGroupOnce(t *testing.T) {
	sig := &fakeSignal{}
	data := t.TempDir()
	dir := signalDir(t)
	h := newLinesAt(t, data, sig, dir)
	h.l.post(lineLab, "ada leased m1-pro until Sat 14:30 AEST.")
	h.l.post(lineAgents, "grace marked ada as M team.")
	h.l.post(lineLab, "ada released m1-pro.")
	rec := h.settle()
	want := []sentLine{
		{testLabGroup, "ada leased m1-pro until Sat 14:30 AEST."},
		{testAgentsGroup, "grace marked ada as M team."},
		{testLabGroup, "ada released m1-pro."},
	}
	if got := sig.lines(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
	for _, r := range rec {
		if r.state != "sent" || r.err != "" {
			t.Errorf("recorded %+v, want it sent", r)
		}
	}
	if len(rec) != 3 {
		t.Fatalf("%d lines recorded, want 3", len(rec))
	}

	// labd starts again on the same database: no line goes twice.
	h.stop()
	again := newLinesAt(t, data, sig, dir)
	again.clock.Advance(2 * time.Hour)
	again.settle()
	time.Sleep(50 * time.Millisecond)
	if got := sig.lines(); len(got) != 3 {
		t.Fatalf("after a restart %d lines were sent, want the same 3: %v", len(got), got)
	}
}

func TestAFailedSendLeavesTheLineRecordedAndIsNotTriedAgain(t *testing.T) {
	sig := &fakeSignal{answer: func(try int) error {
		if try == 1 {
			return errors.New("signal-cli: rate limited")
		}
		return nil
	}}
	h := newLines4Test(t, sig, signalDir(t))
	h.l.post(lineLab, "ada released m1-pro.")
	h.l.post(lineLab, "m2-air resumed.")
	rec := h.settle()
	if rec[0].state != "failed" || !strings.Contains(rec[0].err, "rate limited") || rec[0].text != "ada released m1-pro." {
		t.Errorf("the failed line is recorded as %+v", rec[0])
	}
	if rec[1].state != "sent" {
		t.Errorf("the line after it is recorded as %+v", rec[1])
	}
	h.clock.Advance(3 * time.Hour)
	time.Sleep(50 * time.Millisecond)
	if got := sig.lines(); len(got) != 1 || got[0].text != "m2-air resumed." || sig.tried() != 2 {
		t.Errorf("sent %v in %d tries: a line signal-cli answered with an error is not sent again", got, sig.tried())
	}
	if !strings.Contains(h.logs.String(), "rate limited") {
		t.Errorf("the failure is not in labd's log: %s", h.logs.String())
	}
}

// Whoever records a line is never held by Signal: not by a send that
// hangs, not by one that fails.
func TestRecordingALineWaitsForNoSend(t *testing.T) {
	release := make(chan struct{})
	sig := &fakeSignal{answer: func(int) error { <-release; return nil }}
	h := newLines4Test(t, sig, signalDir(t))
	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			h.l.post(lineLab, fmt.Sprintf("line %d", i))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recording lines waited for a send that hangs")
	}
	if n := len(h.recorded()); n != 20 {
		t.Errorf("%d lines recorded while the sender hung, want 20", n)
	}
	close(release)
	h.settle()
	if n := len(sig.lines()); n != 20 {
		t.Errorf("%d lines sent, want 20", n)
	}
}

func TestALineThatNeverReachedSignalCLIIsTriedAgainAndGoesOnce(t *testing.T) {
	sig := &fakeSignal{answer: func(try int) error {
		if try <= 3 {
			return fmt.Errorf("%w: dial unix /var/lab/run/signal-cli.sock: connection refused", ErrNotReached)
		}
		return nil
	}}
	h := newLines4Test(t, sig, signalDir(t))
	h.l.post(lineAgents, "grace blocked ada.")
	h.l.post(lineAgents, "grace unblocked ada.")
	rec := h.settle()
	if got := sig.lines(); len(got) != 2 || got[0].text != "grace blocked ada." || got[1].text != "grace unblocked ada." {
		t.Fatalf("sent %v, want both lines once and in order", got)
	}
	if rec[0].state != "sent" || rec[1].state != "sent" {
		t.Errorf("recorded %+v", rec)
	}
}

func TestALineSignalCLICouldNotBeReachedForWithinAnHourIsGivenUp(t *testing.T) {
	sig := &fakeSignal{answer: func(int) error { return fmt.Errorf("%w: no socket", ErrNotReached) }}
	h := newLines4Test(t, sig, signalDir(t))
	h.l.post(lineLab, "ada released m1-pro.")
	rec := h.settle()
	if rec[0].state != "failed" || !strings.Contains(rec[0].err, "no socket") {
		t.Fatalf("recorded %+v, want it given up with the last error", rec[0])
	}
	if at := h.clock.Now().Sub(time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)); at < time.Hour || at > 2*time.Hour {
		t.Errorf("given up after %s, want an hour", at)
	}
	tries := sig.tried()
	h.clock.Advance(time.Hour)
	time.Sleep(50 * time.Millisecond)
	if sig.tried() != tries {
		t.Error("a line given up was tried again")
	}
}

func TestWithNoGroupIDLinesAreRecordedAndNothingIsSent(t *testing.T) {
	sig := &fakeSignal{}
	dir := t.TempDir()
	h := newLines4Test(t, sig, dir)
	h.l.post(lineLab, "ada released m1-pro.")
	h.l.post(lineAgents, "grace blocked ada.")
	rec := h.settle()
	if len(rec) != 2 || rec[0].state != "not-posted" || rec[1].state != "not-posted" {
		t.Fatalf("recorded %+v, want both lines kept and not posted", rec)
	}
	if sig.tried() != 0 {
		t.Fatalf("with no group id %d sends were tried", sig.tried())
	}

	// The owner gives labd the Lab group's id: lines from then on go, to
	// that group alone, and what was recorded before stays unsent.
	if err := os.WriteFile(filepath.Join(dir, "signal-lab-group"), []byte(testLabGroup+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.l.post(lineLab, "m2-air resumed.")
	h.l.post(lineAgents, "grace unblocked ada.")
	rec = h.settle()
	if got := sig.lines(); len(got) != 1 || got[0] != (sentLine{testLabGroup, "m2-air resumed."}) {
		t.Errorf("sent %v, want the one new line of the Lab group", got)
	}
	if rec[0].state != "not-posted" || rec[2].state != "sent" || rec[3].state != "not-posted" {
		t.Errorf("recorded %+v", rec)
	}
}

func TestWithNoSenderLinesAreRecordedAndNotPosted(t *testing.T) {
	h := newLines4Test(t, nil, signalDir(t))
	h.l.post(lineLab, "ada released m1-pro.")
	if rec := h.settle(); len(rec) != 1 || rec[0].state != "not-posted" {
		t.Fatalf("recorded %+v", rec)
	}
}

// A line labd was handing to signal-cli when it stopped may have gone:
// it is not sent a second time.
func TestALineLabdStoppedWhileSendingIsNotSentAgain(t *testing.T) {
	data := t.TempDir()
	st, err := openStore(filepath.Join(data, "labd.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`INSERT INTO lines (at, channel, text, state) VALUES (?, 'lab', 'ada released m1-pro.', 'sending')`, time.Now().UnixMicro()); err != nil {
		t.Fatal(err)
	}
	st.db.Close()
	sig := &fakeSignal{}
	h := newLinesAt(t, data, sig, signalDir(t))
	rec := h.settle()
	if rec[0].state != "failed" || rec[0].err == "" || sig.tried() != 0 {
		t.Fatalf("recorded %+v after %d tries", rec[0], sig.tried())
	}
}

func TestALineIsOneLine(t *testing.T) {
	sig := &fakeSignal{}
	h := newLines4Test(t, sig, signalDir(t))
	h.l.post(lineAgents, "ada made the Lab token \"lap\ntop\r\n  two\", which never expires.")
	h.settle()
	if got := sig.lines(); len(got) != 1 || got[0].text != `ada made the Lab token "lap top two", which never expires.` {
		t.Fatalf("sent %q", got)
	}
}

// "Agents" hears of a trouble once and of its end once, however often labd
// looks, and whether or not labd restarted in between.
func TestATroubleIsToldOnceAndItsEndOnce(t *testing.T) {
	sig := &fakeSignal{}
	data := t.TempDir()
	dir := signalDir(t)
	h := newLinesAt(t, data, sig, dir)
	a := agentsLines{h.l}
	a.Recovered("tunnel", "The tunnel is connected again.")
	for i := 0; i < 3; i++ {
		a.Trouble("tunnel", "The tunnel has been disconnected for over 5 minutes.")
	}
	a.Trouble("disk", "Disk under 15 %.")
	h.settle()
	h.stop()
	h = newLinesAt(t, data, sig, dir)
	a = agentsLines{h.l}
	a.Trouble("tunnel", "The tunnel has been disconnected for over 5 minutes.")
	a.Recovered("tunnel", "The tunnel is connected again.")
	a.Recovered("tunnel", "The tunnel is connected again.")
	a.Trouble("tunnel", "The tunnel has been disconnected for over 5 minutes.")
	h.settle()
	var got []string
	for _, l := range sig.lines() {
		if l.group != testAgentsGroup {
			t.Errorf("%q went to %s", l.text, l.group)
		}
		got = append(got, l.text)
	}
	want := []string{
		"The tunnel has been disconnected for over 5 minutes.",
		"Disk under 15 %.",
		"The tunnel is connected again.",
		"The tunnel has been disconnected for over 5 minutes.",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("\"Agents\" got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
