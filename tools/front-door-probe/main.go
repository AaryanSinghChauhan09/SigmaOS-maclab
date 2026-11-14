// Command front-door-probe is what the Front door's tests (scripts/cutover/front-door/)
// cannot do with lab and curl alone: read a held response byte by byte under
// the saved login, read the Exec record, ask which addresses answer, ask the
// router what it would open, and, for a run on one machine, stand in for
// the edge and the tunnel in front of a local labd.
//
// It prints key=value lines and never an address, a token or a page. Under
// the saved login it is the Go client the lab command is: the login is
// read by internal/client, which sends a saved Lab token only to the
// address it was saved for.
package main

import (
	"flag"
	"fmt"
	"os"
)

const usage = `usage: front-door-probe <command>

  held [--timeout SEC] [--cut-after SEC] exec MAC COMMAND...
  held [--cut-after SEC] freeze MAC RELEASE
  held [--for SEC] events
      one held response under the saved login: when its Keepalives came and
      how it ended
  execs MAC TEXT
      the Exec record's lines for MAC whose command holds TEXT
  get [--ipv4] PATH
      one GET under the saved login: the status and what the edge said of it
  mac MAC
      whether the Mac is checking in
  job ID
      a job's state and outcome
  answers HOST:PORT...
      which of the addresses take a TCP connection
  router
      whether a router on this network offers to open ports (UPnP, NAT-PMP)
  tunnel --socket PATH --host NAME [--client ADDR] [--silence 125s] [--down 2s] [--fault NAME,...]
      stand in for the edge and the tunnel in front of a local labd's socket
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	var err error
	switch os.Args[1] {
	case "held":
		err = heldCommand(fs, os.Args[2:])
	case "execs":
		err = execsCommand(fs, os.Args[2:])
	case "get":
		err = getCommand(fs, os.Args[2:])
	case "mac":
		err = macCommand(fs, os.Args[2:])
	case "job":
		err = jobCommand(fs, os.Args[2:])
	case "answers":
		err = answersCommand(fs, os.Args[2:])
	case "router":
		err = routerCommand(fs, os.Args[2:])
	case "tunnel":
		err = tunnelCommand(fs, os.Args[2:])
	default:
		fs.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "front-door-probe:", err)
		os.Exit(1)
	}
}
