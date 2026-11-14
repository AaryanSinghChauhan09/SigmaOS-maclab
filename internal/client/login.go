package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/omacom/omarchy-maclab/internal/labhttp"
)

// Release is the Lab release this program came from, which every request
// names in its User-Agent. The lab CLI sets it from its build.
var Release = "dev"

// LabAddress is where Omarchy Mac Lab answers.
const LabAddress = "https://lab.omarchy-mac.org"

// ownPage is the page a person makes and renews their Lab tokens on.
const ownPage = "/me"

// tokenPrefix starts every Lab token.
const tokenPrefix = "lab_"

// dark is what lab says while the lab cannot be reached, in two parts
// around the status the edge gave.
const (
	darkNow  = "Omarchy Mac Lab cannot be reached right now"
	darkThen = "No reset is possible until it returns."
)

func userAgent() string { return "lab/" + strings.TrimPrefix(Release, "v") }

// LoginPath is the file lab keeps its one login in. A login for maclab
// lives beside it in client.json, which lab never reads or writes.
func LoginPath() string {
	dir, _ := os.UserConfigDir()
	return filepath.Join(dir, "maclab", "omarchy-mac-lab.json")
}

// ErrNotLoggedIn is Load finding no saved login.
var ErrNotLoggedIn = fmt.Errorf("Not logged in to Omarchy Mac Lab. Make a Lab token on your own page, %s, then run: lab login %s", LabAddress+ownPage, LabAddress)

// Load is the client for this call: the saved login, always. MACLAB_URL and
// MACLAB_TOKEN given together reach another lab for this one call, and the
// saved login is then not read. MACLAB_URL alone is taken only when it
// names the saved login's own address (see sameAddress), so a saved token
// goes nowhere else; MACLAB_TOKEN alone is refused.
func Load() (*Client, error) {
	envURL, envToken := os.Getenv("MACLAB_URL"), os.Getenv("MACLAB_TOKEN")
	c := &Client{}
	if envURL != "" && envToken != "" {
		c.URL, c.Token = strings.TrimRight(envURL, "/"), envToken
	} else {
		b, err := os.ReadFile(LoginPath())
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotLoggedIn
		}
		if err == nil {
			err = json.Unmarshal(b, c)
		}
		if err != nil {
			return nil, fmt.Errorf("the saved login, %s: %w", LoginPath(), err)
		}
		if c.URL == "" || c.Token == "" {
			return nil, ErrNotLoggedIn
		}
		switch {
		case envToken != "":
			return nil, errors.New("MACLAB_TOKEN is set without MACLAB_URL: set both to reach another lab for one call, or neither to use the saved login")
		case envURL != "" && !sameAddress(envURL, c.URL):
			return nil, fmt.Errorf("MACLAB_URL names %s, but the saved login is for %s, and a saved Lab token only goes to the address it was saved for: set MACLAB_TOKEN too, or unset MACLAB_URL", redact(envURL), c.URL)
		}
	}
	if v := os.Getenv("MACLAB_CA_FILE"); v != "" {
		c.CAFile = v
	}
	if os.Getenv("MACLAB_ALLOW_PLAIN_HTTP") == "1" {
		c.AllowPlainHTTP = true
	}
	if err := c.connect(); err != nil {
		return nil, err
	}
	return c, nil
}

// address is raw as an address a login is saved for: the scheme and the
// host in lower case, without the scheme's own port and without a trailing
// slash. The path keeps its letters, since labd tells them apart.
func address(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("the address %q: %v", raw, err)
	}
	if u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("the address %s: give the Lab address alone, such as %s", u.Redacted(), LabAddress)
	}
	scheme, host := strings.ToLower(u.Scheme), strings.ToLower(u.Host)
	if port := u.Port(); (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		host = strings.TrimSuffix(host, ":"+port)
	}
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// sameAddress reports whether two addresses are one to lab.
func sameAddress(a, b string) bool {
	x, err := address(a)
	if err != nil {
		return false
	}
	y, err := address(b)
	return err == nil && x == y
}

func redact(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Redacted()
	}
	return "another address"
}

// connect sets up c's HTTP client with labhttp's policy for c.URL.
func (c *Client) connect() error {
	if err := labhttp.CheckServer(c.URL, c.AllowPlainHTTP); err != nil {
		return err
	}
	p := labhttp.Policy{AllowPlainHTTP: c.AllowPlainHTTP, UserAgent: userAgent()}
	if c.CAFile != "" {
		b, err := labhttp.ReadCAFile(c.CAFile)
		if err != nil {
			return err
		}
		p.CAPEM = b
	}
	rt, err := p.Transport()
	if err != nil {
		return err
	}
	c.http = labhttp.Client(&answers{base: rt, c: c}, 0)
	return nil
}

// Save keeps c as the one login, replacing an earlier one.
func (c *Client) Save() error {
	b, _ := json.MarshalIndent(c, "", "  ")
	path := LoginPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".omarchy-mac-lab-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(b, '\n'))
	if err = errors.Join(err, f.Chmod(0o600), f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Identity is who a Lab token belongs to, as the lab names them.
type Identity struct {
	Handle     string `json:"login"`
	Kind       string `json:"kind"`
	LabRelease string `json:"-"` // the Lab release the lab runs
	Address    string `json:"-"` // the lab's address, as a login for it is saved
}

// WhoAmI asks the lab whose Lab token c carries.
func (c *Client) WhoAmI(ctx context.Context) (*Identity, error) {
	var who Identity
	if err := c.do(ctx, "GET", "/api/auth", nil, &who); err != nil {
		return nil, err
	}
	who.LabRelease, who.Address = c.LabRelease(), c.URL
	return &who, nil
}

// LabRelease is the Lab release the lab said it runs on its last answer, ""
// before one.
func (c *Client) LabRelease() string {
	v, _ := c.release.Load().(string)
	return v
}

// Login asks the lab at rawURL whose token is and, once it has named them,
// keeps the two as the one login. Nothing is stored before that, so a login
// the lab refuses (a Blocked person, an expired token, the Alias) leaves an
// earlier login as it was. caFile and allowPlain are lab login's flags.
func Login(ctx context.Context, rawURL, token, caFile string, allowPlain bool) (*Identity, error) {
	at, err := address(rawURL)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(token, tokenPrefix) {
		return nil, fmt.Errorf("that is not a Lab token, which starts with %s: make one on your own page, %s", tokenPrefix, at+ownPage)
	}
	c := &Client{URL: at, Token: token, AllowPlainHTTP: allowPlain}
	if caFile != "" {
		if c.CAFile, err = filepath.Abs(caFile); err != nil {
			return nil, err
		}
	}
	if err := c.connect(); err != nil {
		return nil, err
	}
	who, err := c.WhoAmI(ctx)
	if err != nil {
		return nil, err
	}
	if who.Handle == "" {
		return nil, fmt.Errorf("%s answered, but not as Omarchy Mac Lab does: nothing was saved", at)
	}
	if err := c.Save(); err != nil {
		return nil, err
	}
	return who, nil
}

// answers reads each answer before lab does: it keeps the Lab release the
// lab names, and puts one plain line in place of an error page. While the
// lab is Dark its edge answers 530 with a page of its own, and a challenge
// or a limit of the edge's is a page too; labd answers lab in plain text.
type answers struct {
	base http.RoundTripper
	c    *Client
}

func (a *answers) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := a.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if v := resp.Header.Get("Lab-Release"); v != "" {
		a.c.release.Store(v)
	}
	if !edgeError(resp) {
		return resp, nil
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	where := fmt.Sprintf("HTTP %d", resp.StatusCode)
	if ray := resp.Header.Get("CF-Ray"); ray != "" && len(ray) <= 64 && !strings.ContainsAny(ray, "<> \t\r\n") {
		where += ", ray " + ray
	}
	line := fmt.Sprintf("%s (%s). %s\n", darkNow, where, darkThen)
	resp.Body = io.NopCloser(strings.NewReader(line))
	resp.ContentLength = int64(len(line))
	resp.Uncompressed = false
	resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	return resp, nil
}

// edgeError reports whether resp is the edge's own error and not labd's:
// an error with a page for a body, whatever type it is sent as, or one of
// the statuses only the edge gives (520 to 530).
func edgeError(resp *http.Response) bool {
	if resp.StatusCode < 400 {
		return false
	}
	if (resp.StatusCode >= 520 && resp.StatusCode <= 530) || strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return true
	}
	start := bufio.NewReader(resp.Body)
	resp.Body = struct {
		io.Reader
		io.Closer
	}{start, resp.Body}
	head, _ := start.Peek(512)
	return strings.HasPrefix(http.DetectContentType(head), "text/html")
}
