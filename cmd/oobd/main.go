// Command oobd is the Out-of-band controller: a root LaunchDaemon on the
// mini that sends Hard resets through the mini's three rear ports.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/omacom/omarchy-maclab/internal/oob"
	"github.com/omacom/omarchy-maclab/internal/oobd"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:7780", "the loopback address labd reaches the controller at: it listens nowhere else")
	tokenFile := flag.String("token-file", "/usr/local/lab/secrets/oobd.token", "the file that holds the token labd must present: this account's alone (0600)")
	tool := flag.String("tool", "/usr/local/lab/current/bin/macvdmtool", "the reset tool: omacom/macvdmtool, which takes --rid")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatalf("oobd takes no argument: %v", flag.Args())
	}
	if err := oobd.Loopback(*listen); err != nil {
		log.Fatal(err)
	}
	token, err := oob.ReadToken(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	s, err := oobd.New(oobd.Config{Token: token, Tool: oobd.ExecTool{Path: *tool}, Ports: oobd.DefaultPorts()}, log.Default())
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
	log.Printf("oobd on %s: ports with RID 0, 1 and 2, resets through %s", *listen, *tool)
	if err := hs.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
