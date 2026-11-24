package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const v1, v2 = "../../internal/semdiff/testdata/v1", "../../internal/semdiff/testdata/v2"

func TestExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"under-bumped fails CI", []string{"diff", "--from", "v1.0.0", "--to", "v1.1.0", v1, v2}, exitUnderBumped},
		{"honest major passes", []string{"diff", "--from", "v1.0.0", "--to", "v2.0.0", v1, v2}, 0},
		{"no versions just reports", []string{"diff", v1, v2}, 0},
		{"bad semver", []string{"diff", "--from", "1.0", "--to", "v2.0.0", v1, v2}, 1},
		{"missing dir", []string{"diff", v1, "does-not-exist"}, 1},
		{"no subcommand", nil, 1},
	} {
		var out, errb bytes.Buffer
		if got := run(tc.args, &out, &errb); got != tc.want {
			t.Errorf("%s: exit %d, want %d (stderr: %s)", tc.name, got, tc.want, errb.String())
		}
	}
}

func TestJSON(t *testing.T) {
	var out bytes.Buffer
	run([]string{"diff", "--json", "--from", "v1.0.0", "--to", "v1.1.0", v1, v2}, &out, &out)
	var r struct {
		Required, Declared, Verdict string
		Changes                     []struct{ Level, Address string }
	}
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if r.Required != "major" || r.Declared != "minor" || r.Verdict != "under-bumped" || len(r.Changes) == 0 {
		t.Fatalf("unexpected report: %+v", r)
	}
}

func TestVerdictCommand(t *testing.T) {
	dir := t.TempDir()
	ev := filepath.Join(dir, "evidence.json")
	os.WriteFile(ev, []byte(`{"repo":"r","example":"e","from":"v1","to":"v2","control":[],"upgrade":[{"address":"a.b","actions":["delete","create"]}]}`), 0o644)
	res := filepath.Join(dir, "result")
	var out, errb bytes.Buffer
	if got := run([]string{"verdict", "--evidence-file", ev, "--result-file", res}, &out, &errb); got != exitHarmful {
		t.Fatalf("exit %d, want %d (%s)", got, exitHarmful, errb.String())
	}
	b, _ := os.ReadFile(res)
	var r struct {
		Status  string
		Replace int
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Status != "destructive" || r.Replace != 1 {
		t.Fatalf("result file %s (%v)", b, err)
	}
}
