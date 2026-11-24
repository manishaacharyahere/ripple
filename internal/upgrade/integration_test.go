package upgrade

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Needs terraform on PATH and LocalStack at RIPPLE_ENDPOINT (e.g. http://localhost:4566).
func TestUpgradeAgainstLocalStack(t *testing.T) {
	endpoint := os.Getenv("RIPPLE_ENDPOINT")
	if endpoint == "" {
		t.Skip("RIPPLE_ENDPOINT not set")
	}
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not on PATH")
	}
	if _, err := (&http.Client{Timeout: 3 * time.Second}).Get(endpoint + "/_localstack/health"); err != nil {
		t.Skipf("LocalStack not reachable: %v", err)
	}

	repo := t.TempDir()
	gitT := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, body string) {
		t.Helper()
		p := filepath.Join(repo, path)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	release := func(tag, mainTF string) {
		write("main.tf", mainTF)
		gitT("add", "-A")
		gitT("commit", "-q", "-m", tag)
		gitT("tag", tag)
	}

	gitT("init", "-q")
	write("versions.tf", `terraform {
  required_providers {
    aws = { source = "hashicorp/aws", version = ">= 5.30" }
  }
}
variable "name" { type = string }
`)
	write("examples/basic/main.tf", `provider "aws" { region = "us-east-1" }
module "bucket" {
  source = "../../"
  name   = "ripple-it-bucket"
}
`)
	release("v1.0.0", `resource "aws_s3_bucket" "data" { bucket = var.name }`)
	release("v1.1.0", `resource "aws_s3_bucket" "main" { bucket = var.name }`) // rename, no moved block
	release("v1.1.1", `resource "aws_s3_bucket" "main" { bucket = var.name }
moved {
  from = aws_s3_bucket.data
  to   = aws_s3_bucket.main
}`)
	release("v1.2.0", `resource "aws_s3_bucket" "main" { bucket = var.name }
moved {
  from = aws_s3_bucket.data
  to   = aws_s3_bucket.main
}
data "external" "x" { program = ["sh", "-c", "echo '{}'"] }`) // new version tries to run a program

	cache := filepath.Join(os.TempDir(), "ripple-tf-plugin-cache")
	os.MkdirAll(cache, 0o755)
	t.Setenv("TF_PLUGIN_CACHE_DIR", cache)

	for _, tc := range []struct {
		to   string
		want Status
	}{
		{"v1.0.0", Clean},       // control: same version
		{"v1.1.0", Destructive}, // bucket destroyed and recreated
		{"v1.1.1", Clean},       // moved block makes the rename free
		{"v1.2.0", Rejected},    // preflight stops it before anything executes
	} {
		t.Run(tc.to, func(t *testing.T) {
			res, err := Run(Options{Repo: repo, From: "v1.0.0", To: tc.to, Example: "examples/basic",
				Endpoint: endpoint, WorkDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tc.want {
				t.Fatalf("status %s (err %q, changes %v), want %s", res.Status, res.Error, res.Changes, tc.want)
			}
		})
	}
}
