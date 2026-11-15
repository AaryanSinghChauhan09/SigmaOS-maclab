package server

// The caller log holds one line for every request labd answers: when, the
// caller's address, the person or Mac it came from, the edge's ray id, and
// what was asked and answered. It never holds a query string, where a
// report's deletion link carries its secret, nor a credential. Lines are
// kept callerLogKeep, then deleted.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	callerLogKeep = 30 * 24 * time.Hour
	callerLogDay  = "2006-01-02"
)

// callerLine is one request in the caller log.
type callerLine struct {
	Time   time.Time `json:"time"`
	Addr   string    `json:"addr"`
	Who    string    `json:"who,omitempty"` // mac:<name>, person:<name> or builder[:<name>]
	Ray    string    `json:"ray,omitempty"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Status int       `json:"status"`
	Millis int64     `json:"ms"`
	Agent  string    `json:"agent,omitempty"` // the caller's User-Agent
	// Refused marks a request whose credential labd did not admit, which
	// the limiter counted against Addr (refused.go).
	Refused bool `json:"refused,omitempty"`
}

// callerLog writes the lines to one file for each UTC day under dir. Once
// labd is stopping it still writes the requests that end then to the file
// it has open, and makes no new one.
type callerLog struct {
	dir     string
	log     *log.Logger
	stopped <-chan struct{}

	mu  sync.Mutex
	day string
	f   *os.File

	// pruning is held by the one prune at a time; mu only while it must be.
	pruning sync.Mutex
}

func openCallerLog(dir string, logger *log.Logger, stopped <-chan struct{}) (*callerLog, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &callerLog{dir: dir, log: logger, stopped: stopped}, nil
}

func (l *callerLog) closeLocked() {
	if l.f != nil {
		l.f.Close()
	}
	l.f, l.day = nil, ""
}

func (l *callerLog) write(line callerLine) {
	line.Time = line.Time.UTC()
	b, err := json.Marshal(line)
	if err != nil {
		return
	}
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	if day := line.Time.Format(callerLogDay); day != l.day {
		select {
		case <-l.stopped:
			return
		default:
		}
		l.closeLocked()
		f, err := os.OpenFile(filepath.Join(l.dir, day+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			l.log.Printf("caller log: %v", err)
			return
		}
		l.f, l.day = f, day
	}
	if _, err := l.f.Write(b); err != nil {
		l.log.Printf("caller log: %v", err)
		l.closeLocked()
	}
}

// prune deletes every line older than callerLogKeep at now: a day's file
// when all of it is, and the older lines of the one day that straddles.
func (l *callerLog) prune(now time.Time) {
	cutoff := now.UTC().Add(-callerLogKeep)
	l.pruning.Lock()
	defer l.pruning.Unlock()
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		l.log.Printf("caller log: %v", err)
		return
	}
	for _, e := range entries {
		name := e.Name()
		day, err := time.Parse(callerLogDay, strings.TrimSuffix(name, ".log"))
		if err != nil || !strings.HasSuffix(name, ".log") || !day.Before(cutoff) {
			continue
		}
		if !day.Add(24 * time.Hour).After(cutoff) {
			err = l.removeDay(name)
		} else {
			err = l.dropOlderLines(name, cutoff)
		}
		if err != nil {
			l.log.Printf("caller log: %v", err)
		}
	}
}

func (l *callerLog) removeDay(name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if name == l.day+".log" {
		l.closeLocked()
	}
	return os.Remove(filepath.Join(l.dir, name))
}

// dropOlderLines rewrites a day's file without the lines from before cutoff.
// The file is read as a stream into a new one, with no request waiting; only
// the lines written meanwhile, and the new file taking the name, hold the
// lock a request's line needs.
func (l *callerLog) dropOlderLines(name string, cutoff time.Time) error {
	file := filepath.Join(l.dir, name)
	src, err := os.Open(file)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := file + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	defer dst.Close()
	c := &lineCopy{src: src, rd: bufio.NewReaderSize(src, lineCopyBuffer), w: bufio.NewWriterSize(dst, lineCopyBuffer), cutoff: cutoff}
	if err := c.copy(false); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := c.copy(true); err != nil {
		return err
	}
	if c.dropped == 0 {
		return nil
	}
	if name == l.day+".log" {
		l.closeLocked()
	}
	if c.kept == 0 {
		return os.Remove(file)
	}
	if err := c.w.Flush(); err != nil {
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// lineCopyBuffer is all of a day a prune holds at a time. A line is far
// shorter: its fields are clipped where it is written.
const lineCopyBuffer = 64 << 10

// lineCopy copies the lines of src from cutoff on to w. A line that can't
// be read as one, however long, has no time to keep it by, and is dropped.
type lineCopy struct {
	src           *os.File
	rd            *bufio.Reader
	w             *bufio.Writer
	cutoff        time.Time
	kept, dropped int
}

// copy reads on to the end of src. Unless last, an end with no newline is
// left for the next copy: its line may still be being written.
func (c *lineCopy) copy(last bool) error {
	for {
		n, whole := 0, true
		b, err := c.rd.ReadSlice('\n')
		for err == bufio.ErrBufferFull {
			n, whole = n+len(b), false
			b, err = c.rd.ReadSlice('\n')
		}
		n += len(b)
		if err != nil && err != io.EOF {
			return err
		}
		if err == io.EOF && n == 0 {
			return nil
		}
		if err == io.EOF && !last {
			if _, err := c.src.Seek(-int64(n), io.SeekCurrent); err != nil {
				return err
			}
			c.rd.Reset(c.src)
			return nil
		}
		var line struct {
			Time time.Time `json:"time"`
		}
		if whole && json.Unmarshal(b, &line) == nil && !line.Time.Before(c.cutoff) {
			c.w.Write(bytes.TrimSuffix(b, []byte("\n")))
			c.w.WriteByte('\n')
			c.kept++
		} else {
			c.dropped++
		}
		if err == io.EOF {
			return nil
		}
	}
}

// callerLogLoop prunes the caller log at start and every hour.
func (s *Server) callerLogLoop() {
	s.callers.prune(s.now())
	t := s.clock().NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C():
			s.callers.prune(s.now())
		}
	}
}

// call is what labd knows of a request's caller while it serves it.
type call struct {
	addr, ray string
	who       string
	refused   bool
	// path, when set, is the address the line keeps instead (notePath).
	path string
}

type callKey struct{}

// noteCaller records who r came from, for the caller log: "mac:<name>",
// "person:<name>" or "builder:<name>".
func noteCaller(r *http.Request, who string) {
	if c, ok := r.Context().Value(callKey{}).(*call); ok {
		c.who = who
	}
}

// callerAddr is the address r's caller is at: what the edge names in
// CF-Connecting-IP on the tunnel's socket, the only place that header is
// believed, and the connection's own address anywhere else. It is "" when
// the edge named none, or the caller dialled the socket from this machine.
func callerAddr(r *http.Request) string {
	if c, ok := r.Context().Value(callKey{}).(*call); ok {
		return c.addr
	}
	if viaTunnel(r) {
		if a, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil {
			return a.Unmap().WithZone("").String()
		}
		return ""
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap().WithZone("").String()
	}
	return ""
}

var reRay = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)

// rayID is the edge's id for r, believed on the tunnel's socket only.
func rayID(r *http.Request) string {
	if ray := r.Header.Get("CF-Ray"); viaTunnel(r) && reRay.MatchString(ray) {
		return ray
	}
	return ""
}

// answer is the writer a handler gets: it remembers the status for the
// caller log, and has the answer's cache policy stated before its headers
// go (see statePolicy).
type answer struct {
	http.ResponseWriter
	status int
	// before is called once, with the status, before the headers are sent.
	before func(status int)
}

func (a *answer) starts(code int) {
	if a.status != 0 {
		return
	}
	a.status = code
	if a.before != nil {
		a.before(code)
	}
}

func (a *answer) WriteHeader(code int) {
	if code >= 200 || code == http.StatusSwitchingProtocols {
		a.starts(code)
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a *answer) Write(b []byte) (int, error) {
	a.starts(http.StatusOK)
	return a.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the writer below.
func (a *answer) Unwrap() http.ResponseWriter { return a.ResponseWriter }

// Flush flushes, for the handlers that ask the writer itself.
func (a *answer) Flush() {
	a.starts(http.StatusOK)
	if f, ok := a.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ReadFrom keeps the writer's own way of sending a file.
func (a *answer) ReadFrom(src io.Reader) (int64, error) {
	a.starts(http.StatusOK)
	return io.Copy(a.ResponseWriter, src)
}

// logged serves r through h and writes its line when h returns: a stream's
// line is written when the stream ends.
func (s *Server) logged(w http.ResponseWriter, r *http.Request, h func(*answer, *http.Request)) {
	start := s.now()
	c := &call{addr: callerAddr(r), ray: rayID(r)}
	a := &answer{ResponseWriter: w}
	defer func() {
		// A handler that wrote nothing answers 200 as it returns.
		a.starts(http.StatusOK)
		path := r.URL.Path
		if c.path != "" {
			path = c.path
		}
		s.callers.write(callerLine{Time: start, Addr: c.addr, Who: c.who, Ray: c.ray, Method: clip(r.Method, 16), Path: clip(path, 512),
			Status: a.status, Millis: s.since(start).Milliseconds(), Agent: clip(r.UserAgent(), 200), Refused: c.refused})
	}()
	h(a, r.WithContext(context.WithValue(r.Context(), callKey{}, c)))
}

func clip(s string, n int) string {
	if len(s) > n {
		return strings.ToValidUTF8(s[:n], "")
	}
	return s
}
