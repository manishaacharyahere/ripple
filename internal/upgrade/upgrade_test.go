package upgrade

import "testing"

func TestClassify(t *testing.T) {
	plan := func(actions ...string) []byte {
		js := `{"resource_changes":[`
		for i, a := range actions {
			if i > 0 {
				js += ","
			}
			js += `{"address":"r` + string(rune('a'+i)) + `","change":{"actions":[` + a + `]}}`
		}
		return []byte(js + `]}`)
	}
	for _, tc := range []struct {
		name string
		plan []byte
		want Status
		n    int
	}{
		{"empty", plan(), Clean, 0},
		{"no-op and reads ignored", plan(`"no-op"`, `"read"`), Clean, 0},
		{"create", plan(`"create"`, `"no-op"`), Changes, 1},
		{"update", plan(`"update"`), Changes, 1},
		{"replace", plan(`"delete","create"`), Destructive, 1},
		{"create-before-destroy", plan(`"create","delete"`), Destructive, 1},
		{"delete", plan(`"update"`, `"delete"`), Destructive, 2},
	} {
		r := &Result{}
		cs, err := parsePlan(tc.plan)
		if err == nil {
			err = classify(r, cs, nil)
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if r.Status != tc.want || len(r.Changes) != tc.n {
			t.Errorf("%s: status %s with %d changes, want %s with %d", tc.name, r.Status, len(r.Changes), tc.want, tc.n)
		}
	}
	cs, _ := parsePlan(plan(`"explode"`))
	if err := classify(&Result{}, cs, nil); err == nil {
		t.Error("unknown action must be an error, not silently ignored")
	}
}

func TestNoiseIsSubtracted(t *testing.T) {
	upd := ResourceChange{"aws_s3_bucket.this", []string{"update"}}
	repl := ResourceChange{"aws_s3_bucket.this", []string{"delete", "create"}}
	r := &Result{}
	// Same update in the control plan: emulator noise, not the upgrade.
	if err := classify(r, []ResourceChange{upd}, []ResourceChange{upd}); err != nil {
		t.Fatal(err)
	}
	if r.Status != Clean || r.Noise != 1 {
		t.Errorf("got %s noise=%d, want clean noise=1", r.Status, r.Noise)
	}
	// A replace on a resource that only had noisy updates is still real.
	r = &Result{}
	classify(r, []ResourceChange{repl}, []ResourceChange{upd})
	if r.Status != Destructive || r.Noise != 0 {
		t.Errorf("got %s noise=%d, want destructive noise=0", r.Status, r.Noise)
	}
}

func TestEndpointGuard(t *testing.T) {
	for e, ok := range map[string]bool{
		"":                                   false,
		"localstack:4566":                    false, // no scheme -> no host
		"https://s3.us-east-1.amazonaws.com": false,
		"https://sts.amazonaws.com":          false,
		"http://localhost:4566":              true,
		"http://localstack.ripple.svc:4566":  true,
	} {
		if err := checkEndpoint(e); (err == nil) != ok {
			t.Errorf("checkEndpoint(%q) err=%v, want ok=%v", e, err, ok)
		}
	}
}
