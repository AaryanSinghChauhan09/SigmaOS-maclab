// Command oobd is the Out-of-band controller: a root LaunchDaemon on the
// mini that sends Hard resets through the mini's three rear ports, or on any
// other Mac lab controller through that Mac's ports (--rids), answering labd
// over HTTPS on one tailnet or LAN address.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/omacom/omarchy-maclab/internal/oob"
	"github.com/omacom/omarchy-maclab/internal/oobd"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:7780", "the loopback address labd reaches the controller at: it listens nowhere else")
	tokenFile := flag.String("token-file", "/usr/local/lab/secrets/oobd.token", "the file that holds the token labd must present: this account's alone (0600)")
	tool := flag.String("tool", "/usr/local/lab/current/bin/macvdmtool", "the reset tool: omacom/macvdmtool, which takes --rid")
	cert := flag.String("tls-cert", "", "with --tls-key: serve HTTPS, and answer other machines (a Mac lab controller labd reaches over the network)")
	key := flag.String("tls-key", "", "the key of --tls-cert")
	ridList := flag.String("rids", "", "this machine's ports as the reset tool numbers them, such as 0,1; none means the mini's 0,1,2")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatalf("oobd takes no argument: %v", flag.Args())
	}
	tls := *cert != "" || *key != ""
	if tls && (*cert == "" || *key == "") {
		log.Fatal("--tls-cert and --tls-key go together")
	}
	if err := oobd.Listen(*listen, tls); err != nil {
		log.Fatal(err)
	}
	var rids []int
	for _, f := range strings.Split(*ridList, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil || n < 0 || n > 99 {
			log.Fatalf("--rids: %q is not a RID", f)
		}
		rids = append(rids, n)
	}
	token, err := oob.ReadToken(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	s, err := oobd.New(oobd.Config{Token: token, Tool: oobd.ExecTool{Path: *tool}, Ports: oobd.DefaultPorts(), RIDs: rids, Remote: tls}, log.Default())
	if err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(*tool); err != nil {
		log.Printf("the reset tool is not at %s: every reset will be refused until a Deploy brings it", *tool)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	hs := &http.Server{Addr: *listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); hs.Close() }()
	log.Printf("oobd on %s (https %v): resets through %s", *listen, tls, *tool)
	serve := hs.ListenAndServe
	if tls {
		serve = func() error { return hs.ListenAndServeTLS(*cert, *key) }
	}
	if err := serve(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
