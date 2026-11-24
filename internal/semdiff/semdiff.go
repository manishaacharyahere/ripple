// Package semdiff compares two versions of a Terraform module's public surface
// (variables, outputs, managed resources, module calls, version constraints)
// and decides the smallest semver bump that is honest about the change.
package semdiff

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-config-inspect/tfconfig"
)

type Level int

const (
	Patch Level = iota
	Minor
	Major
)

func (l Level) String() string { return [...]string{"patch", "minor", "major"}[l] }

func (l Level) MarshalText() ([]byte, error) { return []byte(l.String()), nil }

type Change struct {
	Level   Level  `json:"level"`
	Address string `json:"address"`
	Detail  string `json:"detail"`
}

// Module is a loaded module version plus the refactoring blocks tfconfig ignores.
type Module struct {
	*tfconfig.Module
	Moved     map[string]string // from -> to
	Forgotten map[string]bool   // `removed` blocks with destroy = false
}

func Load(dir string) (*Module, error) {
	m, diags := tfconfig.LoadModule(dir)
	if diags.HasErrors() {
		return nil, fmt.Errorf("load %s: %w", dir, diags.Err())
	}
	moved, forgotten, err := loadRefactors(dir)
	if err != nil {
		return nil, err
	}
	return &Module{Module: m, Moved: moved, Forgotten: forgotten}, nil
}

// Compare lists every consumer-visible change from old to new, most severe first.
func Compare(old, new *Module) []Change {
	cs := []Change{} // JSON "[]" not "null" when nothing changed
	add := func(l Level, addr, format string, a ...any) {
		cs = append(cs, Change{l, addr, fmt.Sprintf(format, a...)})
	}

	for name, ov := range old.Variables {
		addr := "var." + name
		nv, ok := new.Variables[name]
		switch {
		case !ok:
			add(Major, addr, "removed; callers that set it fail to plan")
		case normType(ov.Type) != normType(nv.Type):
			if typeCompat(ov.Type, nv.Type) {
				add(Minor, addr, "type widened %q -> %q", shortType(normType(ov.Type)), shortType(normType(nv.Type)))
			} else {
				add(Major, addr, "type narrowed %q -> %q", shortType(normType(ov.Type)), shortType(normType(nv.Type)))
			}
		case !ov.Required && nv.Required:
			add(Major, addr, "became required (default removed)")
		case ov.Required && !nv.Required:
			add(Minor, addr, "became optional")
		case !nv.Required && !reflect.DeepEqual(ov.Default, nv.Default):
			// shortcut: treated as minor; a changed default can still alter consumer plans, which Phase 2 plan checks catch.
			add(Minor, addr, "default changed %v -> %v", ov.Default, nv.Default)
		}
	}
	for name, nv := range new.Variables {
		if _, ok := old.Variables[name]; ok {
			continue
		}
		if nv.Required {
			add(Major, "var."+name, "added as required; every existing caller fails to plan")
		} else {
			add(Minor, "var."+name, "added (optional)")
		}
	}

	for name, oo := range old.Outputs {
		addr := "output." + name
		no, ok := new.Outputs[name]
		switch {
		case !ok:
			add(Major, addr, "removed; callers reading it fail to plan")
		case !oo.Sensitive && no.Sensitive:
			add(Major, addr, "became sensitive; callers using it in non-sensitive contexts fail")
		}
	}
	for name := range new.Outputs {
		if _, ok := old.Outputs[name]; !ok {
			add(Minor, "output."+name, "added")
		}
	}

	removedAddr := func(addr, what string) {
		switch {
		case new.Moved[addr] != "":
			add(Patch, addr, "%s moved to %s (no destroy)", what, new.Moved[addr])
		case new.Forgotten[addr]:
			add(Minor, addr, "%s removed from state without destroy; consumers stop managing it", what)
		default:
			add(Major, addr, "%s removed; it will be DESTROYED in every consumer", what)
		}
	}
	for key := range old.ManagedResources {
		if _, ok := new.ManagedResources[key]; !ok {
			removedAddr(key, "resource")
		}
	}
	for key := range new.ManagedResources {
		if _, ok := old.ManagedResources[key]; !ok {
			add(Minor, key, "resource added; it will be created in every consumer")
		}
	}

	for name, oc := range old.ModuleCalls {
		addr := "module." + name
		nc, ok := new.ModuleCalls[name]
		switch {
		case !ok:
			removedAddr(addr, "module call")
		case oc.Source != nc.Source || oc.Version != nc.Version:
			add(Minor, addr, "now %s %s (was %s %s)", nc.Source, nc.Version, oc.Source, oc.Version)
		}
	}
	for name := range new.ModuleCalls {
		if _, ok := old.ModuleCalls[name]; !ok {
			add(Minor, "module."+name, "module call added")
		}
	}

	compareConstraint := func(addr string, o, n []string) {
		if reflect.DeepEqual(o, n) {
			return
		}
		om, nm := minVersion(o), minVersion(n)
		switch {
		case len(o) == 0:
			add(Minor, addr, "constraint added %v", n)
		case nm[0] > om[0]:
			add(Major, addr, "minimum major version raised %v -> %v", o, n)
		case cmpVersion(nm, om) < 0:
			add(Patch, addr, "constraint loosened %v -> %v", o, n)
		default:
			add(Minor, addr, "constraint tightened %v -> %v", o, n)
		}
	}
	compareConstraint("terraform", old.RequiredCore, new.RequiredCore)
	for name, np := range new.RequiredProviders {
		var oc []string
		if op, ok := old.RequiredProviders[name]; ok {
			oc = op.VersionConstraints
		}
		compareConstraint("provider."+name, oc, np.VersionConstraints)
	}

	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Level != cs[j].Level {
			return cs[i].Level > cs[j].Level
		}
		return cs[i].Address < cs[j].Address
	})
	return cs
}

// Required is the bump the changes demand.
func Required(cs []Change) Level {
	l := Patch
	for _, c := range cs {
		l = max(l, c.Level)
	}
	return l
}

func normType(t string) string {
	t = strings.Join(strings.Fields(hclComment.ReplaceAllString(t, "")), "")
	if t == "" {
		return "any"
	}
	return t
}

var lowerBound = regexp.MustCompile(`^\s*(>=|~>|=)?\s*v?(\d+)(?:\.(\d+))?(?:\.(\d+))?`)

// minVersion is the highest lower bound any constraint requires.
func minVersion(cons []string) [3]int {
	var m [3]int
	for _, c := range cons {
		for _, part := range strings.Split(c, ",") {
			g := lowerBound.FindStringSubmatch(part)
			if g == nil {
				continue
			}
			var v [3]int
			for i := range v {
				v[i], _ = strconv.Atoi(g[i+2]) // missing parts are "" -> 0
			}
			if cmpVersion(v, m) > 0 {
				m = v
			}
		}
	}
	return m
}

func cmpVersion(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return 0
}

var hclComment = regexp.MustCompile(`(?m)(#|//).*$`)

// shortType keeps long object types readable in reports.
func shortType(t string) string {
	if len(t) > 60 {
		return t[:57] + "..."
	}
	return t
}
