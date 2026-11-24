// Command ripple tells you what a shared Terraform module release will do to
// the teams that consume it.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/manishaacharyahere/ripple/internal/gitsrc"
	"github.com/manishaacharyahere/ripple/internal/semdiff"
)

// Exit codes: 0 ok, 1 usage/load error, 2 release is under-bumped.
const exitUnderBumped = 2

const usage = `usage:
  ripple diff [--from v1.2.0 --to v1.3.0] [--json] OLD_DIR NEW_DIR
  ripple diff --repo PATH [--path SUBDIR] [--json] OLD_REF NEW_REF
  ripple upgrade-plan --repo PATH|URL --example DIR --endpoint URL [--evidence-file F] OLD_REF NEW_REF
  ripple verdict --evidence-file F [--result-file F]
  ripple operator [--namespace ripple]
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "upgrade-plan" {
		return runUpgrade(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "verdict" {
		return runVerdict(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "operator" {
		return runOperator(args[1:], stderr)
	}
	if len(args) == 0 || args[0] != "diff" {
		fmt.Fprint(stderr, usage)
		return 1
	}
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", "", "git repo; OLD and NEW are refs (tags) instead of directories")
	sub := fs.String("path", "", "module subdirectory inside the repo")
	from := fs.String("from", "", "declared old version (defaults to OLD ref with --repo)")
	to := fs.String("to", "", "declared new version (defaults to NEW ref with --repo)")
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if fs.NArg() != 2 {
		fmt.Fprint(stderr, usage)
		return 1
	}
	oldDir, newDir := fs.Arg(0), fs.Arg(1)

	if *repo != "" {
		if *from == "" {
			*from = oldDir
		}
		if *to == "" {
			*to = newDir
		}
		tmp, err := os.MkdirTemp("", "ripple-")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer os.RemoveAll(tmp)
		dirs := [2]string{}
		for i, ref := range [2]string{oldDir, newDir} {
			dirs[i] = filepath.Join(tmp, fmt.Sprint(i))
			if err := gitsrc.Extract(*repo, ref, dirs[i]); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			dirs[i] = filepath.Join(dirs[i], *sub)
		}
		oldDir, newDir = dirs[0], dirs[1]
	}

	oldMod, err := semdiff.Load(oldDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	newMod, err := semdiff.Load(newDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	changes := semdiff.Compare(oldMod, newMod)
	report := struct {
		Required semdiff.Level    `json:"required"`
		From     string           `json:"from,omitempty"`
		To       string           `json:"to,omitempty"`
		Declared *semdiff.Level   `json:"declared,omitempty"`
		Verdict  string           `json:"verdict,omitempty"`
		Changes  []semdiff.Change `json:"changes"`
	}{Required: semdiff.Required(changes), From: *from, To: *to, Changes: changes}

	if *from != "" || *to != "" {
		d, err := semdiff.Declared(*from, *to)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		report.Declared = &d
		switch {
		case d < report.Required:
			report.Verdict = "under-bumped"
		case d > report.Required:
			report.Verdict = "over-bumped"
		default:
			report.Verdict = "ok"
		}
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		enc.Encode(report)
	} else {
		fmt.Fprintf(stdout, "Required bump: %s\n", report.Required)
		if report.Declared != nil {
			fmt.Fprintf(stdout, "Declared:      %s -> %s (%s)  %s\n", *from, *to, *report.Declared, report.Verdict)
		}
		if len(changes) == 0 {
			fmt.Fprintln(stdout, "\nNo interface changes.")
		} else {
			fmt.Fprintln(stdout)
		}
		for _, c := range changes {
			fmt.Fprintf(stdout, "  %-6s %-40s %s\n", c.Level, c.Address, c.Detail)
		}
	}
	if report.Verdict == "under-bumped" {
		return exitUnderBumped
	}
	return 0
}
