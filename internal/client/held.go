package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// heldType is the stream labd answers a held request with: Keepalives, each
// an empty line, then one last line, an api.HeldLine.
const heldType = "application/x-ndjson"

// ErrCut is a request that was sent and whose answer never came whole. labd
// may still be carrying it out, so it is never sent again.
var ErrCut = errors.New("connection lost")

func held(resp *http.Response) bool {
	return strings.HasPrefix(resp.Header.Get("Content-Type"), heldType)
}

// heldResult reads a held response to its last line and returns the result
// in it. A stream that ends without one is ErrCut.
func heldResult(body io.Reader) ([]byte, error) {
	br := bufio.NewReader(body)
	var last []byte
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			break // a line without its end is a line that was cut
		}
		if line = bytes.TrimSpace(line); len(line) > 0 {
			last = line
		}
	}
	var l api.HeldLine
	if last == nil || json.Unmarshal(last, &l) != nil || (l.Error == "" && l.Result == nil) {
		return nil, ErrCut
	}
	if l.Error != "" {
		return nil, &StatusError{Code: l.Status, Msg: l.Error}
	}
	return l.Result, nil
}

// once sends a request labd holds until its work ends (lab exec,
// freeze-entry) and reads the stream it answers with. The request goes out
// one time: when it left and no whole answer came back, labd may still be
// carrying it out, and the error is ErrCut saying that what may still be
// running. A request that never left, or that labd turned down, fails as
// any other.
func (c *Client) once(ctx context.Context, method, path string, body io.Reader, out any, what string) error {
	var left atomic.Bool
	trace := &httptrace.ClientTrace{WroteHeaders: func() { left.Store(true) }}
	err := c.do(httptrace.WithClientTrace(ctx, trace), method, path, body, out)
	var refused *StatusError
	if err == nil || errors.As(err, &refused) || ctx.Err() != nil || !left.Load() {
		return err
	}
	return fmt.Errorf("%w; %s may still be running", ErrCut, what)
}
