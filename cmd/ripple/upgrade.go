package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/manishaacharyahere/ripple/internal/upgrade"
)

// Exit 3 means the upgrade would hurt this consumer (destroys, replaces,
// breaks its plan) or preflight refused to run it.
const exitHarmful = 3

func runUpgrade(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("upgrade-plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o upgrade.Options
	fs.StringVar(&o.Repo, "repo", "", "module repo: local path or clone URL")
	fs.StringVar(&o.Example, "example", "", "consumer directory inside the repo, e.g. examples/complete")
	fs.StringVar(&o.Endpoint, "endpoint", os.Getenv("RIPPLE_ENDPOINT"), "LocalStack URL (required)")
	fs.StringVar(&o.Terraform, "terraform", "terraform", "terraform binary")
	fs.StringVar(&o.WorkDir, "workdir", "", "scratch directory (default: a temp dir, removed afterwards)")
	allow := fs.String("allow-providers", strings.Join(upgrade.DefaultAllowedProviders, ","), "providers preflight accepts (namespace/name, comma-separated)")
	verbose := fs.Bool("v", false, "log terraform commands to stderr")
	resultFile := fs.String("result-file", "", "also write a compact verdict here")
	evidenceFile := fs.String("evidence-file", "", "write raw evidence here and skip classifying (the untrusted half; see `ripple verdict`)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 2 || o.Repo == "" || o.Example == "" {
		fmt.Fprint(stderr, usage)
		return 1
	}
	o.From, o.To = fs.Arg(0), fs.Arg(1)
	o.AllowedProviders = strings.Split(*allow, ",")
	if o.WorkDir == "" {
		tmp, err := os.MkdirTemp("", "ripple-upgrade-")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer os.RemoveAll(tmp)
		o.WorkDir = tmp
	}
	if *verbose {
		o.Log = stderr
	}

	ev, err := upgrade.Collect(o)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *evidenceFile != "" {
		// Exit 0 whenever evidence was written: in Kubernetes this runs as an
		// init container, and only a zero exit lets the verdict container start.
		if err := writeJSON(*evidenceFile, ev); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	res, err := upgrade.Verdict(ev)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return report(res, *resultFile, stdout, stderr)
}

// runVerdict is the trusted half: it reads evidence, never runs Terraform,
// and writes the result the operator reads.
func runVerdict(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("verdict", flag.ContinueOnError)
	fs.SetOutput(stderr)
	evidenceFile := fs.String("evidence-file", "", "evidence written by `ripple upgrade-plan --evidence-file` (required)")
	resultFile := fs.String("result-file", "", "also write a compact verdict here (e.g. /dev/termination-log)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *evidenceFile == "" || fs.NArg() != 0 {
		fmt.Fprint(stderr, usage)
		return 1
	}
	b, err := os.ReadFile(*evidenceFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var ev upgrade.Evidence
	if err := json.Unmarshal(b, &ev); err != nil {
		fmt.Fprintf(stderr, "evidence: %v\n", err)
		return 1
	}
	res, err := upgrade.Verdict(&ev)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return report(res, *resultFile, stdout, stderr)
}

// report prints the full result as JSON (the log line is the result) and
// writes the compact form the operator reads.
func report(res *upgrade.Result, resultFile string, stdout, stderr io.Writer) int {
	if err := json.NewEncoder(stdout).Encode(res); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if resultFile != "" {
		if err := writeResult(resultFile, res); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	switch res.Status {
	case upgrade.Destructive, upgrade.PlanFailed, upgrade.Rejected:
		return exitHarmful
	case upgrade.BaselineFailed:
		return 1
	}
	return 0
}

// Kubernetes caps termination messages at 4096 bytes, so drop the per-resource
// list and shorten the error before writing; the full result is in the log.
func writeResult(path string, res *upgrade.Result) error {
	compact := *res
	compact.Changes = nil
	if len(compact.Error) > 3000 {
		compact.Error = "..." + compact.Error[len(compact.Error)-3000:]
	}
	return writeJSON(path, compact)
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
