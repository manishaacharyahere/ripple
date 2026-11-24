// Package upgrade measures what upgrading a module does to a real consumer:
// it applies the consumer against the old module version (on LocalStack),
// swaps the module code to the new version while keeping the consumer's code
// and state, and classifies the resulting `terraform plan`.
package upgrade

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-config-inspect/tfconfig"
	"github.com/manishaacharyahere/ripple/internal/gitsrc"
)

type Options struct {
	Repo      string // local path or clone URL of the module repo
	From, To  string // git refs
	Example   string // consumer directory inside the repo, e.g. examples/complete
	Endpoint  string // LocalStack URL; required so nothing ever reaches real AWS
	Terraform string // terraform binary (default "terraform")
	WorkDir   string // scratch space; created if missing
	Log       io.Writer
	// AllowedProviders overrides DefaultAllowedProviders for preflight.
	AllowedProviders []string
}

type Status string

const (
	Clean          Status = "clean"           // no changes for the consumer
	Changes        Status = "changes"         // in-place updates or new resources only
	Destructive    Status = "destructive"     // something is deleted or replaced
	PlanFailed     Status = "plan-failed"     // consumer code no longer works with the new module
	BaselineFailed Status = "baseline-failed" // old version could not be applied; result inconclusive
	Rejected       Status = "rejected"        // preflight found something that could run arbitrary code
)

// Evidence is what the untrusted half of a check produces: raw plan changes
// (control and upgrade) or the failure that stopped it. Verdict turns it into
// a Result; in Kubernetes the two halves run in different containers.
type Evidence struct {
	Repo    string           `json:"repo"`
	Example string           `json:"example"`
	From    string           `json:"from"`
	To      string           `json:"to"`
	Status  Status           `json:"status,omitempty"` // set only when collection stopped early
	Error   string           `json:"error,omitempty"`
	Control []ResourceChange `json:"control"`
	Upgrade []ResourceChange `json:"upgrade"`
}

type ResourceChange struct {
	Address string   `json:"address"`
	Actions []string `json:"actions"`
}

type Result struct {
	Repo    string           `json:"repo"`
	Example string           `json:"example"`
	From    string           `json:"from"`
	To      string           `json:"to"`
	Status  Status           `json:"status"`
	Error   string           `json:"error,omitempty"`
	Create  int              `json:"create"`
	Update  int              `json:"update"`
	Replace int              `json:"replace"`
	Delete  int              `json:"delete"`
	Noise   int              `json:"noise"` // changes the control plan also showed; excluded above
	Changes []ResourceChange `json:"changes"`
}

// Run collects evidence and classifies it in one process (local use).
func Run(o Options) (*Result, error) {
	ev, err := Collect(o)
	if err != nil {
		return nil, err
	}
	return Verdict(ev)
}

// Verdict classifies evidence. It never runs Terraform, so it can run in a
// container that untrusted code never touches.
func Verdict(ev *Evidence) (*Result, error) {
	res := &Result{Repo: ev.Repo, Example: ev.Example, From: ev.From, To: ev.To, Changes: []ResourceChange{}}
	if ev.Status != "" {
		switch ev.Status {
		case BaselineFailed, PlanFailed, Rejected:
		default:
			return nil, fmt.Errorf("evidence has unknown status %q", ev.Status)
		}
		res.Status, res.Error = ev.Status, ev.Error
		return res, nil
	}
	if err := classify(res, ev.Upgrade, ev.Control); err != nil {
		return nil, err
	}
	return res, nil
}

// Collect never returns nil Evidence when err is nil; err is reserved for
// infrastructure problems (git, filesystem), not for the module breaking.
func Collect(o Options) (*Evidence, error) {
	if err := checkEndpoint(o.Endpoint); err != nil {
		return nil, err
	}
	if o.Terraform == "" {
		o.Terraform = "terraform"
	}
	if o.Log == nil {
		o.Log = io.Discard
	}
	if o.AllowedProviders == nil {
		o.AllowedProviders = DefaultAllowedProviders
	}
	if err := os.MkdirAll(o.WorkDir, 0o755); err != nil {
		return nil, err
	}
	ev := &Evidence{Repo: o.Repo, Example: o.Example, From: o.From, To: o.To, Control: []ResourceChange{}, Upgrade: []ResourceChange{}}
	stop := func(st Status, step, out string) *Evidence {
		ev.Status, ev.Error = st, step+": "+tail(out)
		return ev
	}

	repo := o.Repo
	if isRemote(repo) {
		repo = filepath.Join(o.WorkDir, "repo")
		if err := git(o.Log, "clone", "--quiet", "--filter=blob:none", "--no-checkout", o.Repo, repo); err != nil {
			return nil, err
		}
	}
	oldRoot, newRoot := filepath.Join(o.WorkDir, "old"), filepath.Join(o.WorkDir, "new")
	if err := gitsrc.Extract(repo, o.From, oldRoot); err != nil {
		return nil, err
	}
	if err := gitsrc.Extract(repo, o.To, newRoot); err != nil {
		return nil, err
	}
	oldEx, newEx := filepath.Join(oldRoot, o.Example), filepath.Join(newRoot, o.Example)
	if _, err := os.Stat(oldEx); err != nil {
		return nil, fmt.Errorf("example %s not found at %s", o.Example, o.From)
	}
	if err := writeProviderOverride(oldEx); err != nil {
		return nil, err
	}

	tf := func(dir string, args ...string) (string, error) { return terraform(o, dir, args...) }
	// init downloads but executes nothing; preflight then checks everything init resolved
	// before any plan or apply runs provider or module code.
	initChecked := func(dir string, args ...string) (string, error) {
		out, err := tf(dir, append([]string{"init", "-input=false", "-no-color"}, args...)...)
		if err != nil {
			return out, err
		}
		if err := preflight(dir, o.AllowedProviders); err != nil {
			return err.Error(), err
		}
		return out, nil
	}

	// 1. Baseline: the consumer as it runs today.
	if out, err := initChecked(oldEx); err != nil {
		if errors.Is(err, ErrRejected) {
			return stop(Rejected, "preflight (old version)", out), nil
		}
		return stop(BaselineFailed, "init", out), nil
	}
	// Tear down even after a failed or partial apply so reruns start clean.
	defer func() {
		if out, err := tf(oldEx, "destroy", "-auto-approve", "-input=false", "-no-color"); err != nil {
			fmt.Fprintf(o.Log, "cleanup destroy failed: %s\n", tail(out))
		}
	}()
	if out, err := tf(oldEx, "apply", "-auto-approve", "-input=false", "-no-color"); err != nil {
		return stop(BaselineFailed, "apply", out), nil
	}
	// Control: re-plan the unchanged version. Whatever shows up is emulator
	// drift (e.g. LocalStack not echoing tags_all), not the upgrade's doing.
	noise, err := planChanges(o, oldEx)
	if err != nil {
		return stop(BaselineFailed, "control plan", err.Error()), nil
	}
	ev.Control = append(ev.Control, noise...)

	// 2. Same consumer code and state, new module underneath (source = "../../").
	if err := os.RemoveAll(newEx); err != nil {
		return nil, err
	}
	if err := copyDir(oldEx, newEx); err != nil {
		return nil, err
	}
	out, err := initChecked(newEx)
	if err != nil && !errors.Is(err, ErrRejected) {
		// A raised provider floor conflicts with the old lock file; upgrading is what a consumer would do next.
		out, err = initChecked(newEx, "-upgrade")
	}
	if err != nil {
		if errors.Is(err, ErrRejected) {
			return stop(Rejected, "preflight (new version)", out), nil
		}
		return stop(PlanFailed, "init", out), nil
	}
	changes, err := planChanges(o, newEx)
	if err != nil {
		return stop(PlanFailed, "plan", err.Error()), nil
	}
	ev.Upgrade = append(ev.Upgrade, changes...)
	return ev, nil
}

func planChanges(o Options, dir string) ([]ResourceChange, error) {
	if out, err := terraform(o, dir, "plan", "-out=ripple.plan", "-input=false", "-no-color"); err != nil {
		return nil, errors.New(tail(out))
	}
	js, err := terraform(o, dir, "show", "-json", "ripple.plan")
	if err != nil {
		return nil, errors.New(tail(js))
	}
	return parsePlan([]byte(js))
}

func parsePlan(planJSON []byte) ([]ResourceChange, error) {
	var plan struct {
		ResourceChanges []struct {
			Address string `json:"address"`
			Change  struct {
				Actions []string `json:"actions"`
			} `json:"change"`
		} `json:"resource_changes"`
	}
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		return nil, fmt.Errorf("parse plan json: %w", err)
	}
	var out []ResourceChange
	for _, rc := range plan.ResourceChanges {
		switch strings.Join(rc.Change.Actions, ",") {
		case "no-op", "read", "":
			continue
		}
		out = append(out, ResourceChange{rc.Address, rc.Change.Actions})
	}
	return out, nil
}

// classify counts the upgrade's changes, skipping any that the control plan
// of the unchanged version also produced.
func classify(res *Result, changes, noise []ResourceChange) error {
	seen := map[string]bool{}
	for _, n := range noise {
		seen[n.Address+"|"+strings.Join(n.Actions, ",")] = true
	}
	for _, rc := range changes {
		a := strings.Join(rc.Actions, ",")
		if seen[rc.Address+"|"+a] {
			res.Noise++
			continue
		}
		switch a {
		case "create":
			res.Create++
		case "update":
			res.Update++
		case "delete":
			res.Delete++
		case "delete,create", "create,delete":
			res.Replace++
		default:
			return fmt.Errorf("unknown plan action %q on %s", a, rc.Address)
		}
		res.Changes = append(res.Changes, rc)
	}
	sort.Slice(res.Changes, func(i, j int) bool { return res.Changes[i].Address < res.Changes[j].Address })
	switch {
	case res.Delete+res.Replace > 0:
		res.Status = Destructive
	case res.Create+res.Update > 0:
		res.Status = Changes
	default:
		res.Status = Clean
	}
	return nil
}

func checkEndpoint(e string) error {
	if e == "" {
		return errors.New("--endpoint is required: upgrade runs `terraform apply` and must only ever target LocalStack")
	}
	u, err := url.Parse(e)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid endpoint %q", e)
	}
	if strings.HasSuffix(u.Hostname(), "amazonaws.com") {
		return fmt.Errorf("refusing real AWS endpoint %q", e)
	}
	return nil
}

// writeProviderOverride points every aws provider block in the consumer at
// LocalStack with fake credentials; the endpoint itself comes from AWS_ENDPOINT_URL.
func writeProviderOverride(dir string) error {
	mod, diags := tfconfig.LoadModule(dir)
	if diags.HasErrors() {
		return fmt.Errorf("load example: %w", diags.Err())
	}
	const body = `  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true
}
`
	var b strings.Builder
	for _, p := range mod.ProviderConfigs {
		if p.Name != "aws" {
			continue
		}
		b.WriteString("provider \"aws\" {\n")
		if p.Alias != "" {
			fmt.Fprintf(&b, "  alias = %q\n", p.Alias)
		}
		b.WriteString(body)
	}
	if b.Len() > 0 {
		return os.WriteFile(filepath.Join(dir, "ripple_override.tf"), []byte(b.String()), 0o644)
	}
	// No provider block to override: add a default one (an override file would be rejected).
	return os.WriteFile(filepath.Join(dir, "ripple_provider.tf"), []byte("provider \"aws\" {\n  region = \"us-east-1\"\n"+body), 0o644)
}

func terraform(o Options, dir string, args ...string) (string, error) {
	fmt.Fprintf(o.Log, "+ terraform %s  (%s)\n", strings.Join(args, " "), dir)
	cmd := exec.Command(o.Terraform, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"AWS_ENDPOINT_URL="+o.Endpoint,
		"AWS_ACCESS_KEY_ID=test",
		"AWS_SECRET_ACCESS_KEY=test",
		"AWS_SESSION_TOKEN=",
		"AWS_PROFILE=",
		"AWS_REGION=us-east-1",
		"TF_IN_AUTOMATION=1",
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

func git(log io.Writer, args ...string) error {
	fmt.Fprintf(log, "+ git %s\n", strings.Join(args, " "))
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %v: %s", args[0], err, tail(string(out)))
	}
	return nil
}

func isRemote(repo string) bool {
	return strings.Contains(repo, "://") || strings.HasPrefix(repo, "git@")
}

// tail keeps the end of terraform output, where the error is.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 1500 {
		s = "..." + s[len(s)-1500:]
	}
	return s
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() && d.Name() == ".terraform" {
			return filepath.SkipDir // providers/modules are re-installed by init
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
