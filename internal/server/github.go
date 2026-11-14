package server

// labd's side of the GitHub OAuth app: the web flow that signs a person in
// with a browser. labd asks for no scope, reads the account's numeric id and
// its handle from GET /user, and lets the token go: it is never stored.
//
// A Mac signs in with the device flow, which omarchy-m-test runs with the
// app's client id alone and hands labd the token once: tokenUser checks that
// this app issued it and names its account, and revoke lets it go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// GitHubApp is the OAuth app people sign in through.
type GitHubApp struct {
	ClientID, ClientSecret string
	// WebURL and APIURL are GitHub's own addresses, or those of a stand-in
	// on this machine for a lab under development (see ReadSignIn).
	WebURL, APIURL string
}

const (
	githubWeb = "https://github.com"
	githubAPI = "https://api.github.com"
)

// githubIdentity is what labd keeps of a GitHub account.
type githubIdentity struct {
	Login string
	ID    int64
}

// reHandle is a GitHub handle: letters, digits and dashes, and the
// underscore of a managed account.
var reHandle = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,38}$`)

var githubHTTP = &http.Client{
	Timeout:       10 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (g *GitHubApp) authorizeURL(redirectURI, state string) string {
	q := url.Values{"client_id": {g.ClientID}, "redirect_uri": {redirectURI}, "state": {state}, "scope": {""}}
	return g.WebURL + "/login/oauth/authorize?" + q.Encode()
}

// exchange turns the web flow's code into a token.
func (g *GitHubApp) exchange(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{"client_id": {g.ClientID}, "client_secret": {g.ClientSecret}, "code": {code}, "redirect_uri": {redirectURI}}
	req, err := http.NewRequestWithContext(ctx, "POST", g.WebURL+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var body struct {
		AccessToken string `json:"access_token"`
		Description string `json:"error_description"`
	}
	if err := githubDo(req, &body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		if body.Description != "" {
			return "", errors.New(clip(body.Description, 200))
		}
		return "", errors.New("it gave no token")
	}
	return body.AccessToken, nil
}

// user is who a token belongs to.
func (g *GitHubApp) user(ctx context.Context, token string) (githubIdentity, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", g.APIURL+"/user", nil)
	if err != nil {
		return githubIdentity{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	var id githubIdentity
	if err := githubDo(req, &id); err != nil {
		return githubIdentity{}, err
	}
	if id.ID <= 0 || !reHandle.MatchString(id.Login) {
		return githubIdentity{}, errors.New("it didn't say who signed in")
	}
	return id, nil
}

// tokenUser is who a token belongs to, only if this app issued it.
func (g *GitHubApp) tokenUser(ctx context.Context, token string) (githubIdentity, error) {
	var body struct {
		User githubIdentity `json:"user"`
	}
	if err := g.appToken(ctx, http.MethodPost, token, &body); err != nil {
		return githubIdentity{}, err
	}
	if body.User.ID <= 0 || !reHandle.MatchString(body.User.Login) {
		return githubIdentity{}, errors.New("GitHub didn't say who signed in")
	}
	return body.User, nil
}

// revoke ends a token this app issued. One that stays expires anyway.
func (g *GitHubApp) revoke(ctx context.Context, token string) error {
	return g.appToken(ctx, http.MethodDelete, token, nil)
}

// appToken asks GitHub about a token as the app itself, with its id and
// secret.
func (g *GitHubApp) appToken(ctx context.Context, method, token string, into any) error {
	payload, err := json.Marshal(map[string]string{"access_token": token})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, g.APIURL+"/applications/"+url.PathEscape(g.ClientID)+"/token", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.SetBasicAuth(g.ClientID, g.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return githubDo(req, into)
}

// errNoSuchAccount is GitHub saying it has no account under a handle.
var errNoSuchAccount = errors.New("GitHub knows no such account")

// lookup is the account a handle names on GitHub now: its id, and the
// handle as GitHub writes it. It needs no token and sends none.
func (g *GitHubApp) lookup(ctx context.Context, handle string) (githubIdentity, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", g.APIURL+"/users/"+url.PathEscape(handle), nil)
	if err != nil {
		return githubIdentity{}, err
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	var id githubIdentity
	if err := githubDo(req, &id); err != nil {
		if errors.Is(err, githubStatus(http.StatusNotFound)) {
			return githubIdentity{}, errNoSuchAccount
		}
		return githubIdentity{}, err
	}
	if id.ID <= 0 || !reHandle.MatchString(id.Login) || !strings.EqualFold(id.Login, handle) {
		return githubIdentity{}, errors.New("it didn't say whose handle that is")
	}
	return id, nil
}

// githubStatus is GitHub answering with a status that is no success.
type githubStatus int

func (s githubStatus) Error() string { return fmt.Sprintf("GitHub answered HTTP %d", int(s)) }

func githubDo(req *http.Request, into any) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "omarchy-mac-lab")
	res, err := githubHTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("couldn't reach GitHub: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return githubStatus(res.StatusCode)
	}
	if into == nil {
		return nil
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(into) != nil {
		return errors.New("GitHub's answer wasn't JSON")
	}
	return nil
}

// ReadSignIn reads labd's sign-in configuration from the folder dir
// (--sign-in): the OAuth app in github-client-id and github-client-secret,
// and the Admins in admins, one GitHub account id a line. No secret is a
// flag: the secret's file must be readable by its own account alone. A
// folder without the app's two files leaves sign-in off, so a lab can start
// before its owner has made the app.
//
// githubURL (--github-url) is a stand-in for GitHub on this machine, for a
// lab under development: a loopback http address and nothing else.
func ReadSignIn(dir, githubURL string) (*GitHubApp, []int64, error) {
	if dir == "" {
		if githubURL != "" {
			return nil, nil, errors.New("--github-url needs --sign-in: the folder with the OAuth app's values")
		}
		return nil, nil, nil
	}
	admins, err := readAdmins(filepath.Join(dir, "admins"))
	if err != nil {
		return nil, nil, err
	}
	idPath, secretPath := filepath.Join(dir, "github-client-id"), filepath.Join(dir, "github-client-secret")
	id, idErr := os.ReadFile(idPath)
	st, secretErr := os.Stat(secretPath)
	switch {
	case os.IsNotExist(idErr) && os.IsNotExist(secretErr):
		if githubURL != "" {
			return nil, nil, fmt.Errorf("--github-url needs the OAuth app's values: %s and %s", idPath, secretPath)
		}
		return nil, admins, nil
	case idErr != nil:
		return nil, nil, fmt.Errorf("the OAuth app's client id: %v", idErr)
	case secretErr != nil:
		return nil, nil, fmt.Errorf("the OAuth app's client secret: %v", secretErr)
	case st.Mode().Perm()&0o077 != 0:
		return nil, nil, fmt.Errorf("%s can be read by other accounts: chmod 600 it", secretPath)
	}
	secret, err := os.ReadFile(secretPath)
	if err != nil {
		return nil, nil, fmt.Errorf("the OAuth app's client secret: %v", err)
	}
	app := &GitHubApp{ClientID: strings.TrimSpace(string(id)), ClientSecret: strings.TrimSpace(string(secret)), WebURL: githubWeb, APIURL: githubAPI}
	if app.ClientID == "" || app.ClientSecret == "" {
		return nil, nil, fmt.Errorf("%s or %s is empty", idPath, secretPath)
	}
	if githubURL != "" {
		u, err := url.Parse(strings.TrimSuffix(githubURL, "/"))
		if err != nil || u.Scheme != "http" || !loopbackName(u.Host) || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, nil, fmt.Errorf("--github-url %q: only a stand-in for GitHub on this machine, such as http://127.0.0.1:7791", githubURL)
		}
		app.WebURL, app.APIURL = u.String(), u.String()
	}
	return app, admins, nil
}

func readAdmins(path string) ([]int64, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, err := strconv.ParseInt(line, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%s: %q is not a GitHub account id: one number a line, as `gh api user --jq .id` prints it", path, clip(line, 40))
		}
		ids = append(ids, id)
	}
	return ids, nil
}
