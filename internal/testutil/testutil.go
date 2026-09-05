// Package testutil provides shared helpers for unit and integration tests.
package testutil

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var updateGoldens = flag.Bool("update", false, "update golden test files")

// TempDir returns a temporary directory that is removed when the test ends.
func TempDir(t testing.TB) string {
	t.Helper()
	return t.TempDir()
}

// Must returns value or fails the current test when err is non-nil.
func Must[T any](t testing.TB, value T, err error) T {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return value
}

// Golden compares got with name, or replaces name when -update is set.
func Golden(t testing.TB, name string, got []byte) {
	t.Helper()

	if *updateGoldens {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatalf("create golden directory: %v", err)
		}
		if err := os.WriteFile(name, got, 0o644); err != nil {
			t.Fatalf("update golden %q: %v", name, err)
		}
		return
	}

	want, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read golden %q: %v (run tests with -update to create it)", name, err)
	}
	if bytes.Equal(got, want) {
		return
	}

	offset := firstDifference(want, got)
	t.Fatalf("golden %q differs at byte %d (want %d bytes, got %d); run tests with -update to replace it", name, offset, len(want), len(got))
}

// FindTool locates name, preferring the executable named by envVar.
// The second return value explains why discovery failed and is empty on success.
func FindTool(name, envVar string) (string, string) {
	if name == "" {
		return "", "tool name is empty"
	}

	if envVar != "" {
		if override := strings.TrimSpace(os.Getenv(envVar)); override != "" {
			path, err := exec.LookPath(override)
			if err != nil {
				return "", fmt.Sprintf("%s=%q is not executable or was not found: %v", envVar, override, err)
			}
			return path, ""
		}
	}

	candidates := []string{name}
	if name == "httpd" {
		candidates = append(candidates, "apache2")
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, ""
		}
	}

	if envVar == "" {
		return "", fmt.Sprintf("%s was not found in PATH", name)
	}
	return "", fmt.Sprintf("%s was not found in PATH; set %s to its executable", name, envVar)
}

// RequireTool returns a discovered tool or skips the current test.
func RequireTool(t testing.TB, name, envVar string) string {
	t.Helper()
	path, reason := FindTool(name, envVar)
	if path == "" {
		t.Skip(reason)
	}
	return path
}

// SkipUnlessIntegration skips tests unless they were built with -tags integration.
func SkipUnlessIntegration(t testing.TB) {
	t.Helper()
	if !integrationEnabled {
		t.Skip("integration test requires: go test -tags integration")
	}
}

func firstDifference(a, b []byte) int {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	for index := 0; index < limit; index++ {
		if a[index] != b[index] {
			return index
		}
	}
	return limit
}
