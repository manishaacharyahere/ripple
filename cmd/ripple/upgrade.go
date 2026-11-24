package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/manishaacharyahere/ripple/internal/upgrade"
)

// Exit 3 means the upgrade would hurt this consumer (destroys, replaces or breaks its plan).
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
	verbose := fs.Bool("v", false, "log terraform commands to stderr")
	resultFile := fs.String("result-file", "", "also write a compact verdict here (e.g. /dev/termination-log for the operator)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() != 2 || o.Repo == "" || o.Example == "" {
		fmt.Fprint(stderr, usage)
		return 1
	}
	o.From, o.To = fs.Arg(0), fs.Arg(1)
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

	res, err := upgrade.Run(o)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// Always JSON: this runs inside a Kubernetes Job and its log line is the result.
	if err := json.NewEncoder(stdout).Encode(res); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *resultFile != "" {
		if err := writeResult(*resultFile, res); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	switch res.Status {
	case upgrade.Destructive, upgrade.PlanFailed:
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
	b, err := json.Marshal(compact)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
