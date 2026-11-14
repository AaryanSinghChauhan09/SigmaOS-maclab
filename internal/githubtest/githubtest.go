// Package githubtest is a stand-in for the part of GitHub labd's browser
// sign-in talks to: the OAuth web flow (authorize, the code for a token) and
// GET /user. Tests sign people in against it, and tools/fake-github serves it
// on a loopback port for a labd under development. It knows the accounts it
// was given and nothing else, and it signs anyone in as any of them: it must
// never stand where a lab that holds anything can reach it.
package githubtest

import (
	"crypto/rand"
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

// Server is the fake. Its web and API addresses are the same.
type Server struct {
	ClientID, ClientSecret string

	mu     sync.Mutex
	users  map[int64]string
	next   int64
	codes  map[string]grant
	tokens map[string]int64
	asked  []url.Values
}

// New is a fake GitHub with one OAuth app and no accounts.
func New(clientID, clientSecret string) *Server {
	return &Server{ClientID: clientID, ClientSecret: clientSecret,
		users: map[int64]string{}, codes: map[string]grant{}, tokens: map[string]int64{}}
}

// AddUser adds an account, or renames the one with that id.
func (s *Server) AddUser(id int64, login string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[id] = login
}

// SignInAs makes every visit to the authorize page from now on approve at
// once as the account id, the way GitHub does for someone already signed in
// who approved the app before. With 0 the page asks which account.
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

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method + " " + r.URL.Path {
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

// user answers as GitHub does for a token with no scope: the public profile,
// which carries more than labd may keep.
func (s *Server) user(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	id, ok := s.tokens[token]
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
