package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/omacom/omarchy-maclab/internal/server"
)

// breakGlass is `labd break-glass`: it mints a Lab token for a named person
// by writing to labd's own data, so it works only on the server itself, for
// whoever may write there. The token goes to out, alone on its line, and
// what it is to errw. It returns the command's exit status.
func breakGlass(args []string, out, errw io.Writer) int {
	home, _ := os.UserHomeDir()
	fs := flag.NewFlagSet("labd break-glass", flag.ContinueOnError)
	fs.SetOutput(errw)
	data := fs.String("data", filepath.Join(home, ".local/share/maclab"), "labd's data directory, as labd's own --data names it")
	account := fs.Int64("account-id", 0, "the person's GitHub account id: needed for a person the lab does not know yet, who is then kept under it")
	fs.Usage = func() {
		fmt.Fprintln(errw, "usage: labd break-glass [--data DIR] [--account-id N] <github-handle>")
		fmt.Fprintln(errw, "Mints a Lab token for the person, shown once. Run it on the server, as the account labd runs as.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	if err := asDataOwner(filepath.Join(*data, "labd.db")); err != nil {
		fmt.Fprintln(errw, "labd break-glass:", err)
		return 1
	}
	m, err := server.BreakGlass(*data, fs.Arg(0), *account, time.Now())
	if err != nil {
		fmt.Fprintln(errw, "labd break-glass:", err)
		return 1
	}
	fmt.Fprintf(errw, "A Lab token for @%s (GitHub account %d), named %q. Left unused it expires on %s. It does what @%s may do, and is shown this once: give it to `lab login` at its prompt, at labd's loopback address.\n",
		m.Handle, m.AccountID, m.Name, m.Expires.UTC().Format(time.DateOnly), m.Handle)
	fmt.Fprintln(out, m.Token)
	return 0
}

// asDataOwner refuses to write to a labd.db that belongs to another
// account: files root left behind there would stop labd.
func asDataOwner(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return nil // BreakGlass says what is missing
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s belongs to another account (uid %d): run this as the account labd runs as, as in `sudo -u lab labd break-glass ...`", path, st.Uid)
	}
	return nil
}
