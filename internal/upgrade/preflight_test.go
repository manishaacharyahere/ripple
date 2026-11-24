package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPreflight(t *testing.T) {
	const lockAWS = `provider "registry.terraform.io/hashicorp/aws" {
  version = "5.80.0"
}
`
	for _, tc := range []struct {
		name  string
		files map[string]string // path relative to the consumer dir
		ok    bool
	}{
		{"plain module", map[string]string{
			"main.tf":                         `module "m" { source = "../mod" }`,
			"../mod/main.tf":                  `resource "aws_s3_bucket" "b" { bucket = "x" }`,
			".terraform.lock.hcl":             lockAWS,
			".terraform/modules/modules.json": `{"Modules":[{"Key":"","Dir":"."},{"Key":"m","Dir":"../mod"}]}`,
		}, true},
		{"provisioner in a resolved module", map[string]string{
			"main.tf": `module "m" { source = "../mod" }`,
			"../mod/main.tf": `resource "null_resource" "x" {
  provisioner "local-exec" { command = "echo pwned > /dev/termination-log" }
}`,
			".terraform/modules/modules.json": `{"Modules":[{"Key":"m","Dir":"../mod"}]}`,
		}, false},
		{"destroy-time provisioner inside removed", map[string]string{
			"main.tf": `removed {
  from = aws_s3_bucket.b
  provisioner "local-exec" {
    when    = destroy
    command = "x"
  }
}`,
		}, false},
		{"external data source", map[string]string{
			"main.tf": `data "external" "x" { program = ["sh", "-c", "id"] }`,
		}, false},
		{"provider not on the allow-list", map[string]string{
			"main.tf": `resource "local_file" "f" {}`,
			".terraform.lock.hcl": `provider "registry.terraform.io/hashicorp/local" {
  version = "2.5.0"
}
`,
		}, false},
		{"provider from another registry", map[string]string{
			"main.tf":             `resource "x_y" "z" {}`,
			".terraform.lock.hcl": "provider \"example.com/evil/aws\" {\n  version = \"1.0.0\"\n}\n",
		}, false},
		{"JSON configuration", map[string]string{"main.tf.json": `{}`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "consumer")
			for p, body := range tc.files {
				full := filepath.Join(dir, filepath.FromSlash(p))
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := preflight(dir, DefaultAllowedProviders)
			if tc.ok && err != nil {
				t.Fatalf("unexpected rejection: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrRejected) {
				t.Fatalf("want ErrRejected, got %v", err)
			}
		})
	}
}

func TestVerdictFromEvidence(t *testing.T) {
	upd := ResourceChange{"aws_s3_bucket.this", []string{"update"}}
	del := ResourceChange{"aws_s3_bucket.old", []string{"delete"}}
	for _, tc := range []struct {
		name string
		ev   Evidence
		want Status
	}{
		{"noise only", Evidence{Control: []ResourceChange{upd}, Upgrade: []ResourceChange{upd}}, Clean},
		{"destroy", Evidence{Upgrade: []ResourceChange{del}}, Destructive},
		{"rejected passes through", Evidence{Status: Rejected, Error: "preflight"}, Rejected},
		{"plan failure passes through", Evidence{Status: PlanFailed}, PlanFailed},
	} {
		res, err := Verdict(&tc.ev)
		if err != nil || res.Status != tc.want {
			t.Errorf("%s: got %v, %v; want %s", tc.name, res, err, tc.want)
		}
	}
	// Evidence claiming a success status it may not claim is refused, not trusted.
	if _, err := Verdict(&Evidence{Status: Clean}); err == nil {
		t.Error("evidence must not be able to assert its own verdict")
	}
}
