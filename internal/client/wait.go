package client

import (
	"context"
	"fmt"
	"net/url"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// WaitPast is one job wait: it returns when the job is done, when it has
// more than seen events, or after waitSlice, whichever comes first.
func (c *Client) WaitPast(ctx context.Context, id string, seen int) (*api.Job, error) {
	var out api.Job
	return &out, c.do(ctx, "GET", fmt.Sprintf("/api/jobs/%s/wait?timeout=%d&events=%d", url.PathEscape(id), int(waitSlice.Seconds()), seen), nil, &out)
}
