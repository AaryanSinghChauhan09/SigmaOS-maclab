// Command lab-macos-runner runs the lab's macOS benchmarks on a lab Mac, in
// macOS: a LaunchDaemon starts it at every boot (see release/macos-runner).
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/omacom/omarchy-maclab/internal/macosrunner"
)

const usage = `lab-macos-runner: the macOS side of Omarchy Mac Lab's macOS runs

  sudo lab-macos-runner enroll --server <Lab address> --token-stdin [--gui-user you]
                          enroll this Mac's runner once, with the line lab macos enroll <mac> prints
  lab-macos-runner boot   what the LaunchDaemon runs at boot: with a macOS run queued for this Mac,
                          run it, upload the results and reboot back to Omarchy; else nothing
  lab-macos-runner version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	config := fs.String("config", macosrunner.ConfigPath, "the runner's enrolment")
	var err error
	switch os.Args[1] {
	case "enroll":
		server := fs.String("server", "", "the Lab address")
		tokenStdin := fs.Bool("token-stdin", false, "read the one-time token from the first line of stdin")
		gui := fs.String("gui-user", os.Getenv("SUDO_USER"), "whose desktop session the browsers run in")
		fs.Parse(os.Args[2:])
		if *server == "" || !*tokenStdin {
			err = fmt.Errorf("usage: sudo lab-macos-runner enroll --server <Lab address> --token-stdin [--gui-user you]")
			break
		}
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if err = macosrunner.Enroll(ctx, *server, strings.TrimSpace(line), *gui, *config); err == nil {
			fmt.Printf("this Mac's macOS runner is enrolled; it runs at every boot (%s)\n", *config)
		}
	case "boot":
		fs.Parse(os.Args[2:])
		var cfg macosrunner.Config
		if cfg, err = macosrunner.LoadConfig(*config); err == nil {
			r := &macosrunner.Runner{Config: cfg, Dir: macosrunner.WorkDir, Log: log.Printf}
			err = r.Boot(ctx)
		}
	case "version":
		fmt.Println(macosrunner.Version)
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}
