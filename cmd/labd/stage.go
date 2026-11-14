package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/omacom/omarchy-maclab/internal/server"
)

const stageUsage = `usage: labd stage --data DIR

Says whether the lab whose data directory is DIR has opened: "live" and the
time it did, or "not opened". It reads labd.db and changes nothing; labd may
be running. The Cutover's scripts on the mini ask it before they do what is
only for a lab in Preview.
`

// stage is `labd stage`. It returns the command's exit status.
func stage(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("labd stage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", "", "")
	if fs.Parse(args) != nil || *data == "" || fs.NArg() != 0 {
		fmt.Fprint(errw, stageUsage)
		return 2
	}
	is, err := server.StageOf(*data)
	if err != nil {
		fmt.Fprintf(errw, "labd stage: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, is)
	return 0
}
