package servers

import (
	"os"
	"strings"
	"testing"
)

func TestDockerComposePath(t *testing.T) {
	path := dockerComposePath(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "svnserve:") || !strings.Contains(string(content), "httpd:") {
		t.Fatalf("compose file does not define both services:\n%s", content)
	}
}

func TestDockerRequested(t *testing.T) {
	for _, value := range []string{"1", "true", "YES"} {
		t.Setenv("GOSVN_DOCKER", value)
		if !dockerRequested() {
			t.Fatalf("dockerRequested() is false for %q", value)
		}
	}
	t.Setenv("GOSVN_DOCKER", "0")
	if dockerRequested() {
		t.Fatal("dockerRequested() is true for 0")
	}
}
