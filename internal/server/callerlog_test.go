package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/omacom/omarchy-maclab/internal/agent"
	"github.com/omacom/omarchy-maclab/internal/api"
)

// linesForTest is every line of the caller log, oldest first.
func (l *callerLog) linesForTest(t *testing.T) []callerLine {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(l.dir, "*.log"))
	if err != nil {
		t.Fatal(err)
	}
	var out []callerLine
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var line callerLine
			if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
				t.Fatalf("%s: %q: %v", name, sc.Text(), err)
			}
			out = append(out, line)
		}
		f.Close()
	}
	return out
}

// clearForTest empties the caller log.
func (l *callerLog) clearForTest(t *testing.T) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closeLocked()
	if err := os.RemoveAll(l.dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

// rawForTest is the caller log as it is on disk.
func (l *callerLog) rawForTest(t *testing.T) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(l.dir, "*"))
	var b strings.Builder
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
	}
	return b.String()
}

// Every request leaves one line: the caller's address, the person or Mac it
// came from, and the edge's ray id.
func TestTheCallerLogNamesAddressCallerAndRay(t *testing.T) {
	s := aliasServer(t, nil)
	testBuilder(t, s)
	h := s.Handler()
	enr, err := s.store.addTokenForTest("m1-pro")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "http://"+loopback+"/api/agent/enroll", strings.NewReader(enrollJSON(enr)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var enrolled api.EnrollResponse
	if err := json.Unmarshal(w.Body.Bytes(), &enrolled); err != nil || enrolled.Secret == "" {
		t.Fatalf("enroll: %d %s", w.Code, w.Body)
	}
	mac := enrolled.Secret
	if lines := s.callers.linesForTest(t); len(lines) != 1 || lines[0].Addr != "192.0.2.1" || lines[0].Who != "" || lines[0].Path != "/api/agent/enroll" {
		t.Fatalf("the enrollment's line: %+v", lines)
	}
	s.callers.clearForTest(t)

	ask := func(method, target, bearer, addr, ray string) int {
		t.Helper()
		hdr := map[string]string{"X-Forwarded-Proto": "https", "CF-Connecting-IP": addr, "CF-Ray": ray, "User-Agent": "lab/0.2.0"}
		if bearer != "" {
			hdr["Authorization"] = "Bearer " + bearer
		}
		return edgeAt(h, method, labHost, target, viaProxy, hdr).Code
	}
	if code := ask("GET", "/api/devices?all=1", "lab_adm_secret", "203.0.113.7", "8f1e2d3c4b5a6978-BNE"); code != 200 {
		t.Fatalf("a Maintainer's list: %d", code)
	}
	ask("POST", "/api/agent/checkin", mac, "198.51.100.20", "8f1e2d3c4b5a6979-SYD")
	ask("GET", "/api/devices", "mnt_wrong", "198.51.100.99", "8f1e2d3c4b5a697a-SYD")
	ask("POST", "/api/builder/poll", "bld", "127.0.0.1", "")
	ask("GET", "/healthz", "", "2001:db8::7", "8f1e2d3c4b5a697b-LAX")

	lines := s.callers.linesForTest(t)
	if len(lines) != 5 {
		t.Fatalf("%d lines, want 5: %+v", len(lines), lines)
	}
	for i, want := range []callerLine{
		{Addr: "203.0.113.7", Who: "person:ada", Ray: "8f1e2d3c4b5a6978-BNE", Method: "GET", Path: "/api/devices", Status: 200, Agent: "lab/0.2.0"},
		{Addr: "198.51.100.20", Who: "mac:m1-pro", Ray: "8f1e2d3c4b5a6979-SYD", Method: "POST", Path: "/api/agent/checkin", Agent: "lab/0.2.0"},
		{Addr: "198.51.100.99", Who: "", Ray: "8f1e2d3c4b5a697a-SYD", Method: "GET", Path: "/api/devices", Status: 401, Agent: "lab/0.2.0", Refused: true},
		{Addr: "127.0.0.1", Who: "builder:" + testBuilderName, Method: "POST", Path: "/api/builder/poll", Agent: "lab/0.2.0"},
		{Addr: "2001:db8::7", Ray: "8f1e2d3c4b5a697b-LAX", Method: "GET", Path: "/healthz", Status: 200, Agent: "lab/0.2.0"},
	} {
		got := lines[i]
		if got.Time.IsZero() || time.Since(got.Time) > time.Minute {
			t.Errorf("line %d: time %v", i, got.Time)
		}
		if want.Status == 0 {
			want.Status = got.Status
		}
		got.Time, got.Millis = time.Time{}, 0
		if got != want {
			t.Errorf("line %d:\n got %+v\nwant %+v", i, got, want)
		}
	}
	if raw := s.callers.rawForTest(t); strings.Contains(raw, "lab_adm_secret") || strings.Contains(raw, mac) || strings.Contains(raw, "mnt_wrong") || strings.Contains(raw, "Bearer bld") {
		t.Errorf("the caller log holds a credential:\n%s", raw)
	}
}

// A report's deletion link carries its secret in the query, and no query
// reaches the caller log: not from the Lab address, not from the Alias's
// forward, not from a refused name.
func TestADeletionLinksSecretNeverReachesTheCallerLog(t *testing.T) {
	s := aliasServer(t, nil)
	h := s.Handler()
	const secret = "q7Jx2mVtY0-deletion-secret_4Zk"
	for _, host := range []string{labHost, aliasHost, "evil.example"} {
		for _, target := range []string{
			"/reports/8c1f02ab/deletion?token=" + secret,
			"/reports/8c1f02ab?token=" + secret + "&x=1",
			"/api/reports/8c1f02ab?deletion_token=" + secret,
		} {
			edgeAt(h, "GET", host, target, viaProxy, with(fromIngress, "Referer", "https://lab.omarchy-mac.org/reports/8c1f02ab/deletion?token="+secret))
			edgeAt(h, "DELETE", host, target, viaProxy, fromIngress)
		}
	}
	raw := s.callers.rawForTest(t)
	if n := strings.Count(raw, "\n"); n != 18 {
		t.Fatalf("%d lines, want 18:\n%s", n, raw)
	}
	if strings.Contains(raw, secret) || strings.Contains(raw, "token=") {
		t.Errorf("the caller log holds the deletion link's secret:\n%s", raw)
	}
	if !strings.Contains(raw, `"path":"/reports/8c1f02ab/deletion"`) {
		t.Errorf("the caller log lost the path:\n%s", raw)
	}
}

// Lines are kept 30 days, then deleted: to the line, not to the day.
func TestCallerLogLinesOlderThan30DaysAreDeleted(t *testing.T) {
	start := time.Date(2026, 11, 14, 22, 0, 0, 0, time.UTC)
	clock := agent.NewFakeClock(start)
	at := func(d time.Duration) { clock.Advance(start.Add(d).Sub(clock.Now())) }
	s := aliasServer(t, func(c *Config) { c.Clock = clock })
	h := s.Handler()
	get := func(target string) {
		t.Helper()
		if w := edgeAt(h, "GET", labHost, target, viaProxy, fromIngress); w.Code != 200 {
			t.Fatalf("GET %s: %d", target, w.Code)
		}
	}
	paths := func() string {
		var p []string
		for _, l := range s.callers.linesForTest(t) {
			p = append(p, l.Path)
		}
		return strings.Join(p, " ")
	}
	get("/healthz") // 14 November 22:00
	at(90 * time.Minute)
	get("/terms") // 14 November 23:30
	at(3 * time.Hour)
	get("/privacy") // 15 November 01:00
	at(20 * 24 * time.Hour)
	get("/") // 4 December
	if got := paths(); got != "/healthz /terms /privacy /" {
		t.Fatalf("the caller log: %s", got)
	}

	at(30*24*time.Hour - time.Second)
	s.callers.prune(s.now())
	if got := paths(); got != "/healthz /terms /privacy /" {
		t.Errorf("a line under 30 days old was deleted: %s", got)
	}
	at(30*24*time.Hour + time.Second)
	s.callers.prune(s.now())
	if got := paths(); got != "/terms /privacy /" {
		t.Errorf("30 days and a second after the first line: %s", got)
	}
	at(30*24*time.Hour + 2*time.Hour)
	s.callers.prune(s.now())
	if got := paths(); got != "/privacy /" {
		t.Errorf("30 days and two hours after the first line: %s", got)
	}
	if files, _ := filepath.Glob(filepath.Join(s.callers.dir, "*")); len(files) != 2 {
		t.Errorf("a day with no line left kept its file: %v", files)
	}
	// labd goes on writing after a prune.
	get("/healthz")
	if got := paths(); got != "/privacy / /healthz" {
		t.Errorf("after a prune: %s", got)
	}
	at(51 * 24 * time.Hour)
	s.callers.prune(s.now())
	if got := paths(); got != "/healthz" {
		t.Errorf("51 days after the first line: %s", got)
	}

	// The log is for the account labd runs as.
	st, err := os.Stat(s.callers.dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("the caller log's directory: %v %v", st.Mode(), err)
	}
	files, _ := filepath.Glob(filepath.Join(s.callers.dir, "*.log"))
	for _, f := range files {
		if st, err := os.Stat(f); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, st.Mode(), err)
		}
	}
}

// A stream is logged when it ends, with the status it began with, and stays
// a stream: the writer under the log still flushes.
func TestTheCallerLogKeepsStreamsFlushing(t *testing.T) {
	s := aliasServer(t, nil)
	var flushed bool
	h := s.edge(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("the writer a handler gets can't flush")
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fl.Flush()
		flushed = http.NewResponseController(w).Flush() == nil
		noteCaller(r, "mac:m2-max")
	}))
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://"+labHost+"/api/stream", nil)
	h.ServeHTTP(w, r.WithContext(onTunnel(r.Context())))
	if !flushed || !w.Flushed {
		t.Error("the stream didn't flush")
	}
	lines := s.callers.linesForTest(t)
	if len(lines) != 1 || lines[0].Status != http.StatusAccepted || lines[0].Who != "mac:m2-max" {
		t.Errorf("the stream's line: %+v", lines)
	}
}

// A request that ends while labd is stopping is still logged, to the file
// labd has open; no file is made then, so a late request can't bring a
// removed data directory back.
func TestAStoppingLabdMakesNoCallerLogFile(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := newWith("ada", "lab_adm_secret", ctx, Config{DataDir: dir}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", labdAt+"/healthz", nil))
	if n := len(s.callers.linesForTest(t)); n != 1 {
		t.Fatalf("%d lines before labd stopped", n)
	}
	cancel()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", labdAt+"/healthz", nil))
	if n := len(s.callers.linesForTest(t)); n != 2 {
		t.Fatalf("%d lines after a request that ended while labd stopped, want 2", n)
	}
	if err := os.RemoveAll(s.callers.dir); err != nil {
		t.Fatal(err)
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", labdAt+"/healthz", nil))
	if _, err := os.Stat(s.callers.dir); !os.IsNotExist(err) {
		t.Errorf("a request after labd stopped wrote the caller log again: %v", err)
	}
}

// dayLine is a caller log line as it is on disk.
func dayLine(t *testing.T, at time.Time, path string) string {
	t.Helper()
	b, err := json.Marshal(callerLine{Time: at, Addr: "203.0.113.9", Method: "GET", Path: path, Status: 200})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// The day that straddles 30 days is pruned as a stream, and no request waits
// for it: here the day is a pipe whose lines have not come yet, and a request
// is logged meanwhile.
func TestAPruneReadingADayDoesNotMakeARequestWait(t *testing.T) {
	l, err := openCallerLog(t.TempDir(), log.New(io.Discard, "", 0), make(chan struct{}))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	day := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	file := filepath.Join(l.dir, day.Format(callerLogDay)+".log")
	if err := syscall.Mkfifo(file, 0o600); err != nil {
		t.Skipf("no pipe here: %v", err)
	}
	pruned := make(chan struct{})
	go func() {
		l.prune(now)
		close(pruned)
	}()
	// The pipe opens for writing once the prune has it open for reading.
	pipe, err := os.OpenFile(file, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	wrote := make(chan struct{})
	go func() {
		l.write(callerLine{Time: now, Method: "GET", Path: "/healthz", Status: 200})
		close(wrote)
	}()
	select {
	case <-wrote:
	case <-time.After(5 * time.Second):
		t.Error("a request's line waited for the prune to read a day")
	}
	io.WriteString(pipe, dayLine(t, day.Add(10*time.Hour), "/old")+dayLine(t, day.Add(13*time.Hour), "/kept"))
	pipe.Close()
	select {
	case <-pruned:
	case <-time.After(10 * time.Second):
		t.Fatal("the prune did not end")
	}
	<-wrote
	var paths []string
	for _, line := range l.linesForTest(t) {
		paths = append(paths, line.Path)
	}
	if got := strings.Join(paths, " "); got != "/kept /healthz" {
		t.Errorf("after the prune the caller log holds %q, want /kept /healthz", got)
	}
	if st, err := os.Stat(file); err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		t.Errorf("the pruned day: %v %v", st, err)
	}
	if left, _ := filepath.Glob(filepath.Join(l.dir, "*.tmp")); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

// A day's file is never held whole: what cannot be a line, however long, is
// dropped, and the lines after it are still read.
func TestAPruneKeepsTheLinesAfterWhatIsTooLongToBeALine(t *testing.T) {
	l, err := openCallerLog(t.TempDir(), log.New(io.Discard, "", 0), make(chan struct{}))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	day := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	file := filepath.Join(l.dir, day.Format(callerLogDay)+".log")
	text := dayLine(t, day.Add(10*time.Hour), "/old") + strings.Repeat("x", 2<<20) + "\n" +
		dayLine(t, day.Add(13*time.Hour), "/kept") + "\n" + dayLine(t, day.Add(14*time.Hour), "/kept-too")
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	l.prune(now)
	want := dayLine(t, day.Add(13*time.Hour), "/kept") + dayLine(t, day.Add(14*time.Hour), "/kept-too")
	if got := l.rawForTest(t); got != want {
		t.Errorf("after the prune the day holds %d bytes, want the two lines from 12:00 on:\n%.300s", len(got), got)
	}
}

// A line written to the day while it is being pruned is not lost: the copy
// goes on from where it stopped, and a line still half written waits for its end.
func TestALineWrittenWhileItsDayIsPrunedIsKept(t *testing.T) {
	dir := t.TempDir()
	day := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	file := filepath.Join(dir, "day.log")
	add := func(text string) {
		t.Helper()
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := io.WriteString(f, text); err != nil {
			t.Fatal(err)
		}
	}
	add(dayLine(t, day.Add(10*time.Hour), "/old") + dayLine(t, day.Add(13*time.Hour), "/kept"))
	src, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	var out strings.Builder
	c := &lineCopy{src: src, rd: bufio.NewReaderSize(src, lineCopyBuffer), w: bufio.NewWriter(&out), cutoff: day.Add(12 * time.Hour)}
	copied := func(last bool, want string) {
		t.Helper()
		if err := c.copy(last); err != nil {
			t.Fatal(err)
		}
		c.w.Flush()
		if out.String() != want {
			t.Fatalf("copied so far:\n%s\nwant:\n%s", out.String(), want)
		}
	}
	want := dayLine(t, day.Add(13*time.Hour), "/kept")
	copied(false, want)

	late := dayLine(t, day.Add(23*time.Hour), "/late")
	torn := dayLine(t, day.Add(23*time.Hour+time.Minute), "/torn")
	add(late + torn[:20])
	want += late
	copied(false, want)
	add(torn[20:] + torn[:20])
	want += torn
	copied(false, want)
	// What never got its end is no line.
	copied(true, want)
	if c.kept != 3 || c.dropped != 2 {
		t.Errorf("kept %d and dropped %d lines, want 3 and 2", c.kept, c.dropped)
	}
}
