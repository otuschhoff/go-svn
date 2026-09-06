package repos

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCreateReferenceCompatibility(t *testing.T) {
	svnadmin, adminErr := exec.LookPath("svnadmin")
	svn, svnErr := exec.LookPath("svn")
	if adminErr != nil || svnErr != nil {
		t.Skip("Subversion command-line tools are not installed")
	}
	for _, format := range []int{6, 7, 8} {
		t.Run(fmt.Sprintf("format%d", format), func(t *testing.T) {
			repositoryPath := filepath.Join(t.TempDir(), "repository")
			repository, err := Create(context.Background(), repositoryPath, CreateOptions{Format: format, ShardSize: 1000})
			if err != nil {
				t.Fatal(err)
			}
			if youngest, err := repository.Youngest(context.Background()); err != nil || youngest != 0 {
				t.Fatalf("youngest = %d, error = %v", youngest, err)
			}
			if output, err := exec.Command(svnadmin, "verify", repositoryPath).CombinedOutput(); err != nil {
				t.Fatalf("svnadmin verify: %v\n%s", err, output)
			}
			checkoutPath := filepath.Join(t.TempDir(), "checkout")
			if output, err := exec.Command(svn, "checkout", "--non-interactive", "file://"+repositoryPath, checkoutPath).CombinedOutput(); err != nil {
				t.Fatalf("svn checkout: %v\n%s", err, output)
			}
		})
	}
}
