package client

import "testing"

func TestTargetIsURLRejectsWindowsDrivePaths(t *testing.T) {
	for _, value := range []string{`C:\working\file`, "C:/working/file", `c:working\file`} {
		if targetIsURL(value, "windows") {
			t.Errorf("targetIsURL(%q, windows) = true", value)
		}
	}
	for _, value := range []string{"file:///C:/working/file", "https://example.test/repository"} {
		if !targetIsURL(value, "windows") {
			t.Errorf("targetIsURL(%q, windows) = false", value)
		}
	}
}
