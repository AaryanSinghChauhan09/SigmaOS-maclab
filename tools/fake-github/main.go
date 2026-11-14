// Command fake-github serves internal/githubtest on a loopback port, so a
// labd under development has something to sign people in against, with a
// browser or, for a Mac, with the device flow (the code a Mac shows is
// typed at /login/device):
//
//	go run ./tools/fake-github --files /tmp/lab/sign-in --user ada:1001 --user grace:1002
//	labd --sign-in /tmp/lab/sign-in --github-url http://127.0.0.1:7791 ...
//
// --files writes the two files labd's --sign-in reads for the OAuth app
// (github-client-id, github-client-secret); the admins file beside them is
// yours to write, one account id a line. It is not GitHub and signs anyone in
// as any account it was given: it listens on loopback only, and labd takes
// --github-url only for a loopback address and never beside --socket.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/githubtest"
)

type users []githubtest.User

func (u *users) String() string { return fmt.Sprint(*u) }

func (u *users) Set(v string) error {
	login, raw, ok := strings.Cut(v, ":")
	id, err := strconv.ParseInt(raw, 10, 64)
	if !ok || login == "" || err != nil || id <= 0 {
		return fmt.Errorf("want handle:account-id, such as ada:1001")
	}
	*u = append(*u, githubtest.User{ID: id, Login: login})
	return nil
}

func main() {
	listen := flag.String("listen", "127.0.0.1:7791", "the loopback address to serve on")
	files := flag.String("files", "", "a folder to write github-client-id and github-client-secret into, for labd --sign-in")
	var us users
	flag.Var(&us, "user", "an account as handle:account-id; repeat for more (default ada:1001)")
	flag.Parse()
	if len(us) == 0 {
		us = users{{ID: 1001, Login: "ada"}}
	}
	ap, err := netip.ParseAddrPort(*listen)
	if err != nil || !ap.Addr().IsLoopback() {
		log.Fatalf("--listen %s: fake-github serves on a loopback address only, such as 127.0.0.1:7791", *listen)
	}
	g := githubtest.New("fake-client-id", "fake-client-secret")
	for _, u := range us {
		g.AddUser(u.ID, u.Login)
	}
	if *files != "" {
		if err := os.MkdirAll(*files, 0o700); err != nil {
			log.Fatal(err)
		}
		for name, value := range map[string]string{"github-client-id": g.ClientID, "github-client-secret": g.ClientSecret} {
			if err := os.WriteFile(filepath.Join(*files, name), []byte(value+"\n"), 0o600); err != nil {
				log.Fatal(err)
			}
		}
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("fake GitHub (not GitHub) on http://%s with %d account(s); point labd at it with --github-url http://%s", ln.Addr(), len(us), ln.Addr())
	hs := &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(hs.Serve(ln))
}
