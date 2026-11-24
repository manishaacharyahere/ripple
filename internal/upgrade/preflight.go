package upgrade

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// DefaultAllowedProviders are providers that cannot run programs or write
// arbitrary files in the runner. hashicorp/external (runs programs),
// hashicorp/local and hashicorp/archive (write files, e.g. over
// /dev/termination-log) are deliberately absent.
var DefaultAllowedProviders = []string{
	"hashicorp/aws", "hashicorp/random", "hashicorp/null", "hashicorp/tls", "hashicorp/time",
}

// ErrRejected marks configurations preflight refuses to run.
var ErrRejected = errors.New("rejected by preflight")

// preflight runs after `terraform init` and before anything executes. It reads
// every module directory init resolved (.terraform/modules/modules.json) and
// the provider lock file, and rejects whatever could run arbitrary code in the
// runner: provisioners, `data "external"`, JSON configuration it cannot
// inspect, and providers outside the allow-list.
func preflight(dir string, allowed []string) error {
	dirs := map[string]bool{dir: true}
	if b, err := os.ReadFile(filepath.Join(dir, ".terraform", "modules", "modules.json")); err == nil {
		var m struct{ Modules []struct{ Dir string } }
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("%w: unreadable modules.json: %v", ErrRejected, err)
		}
		for _, mod := range m.Modules {
			d := mod.Dir
			if !filepath.IsAbs(d) {
				d = filepath.Join(dir, d)
			}
			dirs[filepath.Clean(d)] = true
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	p := hclparse.NewParser()
	for d := range dirs {
		if js, _ := filepath.Glob(filepath.Join(d, "*.tf.json")); len(js) > 0 {
			return fmt.Errorf("%w: %s uses JSON configuration, which preflight cannot inspect", ErrRejected, js[0])
		}
		files, _ := filepath.Glob(filepath.Join(d, "*.tf"))
		for _, path := range files {
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			f, diags := p.ParseHCL(src, path)
			if diags.HasErrors() {
				return fmt.Errorf("%w: cannot parse %s: %s", ErrRejected, path, diags.Error())
			}
			if err := checkBlocks(path, f.Body.(*hclsyntax.Body).Blocks); err != nil {
				return err
			}
		}
	}
	return checkLockFile(filepath.Join(dir, ".terraform.lock.hcl"), allowed)
}

func checkBlocks(path string, blocks hclsyntax.Blocks) error {
	for _, b := range blocks {
		switch {
		case b.Type == "provisioner":
			return fmt.Errorf("%w: %s has a provisioner (%s), which runs commands", ErrRejected, path, strings.Join(b.Labels, " "))
		case b.Type == "data" && len(b.Labels) > 0 && b.Labels[0] == "external":
			return fmt.Errorf("%w: %s uses data \"external\", which runs a program", ErrRejected, path)
		}
		if err := checkBlocks(path, b.Body.Blocks); err != nil { // provisioners nest inside resources and removed blocks
			return err
		}
	}
	return nil
}

func checkLockFile(path string, allowed []string) error {
	src, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // no providers at all
	}
	if err != nil {
		return err
	}
	f, diags := hclparse.NewParser().ParseHCL(src, path)
	if diags.HasErrors() {
		return fmt.Errorf("%w: cannot parse lock file: %s", ErrRejected, diags.Error())
	}
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for _, b := range f.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "provider" || len(b.Labels) == 0 {
			continue
		}
		addr := b.Labels[0]
		short, found := strings.CutPrefix(addr, "registry.terraform.io/")
		if !found || !ok[short] {
			return fmt.Errorf("%w: provider %s is not on the allow-list", ErrRejected, addr)
		}
	}
	return nil
}
