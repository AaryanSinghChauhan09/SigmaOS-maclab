package server

// labd tells two Signal groups what happens, a line for each event: the Lab
// group (Signal group "Omarchy Mac Lab") and the owner's group "Agents".
// Every line is recorded in labd.db first and then handed to the lab's
// sender. Recording never waits for Signal, and a send that fails holds
// nothing up. A line goes at most once: one that may have left is not sent
// again. A line recorded while its group has no id is kept and not posted.

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
)

// lineGroup is one of the two groups, as the lines table names it.
type lineGroup string

const (
	lineLab    lineGroup = "lab"
	lineAgents lineGroup = "agents"
)

// groupFiles are the files of the --signal folder that hold each group's
// id. A group with no file gets nothing.
var groupFiles = map[lineGroup]string{
	lineLab:    "signal-lab-group",
	lineAgents: "signal-agents-group",
}

const (
	// lineGiveUp is how long labd keeps trying a line signal-cli could not
	// be reached for, as when both start together.
	lineGiveUp  = time.Hour
	sendTimeout = 30 * time.Second
)

var lineRetry = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute}

type groupLines struct {
	st     *store
	clock  agent.Clock
	log    *log.Logger
	sender Sender // nil records every line and posts none
	dir    string
	wake   chan struct{}
}

func newGroupLines(st *store, clock agent.Clock, logger *log.Logger, sender Sender, dir string) *groupLines {
	return &groupLines{st: st, clock: clock, log: logger, sender: sender, dir: dir, wake: make(chan struct{}, 1)}
}

// groupID is the id the owner gave labd for the group, "" for none.
func groupID(dir string, g lineGroup) string {
	if dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, groupFiles[g]))
	if err != nil {
		return ""
	}
	if f := strings.Fields(string(b)); len(f) == 1 {
		return f[0]
	}
	return ""
}

// post records one line for the group and leaves the sending to run.
func (l *groupLines) post(g lineGroup, text string) {
	l.record(g, oneLine(text))
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func (l *groupLines) record(g lineGroup, text string) {
	state := "not-posted"
	if l.sender != nil && groupID(l.dir, g) != "" {
		state = "waiting"
	}
	if _, err := l.st.db.Exec(`INSERT INTO lines (at, channel, text, state) VALUES (?, ?, ?, ?)`, l.clock.Now().UnixMicro(), string(g), text, state); err != nil {
		l.log.Printf("a line for the %s group was not recorded: %v", g, err)
		return
	}
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// markOnce notes value under key and says whether that changed anything.
func (l *groupLines) markOnce(key, value string) bool {
	res, err := l.st.db.Exec(`INSERT INTO line_marks (key, value, at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, at = excluded.at WHERE value != excluded.value`, key, value, l.clock.Now().UnixMicro())
	if err != nil {
		l.log.Printf("line mark %s: %v", key, err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// mark is the note under key.
func (l *groupLines) mark(key string) (value string, ok bool) {
	value, _, ok = l.markAt(key)
	return value, ok
}

// markAt is the note under key and when it was made.
func (l *groupLines) markAt(key string) (value string, at time.Time, ok bool) {
	var micro int64
	if err := l.st.db.QueryRow(`SELECT value, at FROM line_marks WHERE key = ?`, key).Scan(&value, &micro); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			l.log.Printf("line mark %s: %v", key, err)
		}
		return "", time.Time{}, false
	}
	return value, time.UnixMicro(micro), true
}

// setMark notes value under key, whatever was there.
func (l *groupLines) setMark(key, value string) {
	if _, err := l.st.db.Exec(`INSERT INTO line_marks (key, value, at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, at = excluded.at`, key, value, l.clock.Now().UnixMicro()); err != nil {
		l.log.Printf("line mark %s: %v", key, err)
	}
}

// unmark drops the note under key and says whether there was one.
func (l *groupLines) unmark(key string) bool {
	res, err := l.st.db.Exec(`DELETE FROM line_marks WHERE key = ?`, key)
	if err != nil {
		l.log.Printf("line mark %s: %v", key, err)
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

func (l *groupLines) settle(id int64, state, why string) {
	if _, err := l.st.db.Exec(`UPDATE lines SET state = ?, error = ?, settled = ? WHERE id = ?`, state, why, l.clock.Now().UnixMicro(), id); err != nil {
		l.log.Printf("line %d: %v", id, err)
	}
}

// run sends the waiting lines, oldest first, until ctx ends.
func (l *groupLines) run(ctx context.Context) {
	if _, err := l.st.db.Exec(`UPDATE lines SET state = 'failed', error = 'labd stopped while handing it to signal-cli: it may have gone, and is not sent again', settled = ? WHERE state = 'sending'`, l.clock.Now().UnixMicro()); err != nil && ctx.Err() == nil {
		l.log.Printf("lines: %v", err)
	}
	for ctx.Err() == nil {
		wait, more := l.sendNext(ctx)
		switch {
		case wait > 0:
			select {
			case <-ctx.Done():
			case <-l.clock.After(wait):
			}
		case !more:
			select {
			case <-ctx.Done():
			case <-l.wake:
			}
		}
	}
}

// sendNext sends the oldest waiting line. It returns how long to wait before
// trying that line again, and whether there was a line at all.
func (l *groupLines) sendNext(ctx context.Context) (wait time.Duration, more bool) {
	var (
		id       int64
		at       int64
		channel  string
		text     string
		lastErr  string
		attempts = 0
	)
	err := l.st.db.QueryRow(`SELECT id, at, channel, text, error FROM lines WHERE state = 'waiting' ORDER BY id LIMIT 1`).Scan(&id, &at, &channel, &text, &lastErr)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
			l.log.Printf("lines: %v", err)
			return lineRetry[len(lineRetry)-1], true
		}
		return 0, false
	}
	age := l.clock.Now().Sub(time.UnixMicro(at))
	if lastErr != "" {
		if age >= lineGiveUp {
			l.log.Printf("Signal: line %d for the %s group was given up: %s", id, channel, lastErr)
			l.settle(id, "failed", lastErr)
			return 0, true
		}
		for attempts < len(lineRetry)-1 && age > lineRetry[attempts] {
			attempts++
		}
	}
	// A line an earlier labd left waiting is not posted by one that has no
	// sender, or no id for its group any more.
	group := groupID(l.dir, lineGroup(channel))
	if group == "" || l.sender == nil {
		l.settle(id, "not-posted", "")
		return 0, true
	}
	if _, err := l.st.db.Exec(`UPDATE lines SET state = 'sending' WHERE id = ?`, id); err != nil {
		l.log.Printf("line %d: %v", id, err)
		return lineRetry[len(lineRetry)-1], true
	}
	sctx, cancel := context.WithTimeout(ctx, sendTimeout)
	err = l.sender.Send(sctx, group, text)
	cancel()
	switch {
	case err == nil:
		l.settle(id, "sent", "")
	case errors.Is(err, ErrNotReached):
		if lastErr == "" {
			l.log.Printf("Signal: line %d for the %s group waits: %v", id, channel, err)
		}
		if _, uerr := l.st.db.Exec(`UPDATE lines SET state = 'waiting', error = ? WHERE id = ?`, err.Error(), id); uerr != nil {
			l.log.Printf("line %d: %v", id, uerr)
		}
		return lineRetry[attempts], true
	default:
		l.log.Printf("Signal: line %d for the %s group was not sent: %v", id, channel, err)
		l.settle(id, "failed", err.Error())
	}
	return 0, true
}
