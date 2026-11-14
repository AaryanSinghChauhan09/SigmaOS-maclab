package api

import (
	"fmt"
	"time"
)

// How a line of the Exec record ended.
const (
	ExecRunning = "running" // sent to the Mac, no answer yet
	ExecRan     = "ran"
	ExecRefused = "refused" // labd refused it: nothing reached the Mac
	ExecLost    = "lost"    // sent to the Mac, and its answer never came
)

// ExecEvent is one line of the Exec record: one lab exec, or one refused
// attempt, with the handle the person had then. It never holds output.
type ExecEvent struct {
	Time      time.Time `json:"time"`
	Mac       string    `json:"mac"`
	AccountID int64     `json:"account_id,omitempty"`
	Handle    string    `json:"handle"`
	Command   string    `json:"command"`
	State     string    `json:"state"`
	// ExitCode and Seconds are the command's own, once it ran. For a
	// command whose answer never came, Seconds is how long labd waited.
	ExitCode *int    `json:"exit_code,omitempty"`
	Seconds  float64 `json:"seconds,omitempty"`
	// Why is why labd refused it, or why no answer came.
	Why string `json:"why,omitempty"`
}

// Line is the event as one line of a record.
func (e ExecEvent) Line() string {
	out := "@" + e.Handle + " on " + e.Mac + ": " + e.Command
	switch e.State {
	case ExecRan:
		if e.ExitCode != nil {
			out += fmt.Sprintf(" (exit %d, %.1f s)", *e.ExitCode, e.Seconds)
		}
	case ExecRefused:
		out += " (refused: " + e.Why + ")"
	case ExecLost:
		out += fmt.Sprintf(" (no answer after %.1f s: %s)", e.Seconds, e.Why)
	default:
		out += " (running)"
	}
	return out
}
