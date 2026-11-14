package web

// The files a page loads live in static/. Each is served at an address that
// carries a fingerprint of its content, so it can be kept for a year: a
// changed file has a new address. Text files are also kept beside
// themselves compressed at Brotli's highest level (name.br), made ahead of
// time by `go test ./internal/web -update`: labd compresses nothing itself.

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed static
var staticFiles embed.FS

type asset struct {
	name   string // app.css
	served string // app-0123456789abcdef.css
	ctype  string
	body   []byte
	br     []byte // nil when the file is not kept compressed
}

// compressed are the kinds of file kept as Brotli too; the rest (fonts,
// PNG) are compressed already.
var compressed = map[string]bool{".css": true, ".js": true, ".svg": true}

// contentTypes are the kinds of file static/ may hold. They are named here
// because the machine labd runs on may say otherwise.
var contentTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".svg":   "image/svg+xml",
	".png":   "image/png",
	".woff2": "font/woff2",
}

// atRoot are the files crawlers ask for by a fixed name.
var atRoot = []string{"robots.txt"}

var reCSSURL = regexp.MustCompile(`url\("([^")]+)"\)`)

func (s *Site) loadAssets() error {
	s.assets, s.served = map[string]*asset{}, map[string]*asset{}
	entries, err := fs.ReadDir(staticFiles, "static")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		switch ext := path.Ext(e.Name()); {
		case ext == ".br" || ext == ".txt":
		case contentTypes[ext] == "":
			return fmt.Errorf("web: static/%s is a kind of file the interface does not serve", e.Name())
		default:
			names = append(names, e.Name())
		}
	}
	// A stylesheet names the files it loads, so it comes after them.
	sort.SliceStable(names, func(i, j int) bool { return path.Ext(names[i]) != ".css" && path.Ext(names[j]) == ".css" })
	for _, name := range names {
		body, err := staticFiles.ReadFile("static/" + name)
		if err != nil {
			return err
		}
		ext := path.Ext(name)
		if ext == ".css" {
			var missing string
			body = reCSSURL.ReplaceAllFunc(body, func(m []byte) []byte {
				ref := string(reCSSURL.FindSubmatch(m)[1])
				a := s.assets[ref]
				if a == nil {
					missing = ref
					return m
				}
				return []byte(`url("` + a.served + `")`)
			})
			if missing != "" {
				return fmt.Errorf("web: %s loads %s, which static/ does not hold", name, missing)
			}
		}
		sum := sha256.Sum256(body)
		a := &asset{name: name, body: body, ctype: contentTypes[ext],
			served: strings.TrimSuffix(name, ext) + "-" + hex.EncodeToString(sum[:8]) + ext}
		if compressed[ext] {
			if a.br, err = staticFiles.ReadFile("static/" + name + ".br"); err != nil {
				a.br = nil
			}
		}
		s.assets[name], s.served[a.served] = a, a
	}
	return nil
}

// Asset is the address a page loads the file called name from.
func (s *Site) Asset(name string) (string, error) {
	a := s.assets[name]
	if a == nil {
		return "", fmt.Errorf("web: static/ holds no %s", name)
	}
	return s.base + "/assets/" + a.served, nil
}

// Register adds the routes of the interface's own files.
func (s *Site) Register(m *http.ServeMux) {
	m.HandleFunc("GET /assets/{file}", s.serveAsset)
	for _, name := range atRoot {
		body, err := staticFiles.ReadFile("static/" + name)
		if err != nil {
			continue
		}
		m.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Write(body)
		})
	}
}

func (s *Site) serveAsset(w http.ResponseWriter, r *http.Request) {
	a := s.served[r.PathValue("file")]
	if a == nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	body := a.body
	if a.br != nil {
		h.Set("Vary", "Accept-Encoding")
		if acceptsBrotli(r.Header.Get("Accept-Encoding")) {
			h.Set("Content-Encoding", "br")
			body = a.br
		}
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		w.Write(body)
	}
}

func acceptsBrotli(header string) bool {
	for _, part := range strings.Split(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "br") {
			continue
		}
		q := strings.TrimSpace(params)
		if v, ok := strings.CutPrefix(q, "q="); ok {
			f, err := strconv.ParseFloat(v, 64)
			return err == nil && f > 0
		}
		return true
	}
	return false
}
