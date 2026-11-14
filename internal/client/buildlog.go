package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// followBuildLog writes a build's log to w as it grows, until the build
// ends: one call of at most waitSlice after another, each from where the
// last one ended.
func (c *Client) followBuildLog(ctx context.Context, id string, w io.Writer) error {
	var offset int64
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/builds/%s/log?offset=%d&wait=%d", c.URL, url.PathEscape(id), offset, int(waitSlice.Seconds())), nil)
		req.Header.Set("Authorization", "Bearer "+c.Token)
		resp, err := c.http.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			m, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("%s", strings.TrimSpace(string(m)))
		}
		n, err := io.Copy(w, resp.Body)
		resp.Body.Close()
		if err != nil {
			return err
		}
		next, err := strconv.ParseInt(resp.Header.Get("X-Log-Offset"), 10, 64)
		if err != nil {
			return fmt.Errorf("labd did not say where the build log ends: it is older than this lab")
		}
		offset = next
		switch api.BuildState(resp.Header.Get("X-Build-State")) {
		case api.BuildQueued, api.BuildRunning:
		default:
			return nil
		}
		if n == 0 {
			// labd held the call until there was something to say; a moment
			// between two empty answers keeps a fault from spinning.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(followPause):
			}
		}
	}
}

var followPause = time.Second
