package reports

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// Mac sign-in: `omarchy-m-test --sign-in` ties a Mac's Machine key to a
// person. The tool runs GitHub's device flow and hands the lab the token
// once, in a document the Machine key signed. GitHub confirms that the lab's
// OAuth app issued the token and names its account; the Mac is signed in as
// that person and the token is revoked, never kept. From then on the Mac's
// reports are stamped with the person, and are Verified runs while that
// person is on the Omarchy M team.
//
// The wire is the old site's, names and all: POST /api/v1/tester_bindings
// signs in, POST /api/v1/tester_requests asks the status or signs out, and
// "tester" is whether the Mac's runs are Verified runs.
const (
	// MaxSignInBytes is the largest sign-in or request read.
	MaxSignInBytes = 8 << 10
	// SignInsPerHour and SignInRequestsPerHour are the limits for one
	// address: every sign-in counts, refused or not, and so does every
	// status and sign-out.
	SignInsPerHour        = 10
	SignInRequestsPerHour = 60

	signInVersion = 1
	// requestMaxAge is how far from the lab's clock a status or a sign-out
	// may be dated.
	requestMaxAge = 24 * time.Hour
)

// BlockedSentence is all a Blocked person is told, wherever they meet the
// lab: no reason and no name.
const BlockedSentence = "This GitHub account is blocked from Omarchy Mac Lab."

// GitHub is the lab's OAuth app asking GitHub about a token the device flow
// gave a Mac.
type GitHub interface {
	// TokenUser is the account a token belongs to, only when the lab's own
	// app issued it. The error's words go to the person signing in.
	TokenUser(ctx context.Context, token string) (accountID int64, handle string, err error)
	// Revoke lets the token go. A failure is nobody's concern: it expires.
	Revoke(ctx context.Context, token string)
}

// People is what the lab keeps of people, in labd.db: reports.db holds a
// sign-in's account id and handle and asks the rest.
type People interface {
	// Know makes the lab know the person GitHub named, by account id, under
	// the handle they have now: no more than that. It reports true, and
	// keeps nothing, for a Blocked person.
	Know(accountID int64, handle string) (blocked bool, err error)
	Blocked(accountID int64) bool
	// OnMTeam reports whether the person is on the Omarchy M team now.
	OnMTeam(accountID int64) bool
}

// SignIn is one Mac signed in as a person.
type SignIn struct {
	ID        int64
	MachineID string
	AccountID int64
	Handle    string
	// At is when the Mac signed in, this time.
	At time.Time
}

// Name is what a status calls the sign-in and a sign-out must name: its
// time in microseconds, so a copy of a sign-out made before the Mac signed
// in again ends nothing.
func (s SignIn) Name() string { return strconv.FormatInt(s.At.UnixMicro(), 10) }

const signInColumns = `id, machine_id, account_id, handle, updated_at`

func scanSignIn(row scanner) (*SignIn, error) {
	var s SignIn
	var at int64
	err := row.Scan(&s.ID, &s.MachineID, &s.AccountID, &s.Handle, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.At = time.UnixMicro(at).UTC()
	return &s, nil
}

// signIn signs the Mac in as the person, in place of whoever it was signed
// in as. A sign-in on a Mac is always later than the one before it, even
// within one tick of the clock.
func (s *Store) signIn(machineID string, accountID int64, handle string, at time.Time) (*SignIn, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := at.UnixMicro()
	signIn, err := scanSignIn(tx.QueryRow(`INSERT INTO mac_sign_ins (machine_id, account_id, handle, created_at, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (machine_id) DO UPDATE SET account_id = excluded.account_id, handle = excluded.handle,
			updated_at = max(excluded.updated_at, mac_sign_ins.updated_at + 1)
		RETURNING `+signInColumns, machineID, accountID, handle, now, now))
	if err != nil {
		return nil, err
	}
	return signIn, tx.Commit()
}

// SignInOf is the sign-in of the Mac with that machine id, nil for none.
func (s *Store) SignInOf(machineID string) (*SignIn, error) {
	return scanSignIn(s.db.QueryRow(`SELECT `+signInColumns+` FROM mac_sign_ins WHERE machine_id = ?`, machineID))
}

// endSignIn ends that sign-in and no later one of its Mac.
func (s *Store) endSignIn(signIn SignIn) (ended bool, err error) {
	res, err := s.db.Exec(`DELETE FROM mac_sign_ins WHERE id = ? AND updated_at = ?`, signIn.ID, signIn.At.UnixMicro())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) signIns(where string, args ...any) ([]SignIn, error) {
	rows, err := s.db.Query(`SELECT `+signInColumns+` FROM mac_sign_ins `+where+` ORDER BY updated_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SignIn
	for rows.Next() {
		signIn, err := scanSignIn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *signIn)
	}
	return out, rows.Err()
}

// SignInsOf are the Macs signed in as the person, oldest sign-in first.
func (s *Store) SignInsOf(accountID int64) ([]SignIn, error) {
	return s.signIns(`WHERE account_id = ?`, accountID)
}

// SignIns is every Mac signed in.
func (s *Store) SignIns() ([]SignIn, error) { return s.signIns(``) }

// EndSignInOf ends the person's sign-in with that id, as a page asks it:
// the person's own, or a Maintainer's for them. It gives the sign-in it
// ended, nil when the person has none such.
func (s *Store) EndSignInOf(accountID, id int64) (*SignIn, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	signIn, err := scanSignIn(tx.QueryRow(`DELETE FROM mac_sign_ins WHERE id = ? AND account_id = ? RETURNING `+signInColumns, id, accountID))
	if err != nil || signIn == nil {
		return nil, err
	}
	return signIn, tx.Commit()
}

// EndSignInsOf signs out every Mac signed in as the person, and says how
// many there were.
func (s *Store) EndSignInsOf(accountID int64) (int, error) {
	res, err := s.db.Exec(`DELETE FROM mac_sign_ins WHERE account_id = ?`, accountID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// MacOf is what the lab can say of a Mac it knows only by its machine id:
// the model its latest report names, "" when it sent none, and how many
// reports it sent. The machine id itself is shown to nobody.
func (s *Store) MacOf(machineID string) (model string, reports int, err error) {
	if err = s.db.QueryRow(`SELECT count(*) FROM reports WHERE machine_id = ?`, machineID).Scan(&reports); err != nil || reports == 0 {
		return "", 0, err
	}
	var name sql.NullString
	err = s.db.QueryRow(`SELECT body ->> '$.machine.model' FROM reports WHERE machine_id = ? ORDER BY id DESC LIMIT 1`, machineID).Scan(&name)
	return name.String, reports, err
}

// MacSignIns answers the two addresses of Mac sign-in. Its counts are in
// memory, as the upload's are.
type MacSignIns struct {
	store    *Store
	rules    *Rules
	now      func() time.Time
	github   GitHub
	people   People
	signIns  *limiter
	requests *limiter
}

// NewMacSignIns is Mac sign-in over store. Without github (nil) no Mac
// signs in; a Mac signed in already still asks its status and signs out.
func NewMacSignIns(store *Store, rules *Rules, now func() time.Time, github GitHub, people People) *MacSignIns {
	return &MacSignIns{store: store, rules: rules, now: now, github: github, people: people,
		signIns: newLimiter(SignInsPerHour), requests: newLimiter(SignInRequestsPerHour)}
}

func turnedAway(w http.ResponseWriter, status int, format string, args ...any) error {
	answerJSON(w, status, map[string]any{"error": fmt.Sprintf(format, args...)})
	return nil
}

// small reads a body of at most MaxSignInBytes, however its length was
// said.
func small(r *http.Request) (body []byte, ok bool) {
	if r.ContentLength > MaxSignInBytes {
		return nil, false
	}
	body, _ = io.ReadAll(io.LimitReader(r.Body, MaxSignInBytes+1))
	return body, len(body) <= MaxSignInBytes
}

// isVersion reports whether a document's version member is n, as the old
// site compared it: a number equal to n however it is written.
func isVersion(value any, n int64) bool {
	switch value := value.(type) {
	case *big.Int:
		return value.IsInt64() && value.Int64() == n
	case Float:
		return float64(value) == float64(n)
	}
	return false
}

func hasExactly(document *Object, keys ...string) bool {
	if document == nil || len(document.keys) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := document.members[key]; !ok {
			return false
		}
	}
	return true
}

var (
	reGitHubToken = regexp.MustCompile(`\A[A-Za-z0-9_.-]{1,255}\z`)
	reSignInName  = regexp.MustCompile(`\A[0-9]{1,20}\z`)
)

// SignInLine is what a sign-in means for a Mac's runs, in the tool's own
// words.
func SignInLine(handle string, verified bool) string {
	if verified {
		return "Signed in as @" + handle + ". This Mac's runs are published as Verified runs."
	}
	return "Signed in as @" + handle + ". This Mac's runs are published as Community runs; Verified runs come from Macs of the Omarchy M team."
}

// HandleSignIn answers POST /api/v1/tester_bindings:
//
//	{"binding_version": 1, "github_token": "...", "signature": {"public_key": ..., "signature": ...}}
//
// signed under NamespaceSignIn. addr is the address it came from, for its
// limit. The error is for labd's log: the request has been answered.
func (m *MacSignIns) HandleSignIn(w http.ResponseWriter, r *http.Request, addr string) error {
	if m.signIns.over(addr, m.now()) {
		return turnedAway(w, http.StatusTooManyRequests, "Too many sign-ins from your network: at most %d an hour. Try again later.", SignInsPerHour)
	}
	if m.github == nil {
		return turnedAway(w, http.StatusServiceUnavailable, "Mac sign-in isn't set up on this lab yet.")
	}
	body, ok := small(r)
	if !ok {
		return turnedAway(w, http.StatusRequestEntityTooLarge, "The sign-in is larger than %d KiB.", MaxSignInBytes>>10)
	}
	payload, err := Parse(body)
	if err != nil {
		return turnedAway(w, http.StatusBadRequest, "The sign-in is not valid JSON.")
	}
	document, _ := payload.(*Object)
	var token string
	if hasExactly(document, "binding_version", "github_token", "signature") && isVersion(document.members["binding_version"], signInVersion) {
		token, _ = document.members["github_token"].(string)
	}
	if !reGitHubToken.MatchString(token) {
		return turnedAway(w, http.StatusUnprocessableEntity, "The sign-in isn't what omarchy-m-test sends. Update omarchy-m-test and sign in again.")
	}
	key, err := VerifySignature(document, NamespaceSignIn)
	if err != nil {
		return turnedAway(w, http.StatusUnprocessableEntity, "The sign-in's signature is not valid, so it was refused (%s).", err)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	accountID, handle, err := m.github.TokenUser(ctx, token)
	if err != nil {
		return turnedAway(w, http.StatusUnauthorized, "GitHub didn't confirm the sign-in (%s). Run omarchy-m-test --sign-in again.", err)
	}
	m.github.Revoke(ctx, token)

	failed := func(err error) error {
		turnedAway(w, http.StatusInternalServerError, "The lab could not sign this Mac in. Try again later.")
		return fmt.Errorf("Mac sign-in of @%s: %w", handle, err)
	}
	blocked, err := m.people.Know(accountID, handle)
	if err != nil {
		return failed(err)
	}
	if blocked {
		return turnedAway(w, http.StatusForbidden, BlockedSentence)
	}
	signIn, err := m.store.signIn(m.rules.MachineID(key), accountID, handle, m.now())
	if err != nil {
		return failed(err)
	}
	// A Block ends the person's Mac sign-ins; one that landed while this
	// sign-in was on its way must not leave this one behind.
	if m.people.Blocked(accountID) {
		if _, err := m.store.endSignIn(*signIn); err != nil {
			return failed(err)
		}
		return turnedAway(w, http.StatusForbidden, BlockedSentence)
	}
	verified := m.people.OnMTeam(accountID)
	answerJSON(w, http.StatusCreated, map[string]any{"login": handle, "tester": verified, "message": SignInLine(handle, verified)})
	return nil
}

// HandleRequest answers POST /api/v1/tester_requests, what a Mac asks under
// its sign-in:
//
//	{"request_version": 1, "request": "status", "requested_at": <unix seconds>, "signature": {...}}
//	{"request_version": 1, "request": "sign-out", "sign_in": "<name>", "requested_at": ..., "signature": {...}}
//
// Both answer where the Mac stands afterwards:
//
//	{"signed_in": true, "login": "handle", "tester": true, "sign_in": "<name>"} or {"signed_in": false}
//
// and a sign-out also says whom it signed out ("signed_out", null when the
// Mac was not signed in). Reports already uploaded stay as they were
// stamped.
func (m *MacSignIns) HandleRequest(w http.ResponseWriter, r *http.Request, addr string) error {
	now := m.now()
	if m.requests.over(addr, now) {
		return turnedAway(w, http.StatusTooManyRequests, "Too many requests from your network: at most %d an hour. Try again later.", SignInRequestsPerHour)
	}
	body, ok := small(r)
	if !ok {
		return turnedAway(w, http.StatusRequestEntityTooLarge, "The request is larger than %d KiB.", MaxSignInBytes>>10)
	}
	payload, err := Parse(body)
	if err != nil {
		return turnedAway(w, http.StatusBadRequest, "The request is not valid JSON.")
	}
	document, _ := payload.(*Object)
	var request string
	var requestedAt *big.Int
	if document != nil && isVersion(document.members["request_version"], signInVersion) {
		requestedAt, _ = document.members["requested_at"].(*big.Int)
		switch name, _ := document.members["request"].(string); {
		case name == "status" && hasExactly(document, "request", "request_version", "requested_at", "signature"):
			request = name
		case name == "sign-out" && hasExactly(document, "request", "request_version", "requested_at", "sign_in", "signature") &&
			reSignInName.MatchString(rubyToS(document.members["sign_in"])):
			request = name
		}
	}
	if request == "" || requestedAt == nil {
		return turnedAway(w, http.StatusUnprocessableEntity, "The request isn't what omarchy-m-test sends. Update omarchy-m-test and try again.")
	}
	key, err := VerifySignature(document, NamespaceSignIn)
	if err != nil {
		return turnedAway(w, http.StatusUnprocessableEntity, "The request's signature is not valid, so it was refused (%s).", err)
	}
	age := new(big.Int).Sub(big.NewInt(now.Unix()), requestedAt)
	if age.Abs(age).Cmp(big.NewInt(int64(requestMaxAge/time.Second))) > 0 {
		return turnedAway(w, http.StatusUnprocessableEntity, "The request is dated more than a day from the lab's clock. Check this Mac's date and time, then try again.")
	}

	signIn, err := m.store.SignInOf(m.rules.MachineID(key))
	if err != nil {
		turnedAway(w, http.StatusInternalServerError, "The lab could not read this Mac's sign-in. Try again later.")
		return fmt.Errorf("Mac sign-in %s: %w", request, err)
	}
	switch {
	case request == "sign-out" && signIn == nil:
		answerJSON(w, http.StatusOK, map[string]any{"signed_in": false, "signed_out": nil})
	case request == "sign-out":
		// Only the sign-in the request names, and only while it is still that one.
		named, _ := document.members["sign_in"].(string)
		ended := false
		if named == signIn.Name() {
			if ended, err = m.store.endSignIn(*signIn); err != nil {
				turnedAway(w, http.StatusInternalServerError, "The lab could not sign this Mac out. Try again later.")
				return fmt.Errorf("Mac sign-out of @%s: %w", signIn.Handle, err)
			}
		}
		if !ended {
			return turnedAway(w, http.StatusConflict, "This Mac signed in again after this sign-out was made, so it was refused. Run omarchy-m-test --sign-out again.")
		}
		answerJSON(w, http.StatusOK, map[string]any{"signed_in": false, "signed_out": signIn.Handle})
	case signIn != nil:
		answerJSON(w, http.StatusOK, map[string]any{"signed_in": true, "login": signIn.Handle, "tester": m.people.OnMTeam(signIn.AccountID), "sign_in": signIn.Name()})
	default:
		answerJSON(w, http.StatusOK, map[string]any{"signed_in": false})
	}
	return nil
}
