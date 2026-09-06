package wc

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestLocalOperationsReferenceCompatible(t *testing.T) {
	svnadmin := requireTool(t, "svnadmin")
	svnTool := requireTool(t, "svn")
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	seed := filepath.Join(root, "seed")
	if err := os.Mkdir(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "tracked"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "copy-source"), []byte("copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(seed, "move-source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "move-source", "child"), []byte("move\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(seed, "delete-tree"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "delete-tree", "child"), []byte("delete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(seed, "delete-tree", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "delete-tree", "nested", "child"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(seed, "delete-added"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "delete-added", "base"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repositoryURL := (&url.URL{Scheme: "file", Path: repository}).String()
	if output, err := exec.Command(svnTool, "import", "-q", "-m", "seed", seed, repositoryURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v\n%s", err, output)
	}
	working := filepath.Join(root, "working")
	if output, err := exec.Command(svnTool, "checkout", "-q", repositoryURL+"/trunk", working).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, output)
	}
	database, err := Open(context.Background(), working, Options{Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	deleteTree := filepath.Join(working, "delete-tree")
	deleteChild := filepath.Join(deleteTree, "child")
	if err := os.WriteFile(deleteChild, []byte("locally modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.Delete(context.Background(), deleteTree, DeleteOptions{}); !errors.Is(err, svn.ErrClientModified) {
		t.Fatalf("delete modified tree error = %v", err)
	}
	if contents, err := os.ReadFile(deleteChild); err != nil || string(contents) != "locally modified\n" {
		t.Fatalf("modified child after rejected delete = %q, error = %v", contents, err)
	}
	if err := database.Revert(context.Background(), deleteChild, RevertOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetChangelist(context.Background(), deleteChild, "review"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(deleteChild, []byte("changelist edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.Revert(context.Background(), deleteChild, RevertOptions{}); err != nil {
		t.Fatal(err)
	}
	if info, err := database.Info(context.Background(), deleteChild); err != nil || info.Changelist != "review" {
		t.Fatalf("changelist after revert = %q, error = %v", info.Changelist, err)
	}
	if err := database.SetChangelist(context.Background(), deleteChild, ""); err != nil {
		t.Fatal(err)
	}
	nestedDeleteChild := filepath.Join(deleteTree, "nested", "child")
	if err := os.WriteFile(deleteChild, []byte("direct edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nestedDeleteChild, []byte("nested edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.Revert(context.Background(), deleteTree, RevertOptions{Depth: svn.DepthFiles}); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(deleteChild); err != nil || string(contents) != "delete\n" {
		t.Fatalf("depth-files direct child = %q, error = %v", contents, err)
	}
	if contents, err := os.ReadFile(nestedDeleteChild); err != nil || string(contents) != "nested edit\n" {
		t.Fatalf("depth-files nested child = %q, error = %v", contents, err)
	}
	if err := database.Revert(context.Background(), deleteTree, RevertOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	deleteAdded := filepath.Join(working, "delete-added")
	addedChild := filepath.Join(deleteAdded, "added")
	if err := os.WriteFile(addedChild, []byte("added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.Add(context.Background(), addedChild, AddOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := database.Delete(context.Background(), deleteAdded, DeleteOptions{}); !errors.Is(err, svn.ErrClientModified) {
		t.Fatalf("delete tree with added child error = %v", err)
	}
	if err := database.Delete(context.Background(), deleteAdded, DeleteOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	var addedRows int
	if err := database.sql.QueryRow(`SELECT COUNT(*) FROM NODES_CURRENT WHERE wc_id=? AND local_relpath=?`, database.wcID, "delete-added/added").Scan(&addedRows); err != nil || addedRows != 0 {
		t.Fatalf("added child rows after forced delete = %d, error = %v", addedRows, err)
	}

	added := filepath.Join(working, "added")
	if err := os.WriteFile(added, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.Add(context.Background(), added, AddOptions{}); err != nil {
		t.Fatal(err)
	}
	addedMoved := filepath.Join(working, "added-moved")
	if err := database.Move(context.Background(), added, addedMoved, false); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(working, "newdir")
	if err := database.Mkdir(context.Background(), directory, false); err != nil {
		t.Fatal(err)
	}
	nestedDirectory := filepath.Join(working, "parents", "nested")
	if err := database.Mkdir(context.Background(), nestedDirectory, true); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(working, "tracked")
	if err := database.SetProperty(context.Background(), tracked, "custom", []byte("value"), false); err != nil {
		t.Fatal(err)
	}
	if err := database.SetChangelist(context.Background(), tracked, "review"); err != nil {
		t.Fatal(err)
	}
	if err := database.Delete(context.Background(), tracked, DeleteOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(working, "copied")
	if err := database.Copy(context.Background(), filepath.Join(working, "copy-source"), copied); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(working, "moved")
	moveSource := filepath.Join(working, "move-source")
	if err := os.WriteFile(filepath.Join(moveSource, "child"), []byte("locally moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.Move(context.Background(), moveSource, moved, false); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(filepath.Join(moved, "child")); err != nil || string(contents) != "locally moved\n" {
		t.Fatalf("moved child = %q, error = %v", contents, err)
	}
	output, err := exec.Command(svnTool, "status", working).CombinedOutput()
	if err != nil {
		t.Fatalf("svn status: %v\n%s", err, output)
	}
	status := string(output)
	for _, expected := range []string{"A       " + addedMoved, "A  +    " + copied, "A       " + directory, "A       " + filepath.Dir(nestedDirectory), "A       " + nestedDirectory, "D       " + tracked, "D       " + moveSource, "A  +    " + moved} {
		if !strings.Contains(status, expected) {
			t.Fatalf("svn status missing %q:\n%s", expected, status)
		}
	}
	if err := database.SetChangelist(context.Background(), tracked, ""); err != nil {
		t.Fatal(err)
	}
	if err := database.Revert(context.Background(), working, RevertOptions{Depth: svn.DepthInfinity, RemoveAdded: true}); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "status", working).CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("status after revert: %v\n%s", err, output)
	}
}
