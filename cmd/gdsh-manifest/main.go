// gdsh-manifest maintains godot-gdsh-lib-manifest's manifest.json. Pins are only ever moved by
// a person running `pin` (or merging a PR that did); `propose` just lists newer tags.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	gdshpkg "github.com/brohd11/gdsh-pkg"
	"github.com/brohd11/gdsh-pkg/maint"
)

const usage = `usage:
  gdsh-manifest check [-w] [-f FILE]
      Check every entry at its pinned commit: the tag still points there, package.cfg names the
      entry, it has cmd/manifest.gd and no absolute res://addons paths. Derived fields (gdsh,
      requires, project_only) must match package.cfg; -w rewrites them instead of failing.
  gdsh-manifest pin NAME TAG [-f FILE]
      Pin NAME to TAG and the commit it points at now, and re-derive the entry. Review the
      package at that commit first: this is what installs will serve.
  gdsh-manifest propose [--tsv] [-f FILE]
      List entries with a newer tag than their pin. --tsv prints name, repo, from, to and a
      compare URL per line, for the propose workflow.
FILE defaults to manifest.json.
`

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, &maint.Maint{}))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, m *maint.Maint) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		return 0
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("f", "manifest.json", "manifest file")
	write := fs.Bool("w", false, "rewrite derived fields")
	tsv := fs.Bool("tsv", false, "tab-separated output")
	// Flags may follow positionals: parse what's left after each one.
	var pos []string
	rest := args[1:]
	for {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	man, err := gdshpkg.LoadManifest(*file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch args[0] {
	case "check":
		if len(pos) != 0 {
			return usageErr(stderr, "check takes no arguments")
		}
		before, _ := man.Marshal()
		problems, err := m.Derive(ctx, man, "")
		failed := report(stderr, problems, err)
		after, _ := man.Marshal()
		if string(before) != string(after) {
			if !*write {
				fmt.Fprintf(stderr, "error: derived fields in %s are out of date; run: gdsh-manifest check -w\n", *file)
				return 1
			}
			if err := os.WriteFile(*file, after, 0o644); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			fmt.Fprintf(stdout, "Updated derived fields in %s\n", *file)
		}
		if failed {
			return 1
		}
		fmt.Fprintf(stdout, "%d packages OK\n", len(man.Packages))
		return 0
	case "pin":
		if len(pos) != 2 {
			return usageErr(stderr, "pin needs NAME TAG")
		}
		problems, err := m.Pin(ctx, man, pos[0], pos[1])
		if report(stderr, problems, err) {
			return 1
		}
		data, _ := man.Marshal()
		if err := os.WriteFile(*file, data, 0o644); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		p, _ := man.Find(pos[0])
		fmt.Fprintf(stdout, "Pinned %s to %s (%s)\n", p.Name, p.Tag, p.Commit)
		return 0
	case "propose":
		if len(pos) != 0 {
			return usageErr(stderr, "propose takes no arguments")
		}
		props, err := m.Propose(ctx, man)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		for _, p := range props {
			if *tsv {
				fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Repo, p.From, p.To, p.CompareURL())
			} else {
				fmt.Fprintf(stdout, "%s %s -> %s  %s\n", p.Name, p.From, p.To, p.CompareURL())
			}
		}
		return 0
	}
	return usageErr(stderr, fmt.Sprintf("unknown command %q", args[0]))
}

// report prints problems and returns whether the run failed.
func report(w io.Writer, problems []maint.Problem, err error) bool {
	failed := err != nil
	for _, p := range problems {
		fmt.Fprintln(w, p)
		failed = failed || p.Error
	}
	if err != nil {
		fmt.Fprintln(w, "error:", err)
	}
	return failed
}

func usageErr(w io.Writer, msg string) int {
	fmt.Fprintf(w, "%s\n\n%s", msg, usage)
	return 2
}
