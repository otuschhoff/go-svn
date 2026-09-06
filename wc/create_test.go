package wc

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestCreateReferenceCompatibleWorkingCopy(t *testing.T) {
	svnTool := requireTool(t, "svn")
	root := filepath.Join(t.TempDir(), "working")
	database, err := Create(context.Background(), root, CreateOptions{
		RepositoryRoot: "file:///tmp/repository", RepositoryUUID: "00000000-0000-0000-0000-000000000001",
		RepositoryPath: "trunk", Revision: 0, Depth: svn.DepthInfinity,
	})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := database.AcquireLock(context.Background(), root, -1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.AcquireLock(context.Background(), root, -1); !errors.Is(err, svn.ErrWCLocked) {
		t.Fatalf("second lock error = %v", err)
	}
	if err := lock.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"info", root}, {"status", root}, {"cleanup", root}} {
		if output, err := exec.Command(svnTool, arguments...).CombinedOutput(); err != nil {
			t.Fatalf("svn %v: %v\n%s", arguments, err, output)
		}
	}
}
