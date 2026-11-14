// Package githubtest is a stand-in for the part of GitHub labd talks to: the
// OAuth web flow that signs a browser in (authorize, the code for a token),
// GET /user, GET /users/{handle}, which says whose a handle is, and what a
// Mac sign-in needs: the device flow omarchy-m-test runs (a code to type,
// then the token), and the app's own check and revocation of a token. Tests
// sign people in against it, and tools/fake-github serves it on a loopback
// port for a labd under development. It knows the accounts it was given and
// nothing else, and it signs anyone in as any of them: it must never stand
// where a lab that holds anything can reach it.
package githubtest

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// User is one GitHub account: its numeric id, which never changes, and its
// handle, which can.
type User struct {
	ID    int64
	Login string
}

type grant struct {
	user        int64
	redirectURI string
}

// device is one run of the device flow: the code a person types at
// /login/device, and the account that approved it, 0 until one has.
type device struct {
	userCode string
	user     int64
}

// DeviceGrant is the grant type omarchy-m-test polls for its token with.
const DeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

// Server is the fake. Its web and API addresses are the same.
type Server struct {
	ClientID, ClientSecret string

	mu     sync.Mutex
	users  map[int64]string
	next   int64
	codes  map[string]grant
	tokens map[string]int64
	// elsewhere are tokens another OAuth app issued: GitHub knows whose
	// they are, and this app's check does not find them.
	elsewhere map[string]int64
	devices   map[string]*device
	revoked   []string
	asked     []url.Values
	looked    []string
	broken    int
}

// New is a fake GitHub with one OAuth app and no accounts.
func New(clientID, clientSecret string) *Server {
	return &Server{ClientID: clientID, ClientSecret: clientSecret,
		users: map[int64]string{}, codes: map[string]grant{}, tokens: map[string]int64{},
		elsewhere: map[string]int64{}, devices: map[string]*device{}}
}

// AddUser adds an account, or renames the one with that id.
func (s *Server) AddUser(id int64, login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[id] = login
}

// SignInAs makes every visit to the authorize page from now on approve at
// once as the account id, the way GitHub does for someone already signed in
// who approved the app before, and approves every device code asked for
// from now on as that account. With 0 the pages ask which account.
func (s *Server) SignInAs(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = id
}

// Asked is the query of every visit to the authorize page, oldest first.
func (s *Server) Asked() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.asked...)
}

// LookedUp is every handle GET /users/{handle} was asked for, oldest first.
func (s *Server) LookedUp() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.looked...)
}

// Issue makes token one this app issued to the account id, as the device
// flow would have.
func (s *Server) Issue(token string, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[token] = id
}

// IssueElsewhere makes token one another OAuth app issued to the account.
func (s *Server) IssueElsewhere(token string, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.elsewhere[token] = id
}

// Revoked is every token the app revoked, oldest first.
func (s *Server) Revoked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.revoked...)
}

// Approve is a person typing a device flow's code at /login/device and
// approving as the account id. It reports whether the code is one waiting.
func (s *Server) Approve(userCode string, id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, known := s.users[id]; !known {
		return false
	}
	for _, d := range s.devices {
		if d.userCode == strings.ToUpper(strings.TrimSpace(userCode)) && d.user == 0 {
			d.user = id
			return true
		}
	}
	return false
}

// Break makes every answer from now on this status, as when GitHub is
// down; 0 mends it.
func (s *Server) Break(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broken = status
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	broken := s.broken
	s.mu.Unlock()
	if broken != 0 {
		http.Error(w, "unavailable", broken)
		return
	}
	if handle, ok := strings.CutPrefix(r.URL.Path, "/users/"); ok && r.Method == http.MethodGet {
		s.lookup(w, handle)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/applications/") {
		s.appToken(w, r)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "POST /login/device/code":
		s.deviceCode(w, r)
	case "GET /login/device":
		s.devicePage(w, r)
	case "GET /login/oauth/authorize":
		s.authorize(w, r)
	case "POST /login/oauth/access_token":
		s.accessToken(w, r)
	case "GET /user":
		s.user(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("redirect_uri"))
	if q.Get("client_id") != s.ClientID || err != nil || back.Host == "" {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	if !q.Has("as") {
		s.asked = append(s.asked, q)
	}
	id := s.next
	if as, err := strconv.ParseInt(q.Get("as"), 10, 64); err == nil {
		id = as
	}
	_, known := s.users[id]
	var code string
	if known {
		code = randHex()
		s.codes[code] = grant{user: id, redirectURI: q.Get("redirect_uri")}
	}
	users := make([]User, 0, len(s.users))
	for id, login := range s.users {
		users = append(users, User{id, login})
	}
	s.mu.Unlock()
	if known {
		bq := back.Query()
		bq.Set("code", code)
		bq.Set("state", q.Get("state"))
		back.RawQuery = bq.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
		return
	}
	sort.Slice(users, func(i, k int) bool { return users[i].Login < users[k].Login })
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, "<!doctype html><title>Fake GitHub</title><h1>Fake GitHub</h1><p>This is not GitHub. It signs you in as any account below, for a lab under development.</p><ul>")
	for _, u := range users {
		q.Set("as", strconv.FormatInt(u.ID, 10))
		fmt.Fprintf(w, `<li><a href="/login/oauth/authorize?%s">Sign in as @%s</a> (account id %d)</li>`, html.EscapeString(q.Encode()), html.EscapeString(u.Login), u.ID)
	}
	fmt.Fprint(w, "</ul>")
}

func (s *Server) accessToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	refuse := func(code, why string) {
		writeJSON(w, http.StatusOK, map[string]string{"error": code, "error_description": why})
	}
	if r.PostForm.Get("grant_type") == DeviceGrant {
		s.deviceToken(w, r, refuse)
		return
	}
	if r.PostForm.Get("client_id") != s.ClientID || r.PostForm.Get("client_secret") != s.ClientSecret {
		refuse("incorrect_client_credentials", "The client_id and/or client_secret passed are incorrect.")
		return
	}
	s.mu.Lock()
	g, ok := s.codes[r.PostForm.Get("code")]
	delete(s.codes, r.PostForm.Get("code"))
	var token string
	if ok && g.redirectURI == r.PostForm.Get("redirect_uri") {
		token = "gho_" + randHex()
		s.tokens[token] = g.user
	}
	s.mu.Unlock()
	switch {
	case !ok:
		refuse("bad_verification_code", "The code passed is incorrect or expired.")
	case token == "":
		refuse("redirect_uri_mismatch", "The redirect_uri MUST match the registered callback URL for this application.")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"access_token": token, "token_type": "bearer", "scope": ""})
	}
}

// deviceCode starts the device flow, which needs the app's client id and no
// secret: a code for the Mac to poll with and one for a person to type.
func (s *Server) deviceCode(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("client_id") != s.ClientID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Not Found"})
		return
	}
	code := strings.ToUpper(randHex()[:8])
	d := &device{userCode: code[:4] + "-" + code[4:]}
	deviceCode := randHex()
	s.mu.Lock()
	if _, known := s.users[s.next]; known {
		d.user = s.next
	}
	s.devices[deviceCode] = d
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":      deviceCode,
		"user_code":        d.userCode,
		"verification_uri": "http://" + r.Host + "/login/device",
		"expires_in":       900,
		"interval":         5,
	})
}

// devicePage is where a person types the code their Mac shows, on any
// device, and says which account approves it.
func (s *Server) devicePage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, "<!doctype html><title>Fake GitHub</title><h1>Fake GitHub</h1><p>This is not GitHub. It approves a Mac's code as any account below, for a lab under development.</p>")
	if as, err := strconv.ParseInt(q.Get("as"), 10, 64); err == nil {
		if s.Approve(q.Get("user_code"), as) {
			fmt.Fprint(w, "<p>Approved. Go back to the Mac.</p>")
		} else {
			fmt.Fprint(w, "<p>No such code is waiting, or no such account.</p>")
		}
		return
	}
	s.mu.Lock()
	users := make([]User, 0, len(s.users))
	for id, login := range s.users {
		users = append(users, User{id, login})
	}
	s.mu.Unlock()
	sort.Slice(users, func(i, k int) bool { return users[i].Login < users[k].Login })
	fmt.Fprintf(w, `<form method="get" action="/login/device"><label>The code <input name="user_code" value="%s" required></label> <select name="as">`, html.EscapeString(q.Get("user_code")))
	for _, u := range users {
		fmt.Fprintf(w, `<option value="%d">@%s (account id %d)</option>`, u.ID, html.EscapeString(u.Login), u.ID)
	}
	fmt.Fprint(w, `</select> <button type="submit">Approve</button></form>`)
}

// deviceToken answers a Mac's poll: pending until a person approved the
// code, then the token, once.
func (s *Server) deviceToken(w http.ResponseWriter, r *http.Request, refuse func(code, why string)) {
	if r.PostForm.Get("client_id") != s.ClientID {
		refuse("incorrect_client_credentials", "The client_id and/or client_secret passed are incorrect.")
		return
	}
	deviceCode := r.PostForm.Get("device_code")
	s.mu.Lock()
	d := s.devices[deviceCode]
	var token string
	if d != nil && d.user != 0 {
		delete(s.devices, deviceCode)
		token = "gho_" + randHex()
		s.tokens[token] = d.user
	}
	s.mu.Unlock()
	switch {
	case d == nil:
		refuse("incorrect_device_code", "The device_code provided is not valid.")
	case token == "":
		refuse("authorization_pending", "The authorization request is still pending.")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"access_token": token, "token_type": "bearer", "scope": ""})
	}
}

// appToken is the app asking about a token with its own id and secret:
// POST says whose it is, only when this app issued it, and DELETE revokes
// it.
func (s *Server) appToken(w http.ResponseWriter, r *http.Request) {
	id, secret, ok := r.BasicAuth()
	if r.URL.Path != "/applications/"+s.ClientID+"/token" || (r.Method != http.MethodPost && r.Method != http.MethodDelete) {
		http.NotFound(w, r)
		return
	}
	if !ok || id != s.ClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(s.ClientSecret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Validation Failed"})
		return
	}
	s.mu.Lock()
	account, issued := s.tokens[body.AccessToken]
	login := s.users[account]
	if issued && r.Method == http.MethodDelete {
		delete(s.tokens, body.AccessToken)
		s.revoked = append(s.revoked, body.AccessToken)
	}
	s.mu.Unlock()
	switch {
	case !issued || login == "":
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"id":    1,
			"token": body.AccessToken,
			"app":   map[string]string{"client_id": s.ClientID, "name": "Fake GitHub's app"},
			"user": map[string]any{
				"login":      login,
				"id":         account,
				"avatar_url": "https://avatars.example/u/" + strconv.FormatInt(account, 10),
				"type":       "User",
			},
		})
	}
}

// user answers as GitHub does for a token with no scope: the public profile,
// which carries more than labd may keep.
func (s *Server) user(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	id, ok := s.tokens[token]
	if !ok {
		id, ok = s.elsewhere[token]
	}
	login := s.users[id]
	s.mu.Unlock()
	if !ok || login == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"login":      login,
		"id":         id,
		"name":       "Full Name of " + login,
		"email":      login + "@mail.example",
		"avatar_url": "https://avatars.example/u/" + strconv.FormatInt(id, 10),
		"type":       "User",
	})
}

// lookup answers as GitHub does for a public profile: handles match
// whatever their case, and the answer carries the handle as the account
// writes it.
func (s *Server) lookup(w http.ResponseWriter, handle string) {
	s.mu.Lock()
	s.looked = append(s.looked, handle)
	var id int64
	var login string
	for uid, name := range s.users {
		if strings.EqualFold(name, handle) {
			id, login = uid, name
		}
	}
	s.mu.Unlock()
	if id == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"login":      login,
		"id":         id,
		"name":       "Full Name of " + login,
		"email":      nil,
		"avatar_url": "https://avatars.example/u/" + strconv.FormatInt(id, 10),
		"type":       "User",
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func randHex() string {
	b := make([]byte, 20)
	rand.Read(b)
	return hex.EncodeToString(b)
}
