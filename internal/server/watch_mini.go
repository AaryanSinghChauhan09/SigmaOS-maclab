package server

// What labd asks outside itself to watch the lab's parts: the mini's two
// helpers, which it runs as programs, and an address fetched from outside.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// runHelper runs one of the mini's helpers with no argument and no input, and
// gives what it printed. A helper that fails is an error with what it said.
func runHelper(ctx context.Context, script string, limit time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, script)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		said := oneLine(errs.String())
		if len(said) > 300 {
			said = said[:300]
		}
		return "", fmt.Errorf("%s: %v: %s", script, err, said)
	}
	return out.String(), nil
}

// MiniParts is Watch.Mini as scripts/mini/parts answers it.
func MiniParts(script string) func(context.Context) (Mini, error) {
	return func(ctx context.Context) (Mini, error) {
		out, err := runHelper(ctx, script, 30*time.Second)
		if err != nil {
			return Mini{}, err
		}
		return ParseParts(out)
	}
}

// ParseParts reads what scripts/mini/parts prints, a line for each thing:
//
//	boot SECONDS
//	tunnel up|down
//	litestream running|stopped ERRORS
//	service NAME STARTS
//
// boot is always there; a part the mini does not have has no line. A line
// it does not know is an error: labd and the helper come from one build.
func ParseParts(out string) (Mini, error) {
	m := Mini{Starts: map[string]int{}}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		bad := fmt.Errorf("the mini's helper said %q", line)
		switch {
		case f[0] == "boot" && len(f) == 2:
			sec, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil || sec <= 0 {
				return Mini{}, bad
			}
			m.Boot = time.Unix(sec, 0)
		case f[0] == "tunnel" && len(f) == 2 && (f[1] == "up" || f[1] == "down"):
			m.Tunnel = f[1]
		case f[0] == "litestream" && len(f) == 3 && (f[1] == "running" || f[1] == "stopped"):
			n, err := strconv.Atoi(f[2])
			if err != nil || n < 0 {
				return Mini{}, bad
			}
			m.Litestream, m.LitestreamErrors = f[1], n
		case f[0] == "service" && len(f) == 3:
			n, err := strconv.Atoi(f[2])
			if err != nil || n < 0 {
				return Mini{}, bad
			}
			m.Starts[f[1]] = n
		default:
			return Mini{}, bad
		}
	}
	if m.Boot.IsZero() {
		return Mini{}, errors.New("the mini's helper did not say when the mini started")
	}
	return m, nil
}

// PendingUpdates is Watch.Updates as scripts/mini/pending-updates answers
// it: a line for each update that waits.
func PendingUpdates(script string) func(context.Context) ([]string, error) {
	return func(ctx context.Context) ([]string, error) {
		out, err := runHelper(ctx, script, 4*time.Minute)
		if err != nil {
			return nil, err
		}
		var lines []string
		for _, line := range strings.Split(out, "\n") {
			if line = oneLine(line); line != "" {
				lines = append(lines, line)
			}
		}
		return lines, nil
	}
}

// AskOutside is Watch.Ask over the network: one GET that follows no
// redirect, by the name labd goes by.
func AskOutside(userAgent string) func(ctx context.Context, address string) (int, string, error) {
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, address string) (int, string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(req)
		if err != nil {
			return 0, "", err
		}
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location"), nil
	}
}
