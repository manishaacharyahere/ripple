package semdiff

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// loadRefactors reads `moved` and `removed` blocks, which tell Terraform a
// resource disappeared from the code without being destroyed.
func loadRefactors(dir string) (moved map[string]string, forgotten map[string]bool, err error) {
	moved, forgotten = map[string]string{}, map[string]bool{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.tf"))
	p := hclparse.NewParser()
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		f, diags := p.ParseHCL(src, path)
		if diags.HasErrors() {
			continue // pre-0.12 HCL1 syntax; moved/removed blocks need Terraform >= 1.1 anyway
		}
		for _, b := range f.Body.(*hclsyntax.Body).Blocks {
			switch b.Type {
			case "moved":
				from, to := addrAttr(src, b, "from"), addrAttr(src, b, "to")
				if from != "" && to != "" {
					moved[from] = to
				}
			case "removed":
				if from := addrAttr(src, b, "from"); from != "" && !destroys(b) {
					forgotten[from] = true
				}
			}
		}
	}
	return moved, forgotten, nil
}

// addrAttr returns an address attribute as written, minus instance keys,
// e.g. `aws_s3_bucket.this[0]` -> `aws_s3_bucket.this`.
func addrAttr(src []byte, b *hclsyntax.Block, name string) string {
	a, ok := b.Body.Attributes[name]
	if !ok {
		return ""
	}
	s := strings.Join(strings.Fields(string(a.Expr.Range().SliceBytes(src))), "")
	if i := strings.IndexByte(s, '['); i >= 0 {
		s = s[:i]
	}
	return s
}

// destroys reports whether a `removed` block destroys the object (the default).
func destroys(b *hclsyntax.Block) bool {
	for _, lc := range b.Body.Blocks {
		if lc.Type != "lifecycle" {
			continue
		}
		if a, ok := lc.Body.Attributes["destroy"]; ok {
			v, diags := a.Expr.Value(&hcl.EvalContext{})
			if !diags.HasErrors() && v.Type().FriendlyName() == "bool" && v.False() {
				return false
			}
		}
	}
	return true
}
