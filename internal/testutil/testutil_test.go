package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGoldenUpdateFlow(t *testing.T) {
	original := *updateGoldens
	t.Cleanup(func() { *updateGoldens = original })

	name := filepath.Join(TempDir(t), "nested", "value.golden")
	*updateGoldens = true
	Golden(t, name, []byte("first\n"))

	*updateGoldens = false
	Golden(t, name, []byte("first\n"))

	*updateGoldens = true
	Golden(t, name, []byte("second\n"))
	*updateGoldens = false
	Golden(t, name, []byte("second\n"))
}

func TestGoldenMismatch(t *testing.T) {
	if os.Getenv("GO_SVN_GOLDEN_MISMATCH_HELPER") == "1" {
		*updateGoldens = false
		Golden(t, os.Getenv("GO_SVN_GOLDEN_PATH"), []byte("actual\n"))
		return
	}

	name := filepath.Join(TempDir(t), "value.golden")
	if err := os.WriteFile(name, []byte("expected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestGoldenMismatch$")
	command.Env = append(os.Environ(),
		"GO_SVN_GOLDEN_MISMATCH_HELPER=1",
		"GO_SVN_GOLDEN_PATH="+name,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("mismatched golden unexpectedly passed")
	}
	if !strings.Contains(string(output), "differs at byte") {
		t.Fatalf("mismatch output did not explain the difference:\n%s", output)
	}
}

func TestFindToolOverride(t *testing.T) {
	goName := "go"
	if runtime.GOOS == "windows" {
		goName += ".exe"
	}
	goPath := filepath.Join(runtime.GOROOT(), "bin", goName)
	t.Setenv("GO_SVN_TEST_GO", goPath)

	got, reason := FindTool("unused-name", "GO_SVN_TEST_GO")
	if reason != "" {
		t.Fatalf("FindTool returned a reason on success: %s", reason)
	}
	if got == "" {
		t.Fatal("FindTool returned an empty path")
	}
}

func TestFindToolFailureReason(t *testing.T) {
	t.Setenv("GO_SVN_MISSING_TOOL", filepath.Join(TempDir(t), "missing"))

	path, reason := FindTool("missing-go-svn-tool", "GO_SVN_MISSING_TOOL")
	if path != "" {
		t.Fatalf("FindTool returned unexpected path %q", path)
	}
	if !strings.Contains(reason, "GO_SVN_MISSING_TOOL") {
		t.Fatalf("failure reason %q does not name the override", reason)
	}
}

func TestMust(t *testing.T) {
	if got := Must(t, 42, nil); got != 42 {
		t.Fatalf("Must returned %d, want 42", got)
	}
}

func TestFirstDifference(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want int
	}{
		{name: "equal", a: "abc", b: "abc", want: 3},
		{name: "middle", a: "abc", b: "axc", want: 1},
		{name: "shorter", a: "abc", b: "ab", want: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := firstDifference([]byte(test.a), []byte(test.b)); got != test.want {
				t.Fatalf("firstDifference() = %d, want %d", got, test.want)
			}
		})
	}
}
