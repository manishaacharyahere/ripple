// Package gitsrc materialises a module at a git ref without touching the
// caller's working tree.
package gitsrc

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Extract writes the tree of repo at ref into dst using `git archive`.
func Extract(repo, ref, dst string) error {
	var stderr bytes.Buffer
	cmd := exec.Command("git", "-C", repo, "archive", "--format=tar", ref)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git archive %s: %v: %s", ref, err, strings.TrimSpace(stderr.String()))
	}
	tr := tar.NewReader(bytes.NewReader(out))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue // dirs are created on demand; symlinks are skipped on purpose
		}
		target := filepath.Join(dst, filepath.FromSlash(h.Name))
		if !strings.HasPrefix(target, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("refusing archive entry outside destination: %q", h.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		f, err := os.Create(target)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, tr)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
}
