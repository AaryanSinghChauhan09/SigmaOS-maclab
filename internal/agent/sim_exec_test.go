package agent

import (
	"context"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

func TestTheSimulatedMacSleepsThroughASilentCommand(t *testing.T) {
	sim := NewSim(t.TempDir())
	start := time.Now()
	res := sim.Exec(context.Background(), api.ExecArgs{Command: "sleep 0.3; echo held"})
	if took := time.Since(start); took < 300*time.Millisecond || took > 5*time.Second {
		t.Errorf("sleep 0.3 took %v", took)
	}
	if res.ExitCode != 0 || res.Stdout != "sim ran: sleep 0.3; echo held\n" || res.Seconds < 0.3 {
		t.Errorf("after the sleep it answered %+v", res)
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start = time.Now()
	sim.Exec(ctx, api.ExecArgs{Command: "sleep 30"})
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("a cut command slept on for %v", took)
	}

	start = time.Now()
	if res := sim.Exec(context.Background(), api.ExecArgs{Command: "exit 3"}); res.ExitCode != 3 || time.Since(start) > time.Second {
		t.Errorf("exit 3 answered %+v", res)
	}
}
