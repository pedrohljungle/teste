//go:build e2e

package core

import (
	"fmt"
	"os"
	"path/filepath"
)

// repoFile resolves a path relative to the repository root.
//
// It walks up looking for go.mod instead of counting "../.." from this file: the working
// directory of a test binary is the directory of the TEST package, not of the package the
// helper lives in, so a fixed relative path silently points one level off the moment the
// helpers move to a subpackage. That is exactly what happened once.
func repoFile(path string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, path), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above the working directory, looking for %s", path)
		}
		dir = parent
	}
}
