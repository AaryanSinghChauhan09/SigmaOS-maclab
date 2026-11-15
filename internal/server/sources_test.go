package server

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omacom/omarchy-maclab/internal/api"
)

func TestParseSource(t *testing.T) {
	hosts := []string{"git.example.org"}
	cases := []struct{ in, repo, ref string }{
		{"https://github.com/iconidentify/aurora-linux", "https://github.com/iconidentify/aurora-linux", ""},
		{"https://github.com/iconidentify/aurora-linux/tree/custom/sep", "https://github.com/iconidentify/aurora-linux", "custom/sep"},
		{"github.com/AsahiLinux/linux/tree/asahi-wip", "https://github.com/AsahiLinux/linux", "asahi-wip"},
		{"https://github.com/o/r/commit/17cba00e43b94ba6b5c64be7cbe3db9234a41f84", "https://github.com/o/r", "17cba00e43b94ba6b5c64be7cbe3db9234a41f84"},
		{"https://github.com/o/r/pull/42", "https://github.com/o/r", "refs/pull/42/head"},
		{"https://github.com/o/r.git#dev", "https://github.com/o/r", "dev"},
		{"https://GitHub.com/o/r", "https://github.com/o/r", ""},
		{"https://git.example.org/linux.git@v7.1", "https://git.example.org/linux.git", "v7.1"},
		{"https://git.example.org/linux.git#topic/x", "https://git.example.org/linux.git", "topic/x"},
	}
	for _, c := range cases {
		s, err := parseSource(c.in, hosts...)
		if err != nil || s.Repo != c.repo || s.Ref != c.ref {
			t.Errorf("%s: got %q %q %v, want %q %q", c.in, s.Repo, s.Ref, err, c.repo, c.ref)
		}
	}
}

// Only https://github.com/<owner>/<repo> is a source; another host only
// when the server's configuration lists it; an address with credentials
// never, and its refusal does not repeat them.
func TestOnlyPublicSourcesFromListedHosts(t *testing.T) {
	listed := []string{"git.example.org"}
	for _, c := range []struct {
		in    string
		hosts []string
		ok    bool
	}{
		{"https://github.com/o/r", nil, true},
		{"https://git.example.org/linux.git", listed, true},
		{"https://git.example.org/linux.git", nil, false},
		{"https://git.example.org/linux.git", []string{"example.org"}, false},
		{"https://sub.git.example.org/linux.git", listed, false},
		{"https://git.example.org:8443/linux.git", listed, false},
		{"https://gitlab.com/o/r", nil, false},
		{"https://github.com.evil.example/o/r", nil, false},
		{"https://evil.example/github.com/o/r", nil, false},
		{"http://github.com/o/r", nil, false},
		{"git://github.com/o/r", nil, false},
		{"ssh://git@github.com/o/r", nil, false},
		{"git@github.com:o/r.git", nil, false},
		{"file:///srv/git/linux.git", nil, false},
		{"/srv/git/linux.git", nil, false},
		{"https://github.com/o", nil, false},
		{"https://github.com/o/..", nil, false},
		{"https://github.com/../r", nil, false},
		{"https://hunter2@github.com/o/r", nil, false},
		{"https://x-access-token:hunter2@github.com/o/r", nil, false},
		{"https://ada:hunter2@git.example.org/linux.git", listed, false},
		{"https://git.example.org/linux.git?token=hunter2", listed, false},
	} {
		s, err := parseSource(c.in, c.hosts...)
		if (err == nil) != c.ok {
			t.Errorf("%s with hosts %v: %q %v", c.in, c.hosts, s.Repo, err)
		}
		if err != nil && strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: the refusal repeats the credential: %v", c.in, err)
		}
		if err == nil && (!strings.HasPrefix(s.Repo, "https://") || strings.Contains(s.Repo, "@")) {
			t.Errorf("%s became %q", c.in, s.Repo)
		}
	}
	for _, bad := range [][]string{{"https://git.example.org"}, {"git.example.org/linux"}, {"ada@git.example.org"}, {""}} {
		if got, err := SourceHosts(bad); err == nil {
			t.Errorf("the configuration took %q as a host: %v", bad, got)
		}
	}
	if got, err := SourceHosts([]string{"Git.Kernel.org", "github.com"}); err != nil || len(got) != 1 || got[0] != "git.kernel.org" {
		t.Errorf("a host list: %v %v", got, err)
	}
}

// countedPort listens on a loopback port and counts every connection made to it.
func countedPort(t *testing.T) (addr string, n *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	n = new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			c.Close()
		}
	}()
	return ln.Addr().String(), n
}

// For a refused source labd contacts nothing: no connection reaches the
// host it names. The same host, once the configuration lists it, is
// contacted, which shows the count would have seen it.
func TestARefusedSourceContactsNothing(t *testing.T) {
	addr, n := countedPort(t)
	b := newBuilderLab(t)
	config, _, err := b.s.storeArtifact(strings.NewReader("CONFIG_X=y\n"))
	if err != nil {
		t.Fatal(err)
	}
	recipe, _, _ := b.s.storeArtifact(strings.NewReader("a recipe"))
	ask := func(req api.BuildRequest) (int, string) {
		body, _ := json.Marshal(req)
		code, data := b.do(b.ctx, "POST", "/api/builds", "lab_mtok", strings.NewReader(string(body)))
		return code, string(data)
	}
	refused := []string{
		"https://" + addr + "/o/r",
		"https://" + addr + "/o/r.git@main",
		"http://" + addr + "/o/r",
		"git://" + addr + "/o/r",
		"ssh://git@" + addr + "/o/r",
		"https://ada:hunter2@" + addr + "/o/r",
		"https://ada:hunter2@github.com/o/r",
	}
	for _, src := range refused {
		for _, req := range []api.BuildRequest{{Source: src, Config: config}, {Source: src, Recipe: recipe}} {
			code, body := ask(req)
			if code != 400 || strings.Contains(body, "hunter2") || strings.Count(strings.TrimSpace(body), "\n") != 0 {
				t.Errorf("%s: %d %q", src, code, body)
			}
		}
	}
	if got := n.Load(); got != 0 {
		t.Fatalf("%d connections were made for refused sources", got)
	}
	if all, _ := b.s.store.builds(`1=1`); len(all) != 0 {
		t.Fatalf("%d builds were queued for refused sources", len(all))
	}

	// Listed, the host is asked (and is no git server).
	b.hosts = []string{addr}
	b.start()
	if code, body := ask(api.BuildRequest{Source: "https://" + addr + "/o/r", Config: config}); code != 400 || !strings.Contains(body, "ls-remote") {
		t.Fatalf("a listed host that is no git server: %d %q", code, body)
	}
	if n.Load() == 0 {
		t.Fatal("the listed host was never contacted: the count proves nothing")
	}
	// Credentials are refused for a listed host too, before anything is asked.
	before := n.Load()
	if code, _ := ask(api.BuildRequest{Source: "https://ada:hunter2@" + addr + "/o/r", Config: config}); code != 400 || n.Load() != before {
		t.Fatalf("an address with credentials at a listed host: %d, %d new connections", code, n.Load()-before)
	}
}

// fakeGit puts a git first on PATH that records each call and answers
// ls-remote with one commit for the ref asked, and returns a reader of what it recorded.
func fakeGit(t *testing.T) func() string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$*\" >> '" + calls + "'\nprintf '17cba00e43b94ba6b5c64be7cbe3db9234a41f84\\t%s\\n' \"$4\"\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string {
		b, _ := os.ReadFile(calls)
		return string(b)
	}
}

// An address that could carry a credential is refused before git runs:
// userinfo, any query, a fragment that is not a ref name, a scheme other
// than https. The plain public form still reaches git.
func TestASourceThatCouldCarryACredentialRunsNoGit(t *testing.T) {
	calls := fakeGit(t)
	b := newBuilderLab(t)
	b.hosts = []string{"git.example.org"}
	b.start()
	config, _, err := b.s.storeArtifact(strings.NewReader("CONFIG_X=y\n"))
	if err != nil {
		t.Fatal(err)
	}
	ask := func(src string) (int, string) {
		body, _ := json.Marshal(api.BuildRequest{Source: src, Config: config})
		code, data := b.do(b.ctx, "POST", "/api/builds", "lab_mtok", strings.NewReader(string(body)))
		return code, strings.TrimSpace(string(data))
	}
	for _, src := range []string{
		"https://hunter2@github.com/o/r",
		"https://x-access-token:hunter2@github.com/o/r",
		"https://ada:hunter2@git.example.org/linux.git",
		"https://github.com/o/r?access_token=hunter2",
		"https://github.com/o/r?",
		"github.com/o/r?access_token=hunter2",
		"https://github.com/o/r/tree/main?access_token=hunter2",
		"https://github.com/o/r?access_token=hunter2#main",
		"https://github.com/o/r#main?access_token=hunter2",
		"https://github.com/o/r#access_token=hunter2",
		"https://github.com/o/r#a#access_token=hunter2",
		"https://github.com/o/r/tree/a%3Dhunter2",
		"https://git.example.org/linux.git?token=hunter2",
		"https://git.example.org/linux.git@v7.1?token=hunter2",
		"https://git.example.org/linux.git#token=hunter2",
		"http://github.com/o/r",
		"git://github.com/o/r",
		"ssh://git@github.com/o/r",
		"file:///srv/git/linux.git",
	} {
		code, body := ask(src)
		if code != 400 || strings.Contains(body, "hunter2") || strings.Contains(body, "\n") || !strings.Contains(body, "source") {
			t.Errorf("%s: %d %q", src, code, body)
		}
	}
	if got := calls(); got != "" {
		t.Fatalf("git ran for refused sources:\n%s", got)
	}
	if all, _ := b.s.store.builds(`1=1`); len(all) != 0 {
		t.Fatalf("%d builds were queued for refused sources", len(all))
	}
	for _, src := range []string{"https://github.com/o/r", "https://github.com/o/r#main", "https://git.example.org/linux.git@v7.1"} {
		if code, body := ask(src); code >= 300 {
			t.Errorf("the public form %s: %d %q", src, code, body)
		}
	}
	if got := calls(); !strings.Contains(got, "ls-remote -- https://github.com/o/r ") || !strings.Contains(got, "ls-remote -- https://git.example.org/linux.git v7.1") {
		t.Fatalf("git was not asked for the public forms:\n%s", got)
	}
}
