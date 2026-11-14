package client

import (
	"context"
	"errors"
	"fmt"

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
