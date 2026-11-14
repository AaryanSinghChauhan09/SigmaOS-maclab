package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const noConsole = "The serial console is not part of Omarchy Mac Lab."

func TestTheSerialConsoleCommandsAreRetired(t *testing.T) {
	c := labAt(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("labd was asked %s %s", r.Method, r.URL.Path)
	})
	for _, cmd := range []string{"shell", "console", "serial", "recovery-mode"} {
		if err := retired(cmd); err == nil || err.Error() != noConsole {
			t.Errorf("lab %s says %v", cmd, err)
		}
		if err := dispatch(context.Background(), c, cmd, []string{"m1", "--mode", "network-only"}); err == nil || err.Error() != noConsole {
			t.Errorf("lab %s m1 says %v", cmd, err)
		}
		if strings.Contains(usage, "lab "+cmd+" ") {
			t.Errorf("lab help still lists lab %s", cmd)
		}
	}
	if strings.Contains(usage, "serial") {
		t.Errorf("lab help still speaks of serial:\n%s", usage)
	}
	for _, cmd := range []string{"baseline", "crashtest", "run"} {
		err := dispatch(context.Background(), c, cmd, []string{"m1", "--recovery-mode", "serial-assisted", "--no-wait"})
		if err == nil || err.Error() != noConsole {
			t.Errorf("lab %s --recovery-mode serial-assisted says %v", cmd, err)
		}
	}
}

func TestLabExecHelpSaysNeverToPutASecretInACommand(t *testing.T) {
	const want = "never put a secret in a command"
	var help []string
	for _, line := range strings.Split(usage, "\n") {
		if strings.Contains(line, "lab exec ") {
			help = append(help, line)
		} else if len(help) > 0 && strings.HasPrefix(line, "      ") {
			help = append(help, line)
		} else if len(help) > 0 {
			break
		}
	}
	text := strings.Join(help, "\n")
	if !strings.Contains(text, want) || !strings.Contains(text, "your own Lease") || !strings.Contains(text, "Exec record") || !strings.Contains(text, "12 months") {
		t.Errorf("lab help on exec:\n%s", text)
	}
	c := labAt(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("labd was asked %s %s", r.Method, r.URL.Path)
	})
	if err := dispatch(context.Background(), c, "exec", []string{"m1"}); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("lab exec with no command says %v", err)
	}
}
