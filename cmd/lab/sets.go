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
	"time"

	"github.com/omacom/omarchy-maclab/internal/api"
	"github.com/omacom/omarchy-maclab/internal/client"
)

const setUse = "lab set create <name> --pkg NAME=repo:VERSION|file:SHA256[@VERSION]|build:ID ... | capture <name> --from <mac> | list | show <name>"

// pkgFlags collects every --pkg.
type pkgFlags []string

func (p *pkgFlags) String() string     { return strings.Join(*p, " ") }
func (p *pkgFlags) Set(v string) error { *p = append(*p, v); return nil }

// setCmd handles `lab set ...`: Package sets, frozen lists of packages to
// boot together (Maintainers).
func setCmd(ctx context.Context, c *client.Client, args []string) error {
	if len(args) < 1 {
		return errors.New(setUse)
	}
	fs := flag.NewFlagSet("set "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch args[0] {
	case "create":
		var pkgs pkgFlags
		fs.Var(&pkgs, "pkg", "NAME=repo:VERSION, NAME=file:SHA256[@VERSION] or NAME=build:ID; repeat for each package")
		pos, err := parseSet(fs, args[1:])
		if err != nil || len(pos) != 1 || len(pkgs) == 0 {
			return errors.New("lab set create <name> --pkg NAME=repo:VERSION|file:SHA256[@VERSION]|build:ID ...")
		}
		req := api.SetRequest{Name: pos[0]}
		for _, p := range pkgs {
			it, err := api.ParseSetItem(p)
			if err != nil {
				return err
			}
			req.Items = append(req.Items, it)
		}
		set, err := c.CreateSet(ctx, req)
		if err != nil {
			return err
		}
		return printSet(os.Stdout, set)
	case "capture":
		from := fs.String("from", "", "the Mac whose installed stack to record")
		pos, err := parseSet(fs, args[1:])
		if err != nil || len(pos) != 1 || *from == "" {
			return errors.New("lab set capture <name> --from <mac>")
		}
		set, err := c.CaptureSet(ctx, api.SetCapture{Name: pos[0], Mac: *from})
		if err != nil {
			return err
		}
		return printSet(os.Stdout, set)
	case "list":
		l, err := c.Sets(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "SET\tPACKAGES\tFROM\tBY\tMADE")
		for _, s := range l {
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\n", s.Name, len(s.Items), orDash(s.CapturedFrom, "-"), orDash(s.CreatedBy, "-"), s.Created.Format(time.DateTime))
		}
		return w.Flush()
	case "show":
		if len(args) != 2 {
			return errors.New("lab set show <name>")
		}
		set, err := c.Set(ctx, args[1])
		if err != nil {
			return err
		}
		return printSet(os.Stdout, set)
	}
	return errors.New(setUse)
}

// parseSet takes flags before and after the positional arguments.
func parseSet(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

func printSet(out io.Writer, s *api.PackageSet) error {
	fmt.Fprintf(out, "set %s, made by %s %s", s.Name, orDash(s.CreatedBy, "-"), s.Created.Format(time.DateTime))
	if s.CapturedFrom != "" {
		fmt.Fprintf(out, ", captured from %s", s.CapturedFrom)
	}
	fmt.Fprintln(out)
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "PACKAGE\tSOURCE\tVERSION\tSHA256")
	for _, it := range s.Items {
		src := it.Source
		if it.Source == api.SetBuild {
			src += ":" + it.Ref
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", it.Package, src, orDash(it.Version, "-"), orDash(it.SHA256, "-"))
	}
	return w.Flush()
}
