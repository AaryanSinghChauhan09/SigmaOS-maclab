package server

// The opening: the one step that takes the lab from Preview to Live (the
// Cutover's step 25, the point of no return). It is asked on the ops
// socket, which only the lab account can open, by scripts/cutover/open on
// the mini. In that step uploads and Mac sign-in are accepted, Preview's
// banner and noindex go, the home page says for 30 days that the old site
// is now here, and the install line serves the latest Lab release.
//
// Live is a row in labd.db, not a flag: the LaunchDaemon file of the Lab
// release the mini runs still says --preview, and so does the next one
// Deployed. A labd that finds the row is open whatever it was started with,
// and nothing takes the row away.

import (
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// movedHereFor is how long after the lab opens its home page names the old
// site.
const movedHereFor = 30 * 24 * time.Hour

// liveAt is when the lab opened; zero when it has not.
func (st *store) liveAt() (time.Time, error) {
	var at int64
	switch err := st.db.QueryRow(`SELECT at FROM live WHERE one = 1`).Scan(&at); {
	case errors.Is(err, sql.ErrNoRows):
		return time.Time{}, nil
	case err != nil:
		return time.Time{}, err
	}
	return time.UnixMicro(at), nil
}

// Live reports when the lab opened, and whether it has.
func (s *Server) Live() (time.Time, bool) {
	at := s.openedAt.Load()
	return time.UnixMicro(at), at != 0
}

// movedHere reports whether the home page names the old site: from the
// opening, for 30 days.
func (s *Server) movedHere() bool {
	at, ok := s.Live()
	return ok && s.now().Before(at.Add(movedHereFor))
}

// open is the opening. mTeam is how many M team members the owner marked on
// People, as whoever asks counts them: the lab opens only when it counts
// the same. Asked again once the lab is Live it changes nothing and says
// when the lab opened.
func (s *Server) open(mTeam int) (time.Time, error) {
	s.opening.Lock()
	defer s.opening.Unlock()
	if at, ok := s.Live(); ok {
		return at, nil
	}
	if !s.cfg.Preview {
		return time.Time{}, refuse(http.StatusConflict, "this labd is not in Preview (it was started without --preview): there is nothing to open")
	}
	if s.uploads == nil {
		return time.Time{}, refuse(http.StatusConflict, "this labd has no machine-id key (--machine-id-key): opened, it would refuse every report")
	}
	if s.cfg.GitHub == nil {
		return time.Time{}, refuse(http.StatusConflict, "sign-in is off: this labd has no OAuth app (--sign-in), so nobody could sign in to the open lab")
	}
	var marked int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM people WHERE m_team = 1 AND blocked_at IS NULL`).Scan(&marked); err != nil {
		return time.Time{}, err
	}
	if marked != mTeam {
		return time.Time{}, refuse(http.StatusConflict, "%d people are marked M team, not %d: mark them on People first (the Cutover's step 24)", marked, mTeam)
	}
	at := s.now().Truncate(time.Second)
	if _, err := s.store.db.Exec(`INSERT INTO live (one, at, m_team, release) VALUES (1, ?, ?, ?)`, at.UnixMicro(), marked, s.cfg.Release); err != nil {
		return time.Time{}, err
	}
	s.openedAt.Store(at.UnixMicro())
	s.log.Printf("the lab is Live: opened with %d M team members marked. Reports and Mac sign-in are accepted", marked)
	return at, nil
}

// stage is what GET /stage on the ops socket answers.
func (s *Server) stage() string {
	if at, ok := s.Live(); ok {
		return "live " + at.UTC().Format(time.RFC3339)
	}
	if s.cfg.Preview {
		return "preview"
	}
	return "open"
}

// opsOpen is POST /open?m-team=N on the ops socket: N is the number of M
// team members marked.
func (s *Server) opsOpen(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.URL.Query().Get("m-team"))
	if err != nil || n < 0 {
		httpErr(w, http.StatusBadRequest, "say how many M team members are marked: /open?m-team=N")
		return
	}
	at, err := s.open(n)
	var no *refusal
	switch {
	case errors.As(err, &no):
		httpErr(w, no.code, "%s", no.msg)
	case err != nil:
		s.log.Printf("the opening: %v", err)
		httpErr(w, http.StatusInternalServerError, "the opening could not be recorded: the lab is as it was")
	default:
		io.WriteString(w, "live "+at.UTC().Format(time.RFC3339)+"\n")
	}
}

// StageOf reads a data directory's stage without a labd: "live <time>" or
// "not opened". It changes nothing; a labd may be running on the directory.
func StageOf(dataDir string) (string, error) {
	path, err := filepath.Abs(filepath.Join(dataDir, "labd.db"))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "not opened", nil
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path}).String()+"?mode=ro&_pragma=busy_timeout(5000)&_query_only=1")
	if err != nil {
		return "", err
	}
	defer db.Close()
	var at int64
	switch err := db.QueryRow(`SELECT at FROM live WHERE one = 1`).Scan(&at); {
	case errors.Is(err, sql.ErrNoRows), err != nil && strings.Contains(err.Error(), "no such table"):
		return "not opened", nil
	case err != nil:
		return "", err
	}
	return "live " + time.UnixMicro(at).UTC().Format(time.RFC3339), nil
}
