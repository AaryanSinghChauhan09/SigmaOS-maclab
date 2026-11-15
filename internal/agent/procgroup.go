package agent

import (
	"bytes"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// stopGrace is how long the programs a command runs have, once lab-agent
// stops it, between SIGTERM and SIGKILL.
const stopGrace = 5 * time.Second

// runGroup runs cmd, made with exec.CommandContext, as a process group of
// its own, and ends it as one. When cmd's context ends, the group gets
// SIGTERM; cmd.WaitDelay (stopGrace if unset) later its program is killed
// and its pipes are closed, so a child that keeps them open holds nothing
// up. Once the program has ended, whatever is left in its group gets
// SIGTERM (unless it already had one) and, stopGrace after it, SIGKILL. So
// a command never leaves a process behind, and lab-agent's Run, which waits
// for its commands, ends in a bounded time.
func runGroup(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	var mu sync.Mutex
	var termed time.Time
	cmd.Cancel = func() error {
		mu.Lock()
		termed = time.Now()
		mu.Unlock()
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = stopGrace
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	err := cmd.Wait()
	mu.Lock()
	t := termed
	mu.Unlock()
	endGroup(cmd.Process.Pid, t)
	return err
}

// combinedGroup is cmd.CombinedOutput through runGroup.
func combinedGroup(cmd *exec.Cmd) ([]byte, error) {
	var b bytes.Buffer
	cmd.Stdout, cmd.Stderr = &b, &b
	err := runGroup(cmd)
	return b.Bytes(), err
}

// endGroup ends what is left of process group pg: SIGTERM, unless it had
// one at termed, then SIGKILL once stopGrace has passed since.
func endGroup(pg int, termed time.Time) {
	if termed.IsZero() {
		if syscall.Kill(-pg, syscall.SIGTERM) != nil {
			return // nothing is left
		}
		termed = time.Now()
	}
	for time.Since(termed) < stopGrace {
		if syscall.Kill(-pg, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	syscall.Kill(-pg, syscall.SIGKILL)
}
