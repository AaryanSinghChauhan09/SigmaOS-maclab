package client

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/omacom/omarchy-maclab/internal/api"
)

func (c *Client) requireRecoveryMode(ctx context.Context, mode string) error {
	if mode == api.RecoverySerialAssisted {
		return errors.New(api.NoSerialConsole)
	}
	if !api.ValidRecoveryMode(mode) {
		return fmt.Errorf("invalid recovery_mode; no job submitted")
	}
	if mode == "" {
		return nil
	}
	var caps api.RecoveryCapabilities
	if err := c.do(ctx, "GET", "/api/recovery", nil, &caps); err != nil {
		return fmt.Errorf("controller recovery-mode capability unavailable; no job submitted: %w", err)
	}
	if caps.Schema != 1 || !caps.PerJobOOBIsolation {
		return fmt.Errorf("controller lacks per-job OOB isolation; no job submitted")
	}
	for _, m := range caps.Modes {
		if m == mode {
			return nil
		}
	}
	return fmt.Errorf("controller does not support requested recovery mode; no job submitted")
}

// Ports are the lab server's ports a Proving reset may name, facing its back.
var Ports = []string{"left", "middle", "right"}

// ProvingReset has the lab reset the port named, to place a Mac the lab has
// no Sighting of. It is a Maintainer's, and it counts only if that Mac
// returns and every other Mac keeps its boot id: the answer comes when the
// lab knows, which takes as long as the Mac takes to start.
func (c *Client) ProvingReset(ctx context.Context, name, port string) (*api.Proving, error) {
	ok := false
	for _, p := range Ports {
		ok = ok || p == port
	}
	if !ok {
		return nil, fmt.Errorf("no port %q: the lab server's ports are left, middle and right, facing its back", port)
	}
	var out api.Proving
	return &out, c.do(ctx, "POST", "/api/devices/"+url.PathEscape(name)+"/reset?prove="+url.QueryEscape(port), nil, &out)
}

// Cable is what the lab knows of a Mac's cable to the lab server, and what
// the Out-of-band controller's ports show.
func (c *Client) Cable(ctx context.Context, name string) (*api.OOBStatus, error) {
	var out api.OOBStatus
	return &out, c.do(ctx, "GET", "/api/devices/"+url.PathEscape(name)+"/oob", nil, &out)
}
