// Package oob is labd's side of out-of-band control: Hard reset through the
// one Out-of-band controller on the mini, which drives a port, not a Mac.
package oob

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
)

// Controller is what labd's recovery code holds for one Mac. Nothing makes
// one any more: a Mac gets its port from a Sighting, not from its record.
type Controller interface {
	Name() string
	// Reset hard-reboots the Mac regardless of what its OS is doing.
	Reset(ctx context.Context) error
	// Serial streams console lines to fn until ctx ends or the stream breaks.
	Serial(ctx context.Context, fn func(line string)) error
	Status(ctx context.Context) (string, error)
	// Arm puts the Mac back into serial mode, retrying for window while it reboots.
	Arm(ctx context.Context, window time.Duration) error
	// Raw copies console bytes to w until ctx ends; Write types into the console.
	Raw(ctx context.Context, w io.Writer) error
	Write(ctx context.Context, b []byte) error
}

// New refuses the controller a Mac's record names. The old lab gave each Mac
// a controller of its own, by address; the mini's controller serves three
// ports and no request to it goes without one.
func New(c *api.OOBConfig) (Controller, error) {
	return nil, errors.New("its record names a controller of its own, which this lab does not use: a Hard reset goes through the mini's Out-of-band controller, to the port the Mac is sighted on")
}

// Port is what the Out-of-band controller sees on one of its ports.
type Port struct {
	RID       int  `json:"rid"`
	Connected bool `json:"connected"`
	// Name is the name the Mac's Linux shows over a Thunderbolt link; empty
	// with no link, a Mac that is hung or off, or a USB 2 cable.
	Name string `json:"name,omitempty"`
	// ProductID is the partner's USB-PD product id: the same for two Macs of one model.
	ProductID int `json:"product_id,omitempty"`
	// PlugCount goes up each time something is plugged in; it starts again
	// when the mini restarts.
	PlugCount int `json:"plug_count"`
	// Tool is the reset tool's answer on this port, when it was asked for.
	Tool       string `json:"tool,omitempty"`
	ToolFailed bool   `json:"tool_failed,omitempty"`
}

// Seen is the state of a port at a Sighting. A reset that carries it goes
// out only while the port still shows it.
type Seen struct {
	ProductID int `json:"product_id"`
	PlugCount int `json:"plug_count"`
}

// Changed is the controller's refusal of a reset whose port is no longer as
// it was seen: the Sighting behind it is to be dropped.
type Changed struct{ Reason string }

func (e *Changed) Error() string { return e.Reason }

// Client talks to the Out-of-band controller on this machine.
type Client struct {
	// URL is the controller's loopback address, such as http://127.0.0.1:7780.
	URL string
	// TokenFile holds the controller's token, in a file only labd's account
	// reads. It is read for every request, so a token made after labd
	// started needs no restart.
	TokenFile string
}

// NewClient checks that addr is on this machine: the controller never
// listens anywhere else, and its token goes nowhere else.
func NewClient(addr, tokenFile string) (*Client, error) {
	u, err := url.Parse(addr)
	if err != nil || u.Scheme != "http" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.User != nil {
		return nil, fmt.Errorf("the Out-of-band controller's address %q is not http://127.0.0.1:PORT", addr)
	}
	if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() || u.Port() == "" {
		return nil, fmt.Errorf("the Out-of-band controller's address %q is not a loopback address with a port: it listens on this machine only", addr)
	}
	if tokenFile == "" {
		return nil, errors.New("the Out-of-band controller's token has no file")
	}
	return &Client{URL: "http://" + u.Host, TokenFile: tokenFile}, nil
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, timeout time.Duration) ([]byte, error) {
	token, err := ReadToken(c.TokenFile)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("the Out-of-band controller does not answer: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	text := strings.TrimSpace(string(b))
	switch resp.StatusCode {
	case http.StatusOK:
		return b, nil
	case http.StatusConflict:
		return nil, &Changed{Reason: text}
	}
	return nil, fmt.Errorf("the Out-of-band controller refused (%s): %s", resp.Status, text)
}

func (c *Client) status(ctx context.Context, rid int, query string, timeout time.Duration) (Port, error) {
	var p Port
	b, err := c.do(ctx, "GET", fmt.Sprintf("/v1/ports/%d/status%s", rid, query), nil, timeout)
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("the Out-of-band controller's answer for RID %d: %w", rid, err)
	}
	if p.RID != rid {
		return p, fmt.Errorf("the Out-of-band controller answered for RID %d when asked for RID %d", p.RID, rid)
	}
	return p, nil
}

// Port asks what the controller sees on a port. No tool runs and nothing is reset.
func (c *Client) Port(ctx context.Context, rid int) (Port, error) {
	return c.status(ctx, rid, "", 30*time.Second)
}

// Probe is Port with the reset tool's own answer on that port. It waits its
// turn behind a reset and resets nothing.
func (c *Client) Probe(ctx context.Context, rid int) (Port, error) {
	return c.status(ctx, rid, "?probe=1", 2*time.Minute)
}

// Reset sends a Hard reset to a port. With seen, the controller sends it only
// while the port still shows that state, and answers *Changed otherwise.
// With nil the port alone is named: a Proving reset.
func (c *Client) Reset(ctx context.Context, rid int, seen *Seen) error {
	var body []byte
	if seen != nil {
		body, _ = json.Marshal(seen)
	}
	_, err := c.do(ctx, "POST", fmt.Sprintf("/v1/ports/%d/reset", rid), body, 2*time.Minute)
	return err
}
