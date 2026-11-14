package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/omacom/omarchy-maclab/internal/server"
)

const oldLabUsage = `usage: labd old-lab counts --data DIR
       labd old-lab upgrade [--dry-run] --data DIR --account-id N <owner's GitHub handle>

For the data directory of the old lab's labd, copied to this machine.
counts says how many Mac records, jobs and builds it holds and changes
nothing. upgrade makes it this lab's, where it lies and once, before labd
first opens it: its Mac records are Retired, what was done under its admin
token becomes the owner's, and nothing the old lab handed out is honoured.
Run it as the account labd runs as.
`

// oldLab is `labd old-lab`. What it found or did goes to out, in counts
// and names alone; it reads no token and prints none. It returns the
// command's exit status.
func oldLab(args []string, out, errw io.Writer) int {
	usage := func() int {
		fmt.Fprint(errw, oldLabUsage)
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	fs := flag.NewFlagSet("labd old-lab "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	data := fs.String("data", "", "")
	account := fs.Int64("account-id", 0, "")
	dry := fs.Bool("dry-run", false, "")
	if fs.Parse(args[1:]) != nil || *data == "" {
		return usage()
	}
	failed := func(err error) int {
		fmt.Fprintf(errw, "labd old-lab %s: %v. Nothing was changed.\n", args[0], err)
		return 1
	}
	switch {
	case args[0] == "counts" && fs.NArg() == 0 && *account == 0 && !*dry:
		c, err := server.CountOldLab(*data)
		if err != nil {
			return failed(err)
		}
		fmt.Fprintf(out, "records %d\njobs %d\nbuilds %d\n", c.Records, c.Jobs, c.Builds)
		return 0
	case args[0] == "upgrade" && fs.NArg() == 1 && *account > 0:
		if !*dry {
			if err := asDataOwner(filepath.Join(*data, "labd.db")); err != nil {
				return failed(err)
			}
		}
		owner := server.Person{AccountID: *account, Handle: strings.TrimPrefix(fs.Arg(0), "@")}
		up, err := server.UpgradeOldLab(*data, owner, time.Now(), *dry)
		if err != nil {
			return failed(err)
		}
		if *dry {
			fmt.Fprintf(out, "A dry run: %s is as it was. The upgrade would do this.\n", *data)
		} else {
			fmt.Fprintf(out, "The old lab's data in %s is upgraded.\n", *data)
		}
		fmt.Fprintf(out, "records %d (%d before)\njobs %d (%d before)\nbuilds %d (%d before)\n",
			up.After.Records, up.Before.Records, up.After.Jobs, up.Before.Jobs, up.After.Builds, up.Before.Builds)
		fmt.Fprintf(out, "Retired: %s\n", names(up.Retired))
		fmt.Fprintf(out, "Lease ended: %s\n", names(up.LeasesEnded))
		fmt.Fprintf(out, "out-of-band entry cleared: %s\n", names(up.OutOfBand))
		fmt.Fprintf(out, "now the work of @%s (GitHub account %d): %d jobs, %d builds\n", owner.Handle, owner.AccountID, up.JobsOwners, up.BuildsOwners)
		fmt.Fprintf(out, "ended, left unfinished by the old labd: %d jobs, %d builds\n", up.JobsEnded, up.BuildsEnded)
		fmt.Fprintf(out, "no longer honoured: enrollment tokens %d, Maintainers' tokens %d, builders' tokens %d, every device secret; token files removed: %s\n",
			up.EnrollTokens, up.Maintainers, up.Builders, names(up.TokenFiles))
		return 0
	}
	return usage()
}

func names(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

func runOldLab(args []string) int { return oldLab(args, os.Stdout, os.Stderr) }
