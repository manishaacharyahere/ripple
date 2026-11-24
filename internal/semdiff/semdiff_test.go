package semdiff

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-config-inspect/tfconfig"
)

func TestCompareFixtures(t *testing.T) {
	old, err := Load("testdata/v1")
	if err != nil {
		t.Fatal(err)
	}
	new, err := Load("testdata/v2")
	if err != nil {
		t.Fatal(err)
	}
	cs := Compare(old, new)

	got := map[string]Level{}
	for _, c := range cs {
		got[c.Address] = c.Level
	}
	want := map[string]Level{
		"var.legacy_acl":                             Major, // removed
		"var.kms_key":                                Major, // became required
		"var.owner":                                  Major, // added required
		"var.versioning":                             Minor, // default changed
		"var.lifecycle_days":                         Minor, // added optional
		"output.domain":                              Major, // removed
		"output.arn":                                 Major, // became sensitive
		"output.region":                              Minor, // added
		"aws_s3_bucket_acl.this":                     Major, // destroyed
		"aws_s3_bucket_policy.old":                   Patch, // moved
		"aws_s3_bucket_logging.this":                 Minor, // removed block, destroy = false
		"aws_s3_bucket_policy.main":                  Minor, // new address (moved target)
		"aws_s3_bucket_lifecycle_configuration.this": Minor,
		"module.labels":                              Minor,
		"provider.aws":                               Major, // >= 4.0 -> >= 5.0
	}
	for addr, l := range want {
		if g, ok := got[addr]; !ok || g != l {
			t.Errorf("%s: got %v (present=%v), want %v", addr, g, ok, l)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected extra changes: %v", cs) // e.g. var.tags whitespace-only type change must not appear
	}
	if Required(cs) != Major {
		t.Errorf("required = %v, want major", Required(cs))
	}
	if cs[0].Level != Major || cs[len(cs)-1].Level != Patch {
		t.Errorf("changes not sorted by severity: %v", cs)
	}
}

func TestIdenticalIsPatch(t *testing.T) {
	m, err := Load("testdata/v1")
	if err != nil {
		t.Fatal(err)
	}
	if cs := Compare(m, m); len(cs) != 0 || Required(cs) != Patch {
		t.Fatalf("got %v", cs)
	}
}

func TestDeclared(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		want     Level
		err      bool
	}{
		{"v1.2.3", "v2.0.0", Major, false},
		{"v1.2.3", "v1.3.0", Minor, false},
		{"v1.2.3", "v1.2.4", Patch, false},
		{"1.2.3", "1.2.4-rc.1", Patch, false},
		{"v0.4.1", "v0.5.0", Major, false}, // 0.x: minor is breaking
		{"v0.4.1", "v0.4.2", Minor, false},
		{"v1.2", "v1.3.0", 0, true},
		{"latest", "v1.0.0", 0, true},
	} {
		got, err := Declared(tc.from, tc.to)
		if (err != nil) != tc.err || (!tc.err && got != tc.want) {
			t.Errorf("Declared(%s,%s) = %v,%v want %v err=%v", tc.from, tc.to, got, err, tc.want, tc.err)
		}
	}
}

func TestConstraintRules(t *testing.T) {
	for _, tc := range []struct {
		old, new []string
		want     Level
	}{
		{nil, []string{"~> 2.35"}, Minor},               // first constraint
		{[]string{">= 4.0"}, []string{">= 5.0"}, Major}, // major floor raised
		{[]string{">= 4.5"}, []string{">= 4.9"}, Minor}, // floor raised within major
		{[]string{">= 1.10"}, []string{">= 1.5.7"}, Patch},
		{[]string{">= 3.0, < 6.0"}, []string{">= 3.0, < 7.0"}, Minor}, // shortcut: upper-bound changes count as minor
	} {
		m := func(c []string) *Module {
			return &Module{Module: &tfconfig.Module{RequiredCore: c}}
		}
		cs := Compare(m(tc.old), m(tc.new))
		if len(cs) != 1 || cs[0].Level != tc.want {
			t.Errorf("%v -> %v: got %v, want %v", tc.old, tc.new, cs, tc.want)
		}
	}
}

func TestTypeRules(t *testing.T) {
	v := func(typ string) *Module {
		return &Module{Module: &tfconfig.Module{Variables: map[string]*tfconfig.Variable{
			"x": {Name: "x", Type: typ, Default: nil},
		}}}
	}
	for _, tc := range []struct {
		old, new string
		want     []Level
	}{
		{"list(any)", "any", []Level{Minor}},                // widened
		{"any", "list(object({a=string}))", []Level{Major}}, // narrowed
		{"object({a=string})", "object({a=string, b=optional(number)})", []Level{Minor}},
		{"object({a=string})", "object({a=string, b=number})", []Level{Major}},           // new required attr
		{"object({a=string, b=optional(number)})", "object({a=string})", []Level{Major}}, // attr dropped
		{"object({a=optional(string)})", "object({a=string})", []Level{Major}},           // became required
		{"map(number)", "map(string)", []Level{Minor}},                                   // auto-converts
		{"list(string)", "set(string)", []Level{Major}},
		{"list", "any", []Level{Minor}},           // 0.11 bare keyword = list(any)
		{"map", "map(string)", []Level{Major}},    // map(any) -> map(string) narrows
		{"\"list\"", "list(any)", []Level{Minor}}, // quoted 0.11 form, same type, still reported
		{"string", "bool", []Level{Major}},
		{"list(object({a=string}))", "list(object({a=string, b=optional(bool, true)}))", []Level{Minor}},
		{"object({\n # note\n a = string\n})", "object({a=string})", nil}, // comments and spacing only
	} {
		var got []Level
		for _, c := range Compare(v(tc.old), v(tc.new)) {
			got = append(got, c.Level)
		}
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%q -> %q: got %v want %v", tc.old, tc.new, got, tc.want)
		}
	}
}
