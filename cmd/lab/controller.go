package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/client"
)

const controllerUse = "lab controller add <name> --url https://ADDRESS:PORT --fingerprint SHA256 --token-file FILE | list | remove <name>"

// controllerCmd handles `lab controller ...`: the Out-of-band controllers,
// the mini's own and each Mac lab controller (Maintainers). The token is
// read from the file its install wrote, never typed as an argument.
func controllerCmd(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 1 {
		return errors.New(controllerUse)
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("controller add", flag.ExitOnError)
		u := fs.String("url", "", "the controller's address, as its install printed it: https://ADDRESS:PORT")
		fp := fs.String("fingerprint", "", "its certificate's SHA-256, as its install printed it")
		tf := fs.String("token-file", "", "a copy of its token, from /usr/local/lab-controller/secrets/oobd.token on that Mac")
		pos := parse(fs, args[1:])
		if err := need(pos, 1, controllerUse); err != nil {
			return err
		}
		if *u == "" || *fp == "" || *tf == "" {
			return errors.New(controllerUse)
		}
		b, err := os.ReadFile(*tf)
		if err != nil {
			return err
		}
		token := strings.TrimSpace(string(b))
		if err := c.AddController(ctx, api.ControllerAdd{Name: pos[0], URL: *u, Fingerprint: *fp, Token: token}); err != nil {
			return err
		}
		fmt.Printf("added the controller %s: labd looks at its ports from now on, and resets a Mac sighted there through it\n", pos[0])
		return nil
	case "list":
		list, err := c.Controllers(ctx)
		if err != nil {
			return err
		}
		showControllers(os.Stdout, list)
		return nil
	case "remove":
		if err := need(args[1:], 1, "lab controller remove <name>"); err != nil {
			return err
		}
		if err := c.RemoveController(ctx, args[1]); err != nil {
			return err
		}
		fmt.Printf("removed the controller %s: the Sightings on its ports are dropped\n", args[1])
		return nil
	}
	return fmt.Errorf("unknown: lab controller %s\n%s", args[0], controllerUse)
}

func showControllers(out io.Writer, list []api.Controller) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tADDRESS\tPORTS\tADDED BY")
	for _, c := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.Name, orDash(c.URL, "this machine"), strings.Join(c.Ports, " "), orDash(c.AddedBy, "-"))
	}
	w.Flush()
}
