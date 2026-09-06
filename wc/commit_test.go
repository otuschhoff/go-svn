package wc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/ra/inmem"
	_ "github.com/otuschhoff/go-svn/ra/ralocal"
	"github.com/otuschhoff/go-svn/svn"
)

func TestCommitInMemoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	repository := inmem.NewRepository("memory://wc-commit", "wc-commit-uuid")
	repository.AddRevision(inmem.Revision{
		Root: inmem.Directory(map[string]*inmem.Node{
			"trunk": inmem.Directory(map[string]*inmem.Node{
				"modified": inmem.File([]byte("old\n")),
				"deleted":  inmem.File([]byte("deleted\n")),
				"directory": inmem.Directory(map[string]*inmem.Node{
					"child": inmem.File([]byte("child\n")),
				}),
			}),
		}),
	})
	session, err := repository.Open("memory://wc-commit/trunk")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	working := filepath.Join(t.TempDir(), "working")
	database, err := Checkout(ctx, session, working, 1, UpdateOptions{Depth: svn.DepthInfinity})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := os.WriteFile(filepath.Join(working, "modified"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added := filepath.Join(working, "added")
	if err := os.WriteFile(added, []byte("added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.Add(ctx, added, AddOptions{AutoProps: map[string]svn.Props{"added": {"custom": []byte("value")}}}); err != nil {
		t.Fatal(err)
	}
	if err := database.Delete(ctx, filepath.Join(working, "deleted"), DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetProperty(ctx, working, "root-property", []byte("root-value"), false); err != nil {
		t.Fatal(err)
	}
	info, err := database.Commit(ctx, session, working, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("mixed")}, Depth: svn.DepthInfinity})
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 2 {
		t.Fatalf("commit revision = %d", info.Revision)
	}
	for name, want := range map[string]string{"modified": "new\n", "added": "added\n"} {
		var contents bytes.Buffer
		if _, _, err := session.GetFile(ctx, name, 2, &contents, false); err != nil || contents.String() != want {
			t.Fatalf("repository %s = %q, error = %v", name, contents.String(), err)
		}
	}
	if kind, err := session.CheckPath(ctx, "deleted", 2); err != nil || kind != svn.NodeNone {
		t.Fatalf("deleted kind = %v, error = %v", kind, err)
	}
	_, addedProperties, err := session.GetFile(ctx, "added", 2, nil, true)
	if err != nil || string(addedProperties["custom"]) != "value" {
		t.Fatalf("added properties = %#v, error = %v", addedProperties, err)
	}
	_, _, rootProperties, err := session.GetDir(ctx, "", 2, 0)
	if err != nil || string(rootProperties["root-property"]) != "root-value" {
		t.Fatalf("root properties = %#v, error = %v", rootProperties, err)
	}
	if err := database.Status(ctx, working, StatusOptions{Depth: svn.DepthInfinity}, func(status *Status) error {
		if status.NodeStatus != StatusNormal || status.PropertyStatus != StatusNormal {
			t.Fatalf("post-commit status = %#v", status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(working, "copied")
	if err := database.Copy(ctx, filepath.Join(working, "modified"), copied); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(working, "moved")
	if err := database.Move(ctx, filepath.Join(working, "directory"), moved, false); err != nil {
		t.Fatal(err)
	}
	info, err = database.Commit(ctx, session, working, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("copy and move")}, Depth: svn.DepthInfinity})
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 3 {
		t.Fatalf("copy/move revision = %d", info.Revision)
	}
	for name, want := range map[string]string{"copied": "new\n", "moved/child": "child\n"} {
		var contents bytes.Buffer
		if _, _, err := session.GetFile(ctx, name, 3, &contents, false); err != nil || contents.String() != want {
			t.Fatalf("repository %s = %q, error = %v", name, contents.String(), err)
		}
	}
	if kind, err := session.CheckPath(ctx, "directory", 3); err != nil || kind != svn.NodeNone {
		t.Fatalf("moved source kind = %v, error = %v", kind, err)
	}
	if err := database.Delete(ctx, copied, DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copied, []byte("replacement\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.Add(ctx, copied, AddOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	info, err = database.Commit(ctx, session, copied, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("replace")}, Depth: svn.DepthEmpty})
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 4 {
		t.Fatalf("replacement revision = %d", info.Revision)
	}
	var replacement bytes.Buffer
	if _, _, err := session.GetFile(ctx, "copied", 4, &replacement, false); err != nil || replacement.String() != "replacement\n" {
		t.Fatalf("replacement = %q, error = %v", replacement.String(), err)
	}
	special := filepath.Join(working, "special")
	if err := os.Symlink("modified", special); err != nil {
		t.Fatal(err)
	}
	if err := database.Add(ctx, special, AddOptions{}); err != nil {
		t.Fatal(err)
	}
	info, err = database.Commit(ctx, session, special, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("special")}, Depth: svn.DepthEmpty})
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 5 {
		t.Fatalf("special revision = %d", info.Revision)
	}
	var specialContents bytes.Buffer
	_, specialProperties, err := session.GetFile(ctx, "special", 5, &specialContents, true)
	if err != nil || specialContents.String() != "link modified" || string(specialProperties["svn:special"]) != "*" {
		t.Fatalf("special = %q, properties = %#v, error = %v", specialContents.String(), specialProperties, err)
	}
}

func TestCommitDepthMatrixInMemory(t *testing.T) {
	repository := inmem.NewRepository("memory://wc-commit-depths", "wc-commit-depths-uuid")
	repository.AddRevision(inmem.Revision{Root: inmem.Directory(map[string]*inmem.Node{
		"trunk": inmem.Directory(map[string]*inmem.Node{
			"root-file": inmem.File([]byte("root base\n")),
			"selected":  inmem.File([]byte("selected base\n")),
			"excluded":  inmem.File([]byte("excluded base\n")),
			"dir":       inmem.Directory(map[string]*inmem.Node{"child": inmem.File([]byte("child base\n"))}),
		}),
	})})
	session, err := repository.Open("memory://wc-commit-depths/trunk")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	runCommitDepthMatrix(t, session, 1)
}

func TestCommitDepthMatrixLocal(t *testing.T) {
	svnadmin := requireTool(t, "svnadmin")
	svnTool := requireTool(t, "svn")
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(filepath.Join(seed, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "root-file"), []byte("root base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "dir", "child"), []byte("child base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{"selected": "selected base\n", "excluded": "excluded base\n"} {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repositoryURL := (&url.URL{Scheme: "file", Path: repository}).String()
	if output, err := exec.Command(svnTool, "import", "-q", "-m", "seed", seed, repositoryURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v\n%s", err, output)
	}
	session, _, err := ra.Open(context.Background(), repositoryURL+"/trunk", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	runCommitDepthMatrix(t, session, 1)
}

func runCommitDepthMatrix(t *testing.T, session ra.Session, revision svn.Revnum) {
	t.Helper()
	ctx := context.Background()
	working := filepath.Join(t.TempDir(), "working")
	database, err := Checkout(ctx, session, working, revision, UpdateOptions{Depth: svn.DepthInfinity})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rootFile := filepath.Join(working, "root-file")
	child := filepath.Join(working, "dir", "child")
	if err := os.WriteFile(rootFile, []byte("root changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("child changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.SetProperty(ctx, working, "matrix:root", []byte("set"), false); err != nil {
		t.Fatal(err)
	}
	if err := database.SetProperty(ctx, filepath.Join(working, "dir"), "matrix:dir", []byte("set"), false); err != nil {
		t.Fatal(err)
	}
	commitTarget := func(t *testing.T, target string, options CommitOptions) {
		info, err := database.Commit(ctx, session, target, options)
		if err != nil {
			t.Fatal(err)
		}
		revision++
		if info.Revision != revision {
			t.Fatalf("commit revision = %d, want %d", info.Revision, revision)
		}
	}
	commit := func(t *testing.T, depth svn.Depth) {
		commitTarget(t, working, CommitOptions{
			RevisionProperties: svn.Props{"svn:log": []byte("depth " + depth.String())}, Depth: depth,
		})
	}
	t.Run("empty-root-properties", func(t *testing.T) {
		commit(t, svn.DepthEmpty)
		_, _, properties, err := session.GetDir(ctx, "", revision, 0)
		if err != nil || string(properties["matrix:root"]) != "set" {
			t.Fatalf("root properties = %#v, error = %v", properties, err)
		}
		assertRepositoryFile(t, ctx, session, "root-file", revision, "root base\n")
		assertRepositoryFile(t, ctx, session, "dir/child", revision, "child base\n")
	})
	t.Run("files-direct-file", func(t *testing.T) {
		commit(t, svn.DepthFiles)
		assertRepositoryFile(t, ctx, session, "root-file", revision, "root changed\n")
		assertRepositoryFile(t, ctx, session, "dir/child", revision, "child base\n")
	})
	t.Run("immediates-directory-properties", func(t *testing.T) {
		commit(t, svn.DepthImmediates)
		_, _, properties, err := session.GetDir(ctx, "dir", revision, 0)
		if err != nil || string(properties["matrix:dir"]) != "set" {
			t.Fatalf("directory properties = %#v, error = %v", properties, err)
		}
		assertRepositoryFile(t, ctx, session, "dir/child", revision, "child base\n")
	})
	t.Run("infinity-nested-file", func(t *testing.T) {
		commit(t, svn.DepthInfinity)
		assertRepositoryFile(t, ctx, session, "dir/child", revision, "child changed\n")
		if err := database.Status(ctx, working, StatusOptions{Depth: svn.DepthInfinity}, func(status *Status) error {
			if status.NodeStatus != StatusNormal || status.PropertyStatus != StatusNormal {
				t.Fatalf("post-commit status = %#v", status)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	selected := filepath.Join(working, "selected")
	excluded := filepath.Join(working, "excluded")
	if err := os.WriteFile(selected, []byte("selected one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(excluded, []byte("excluded one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.SetChangelist(ctx, selected, "commit-now"); err != nil {
		t.Fatal(err)
	}
	if err := database.SetChangelist(ctx, excluded, "commit-later"); err != nil {
		t.Fatal(err)
	}
	t.Run("changelist-filter-keep", func(t *testing.T) {
		commitTarget(t, working, CommitOptions{
			RevisionProperties: svn.Props{"svn:log": []byte("selected changelist")}, Depth: svn.DepthInfinity,
			Changelists: []string{"commit-now"}, KeepChangelists: true,
		})
		assertRepositoryFile(t, ctx, session, "selected", revision, "selected one\n")
		assertRepositoryFile(t, ctx, session, "excluded", revision, "excluded base\n")
		info, err := database.Info(ctx, selected)
		if err != nil || info.Changelist != "commit-now" {
			t.Fatalf("selected info = %#v, error = %v", info, err)
		}
	})
	t.Run("single-file-target-clear-changelist", func(t *testing.T) {
		if err := os.WriteFile(selected, []byte("selected two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, selected, CommitOptions{
			RevisionProperties: svn.Props{"svn:log": []byte("single target")}, Depth: svn.DepthEmpty,
		})
		assertRepositoryFile(t, ctx, session, "selected", revision, "selected two\n")
		info, err := database.Info(ctx, selected)
		if err != nil || info.Changelist != "" {
			t.Fatalf("selected info = %#v, error = %v", info, err)
		}
	})
	t.Run("deferred-changelist", func(t *testing.T) {
		commitTarget(t, working, CommitOptions{
			RevisionProperties: svn.Props{"svn:log": []byte("deferred changelist")}, Depth: svn.DepthInfinity,
			Changelists: []string{"commit-later"},
		})
		assertRepositoryFile(t, ctx, session, "excluded", revision, "excluded one\n")
		info, err := database.Info(ctx, excluded)
		if err != nil || info.Changelist != "" {
			t.Fatalf("excluded info = %#v, error = %v", info, err)
		}
	})
	t.Run("add-file", func(t *testing.T) {
		added := filepath.Join(working, "added")
		if err := os.WriteFile(added, []byte("added\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := database.Add(ctx, added, AddOptions{}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, added, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("add file")}, Depth: svn.DepthEmpty})
		assertRepositoryFile(t, ctx, session, "added", revision, "added\n")
	})
	t.Run("add-directory", func(t *testing.T) {
		addedDirectory := filepath.Join(working, "added-directory")
		if err := database.Mkdir(ctx, addedDirectory, false); err != nil {
			t.Fatal(err)
		}
		addedChild := filepath.Join(addedDirectory, "child")
		if err := os.WriteFile(addedChild, []byte("added child\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := database.Add(ctx, addedChild, AddOptions{}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, addedDirectory, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("add directory")}, Depth: svn.DepthInfinity})
		assertRepositoryFile(t, ctx, session, "added-directory/child", revision, "added child\n")
	})
	t.Run("delete-file", func(t *testing.T) {
		if err := database.Delete(ctx, excluded, DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, excluded, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("delete file")}, Depth: svn.DepthEmpty})
		if kind, err := session.CheckPath(ctx, "excluded", revision); err != nil || kind != svn.NodeNone {
			t.Fatalf("deleted kind = %v, error = %v", kind, err)
		}
	})
	copied := filepath.Join(working, "copied")
	t.Run("copy-file", func(t *testing.T) {
		if err := database.Copy(ctx, rootFile, copied); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, copied, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("copy file")}, Depth: svn.DepthEmpty})
		assertRepositoryFile(t, ctx, session, "copied", revision, "root changed\n")
	})
	moved := filepath.Join(working, "moved")
	t.Run("move-directory", func(t *testing.T) {
		if err := database.Move(ctx, filepath.Join(working, "dir"), moved, false); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, working, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("move directory")}, Depth: svn.DepthInfinity})
		assertRepositoryFile(t, ctx, session, "moved/child", revision, "child changed\n")
		if kind, err := session.CheckPath(ctx, "dir", revision); err != nil || kind != svn.NodeNone {
			t.Fatalf("moved source kind = %v, error = %v", kind, err)
		}
	})
	t.Run("replace-file", func(t *testing.T) {
		if err := database.Delete(ctx, copied, DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(copied, []byte("replacement\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := database.Add(ctx, copied, AddOptions{Force: true}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, copied, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("replace file")}, Depth: svn.DepthEmpty})
		assertRepositoryFile(t, ctx, session, "copied", revision, "replacement\n")
	})
	t.Run("set-file-property", func(t *testing.T) {
		if err := database.SetProperty(ctx, rootFile, "matrix:file", []byte("set"), false); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, rootFile, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("set property")}, Depth: svn.DepthEmpty})
		_, properties, err := session.GetFile(ctx, "root-file", revision, nil, true)
		if err != nil || string(properties["matrix:file"]) != "set" {
			t.Fatalf("file properties = %#v, error = %v", properties, err)
		}
	})
	t.Run("delete-file-property", func(t *testing.T) {
		if err := database.SetProperty(ctx, rootFile, "matrix:file", nil, false); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, rootFile, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("delete property")}, Depth: svn.DepthEmpty})
		_, properties, err := session.GetFile(ctx, "root-file", revision, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := properties["matrix:file"]; exists {
			t.Fatalf("deleted property remains: %#v", properties)
		}
	})
	t.Run("add-empty-file", func(t *testing.T) {
		empty := filepath.Join(working, "empty")
		if err := os.WriteFile(empty, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := database.Add(ctx, empty, AddOptions{}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, empty, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("empty file")}, Depth: svn.DepthEmpty})
		assertRepositoryFile(t, ctx, session, "empty", revision, "")
	})
	t.Run("add-file-with-property", func(t *testing.T) {
		propertyFile := filepath.Join(working, "property-file")
		if err := os.WriteFile(propertyFile, []byte("property file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := database.Add(ctx, propertyFile, AddOptions{AutoProps: map[string]svn.Props{"property-file": {"matrix:added": []byte("yes")}}}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, propertyFile, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("file with property")}, Depth: svn.DepthEmpty})
		_, properties, err := session.GetFile(ctx, "property-file", revision, nil, true)
		if err != nil || string(properties["matrix:added"]) != "yes" {
			t.Fatalf("added properties = %#v, error = %v", properties, err)
		}
	})
	t.Run("add-special-file", func(t *testing.T) {
		special := filepath.Join(working, "special")
		if err := os.Symlink("root-file", special); err != nil {
			t.Fatal(err)
		}
		if err := database.Add(ctx, special, AddOptions{}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, special, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("special file")}, Depth: svn.DepthEmpty})
		var contents bytes.Buffer
		_, properties, err := session.GetFile(ctx, "special", revision, &contents, true)
		if err != nil || contents.String() != "link root-file" || string(properties["svn:special"]) != "*" {
			t.Fatalf("special = %q, properties = %#v, error = %v", contents.String(), properties, err)
		}
	})
	t.Run("add-path-with-space", func(t *testing.T) {
		spaced := filepath.Join(working, "path with space")
		if err := os.WriteFile(spaced, []byte("space\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := database.Add(ctx, spaced, AddOptions{}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, spaced, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("space path")}, Depth: svn.DepthEmpty})
		assertRepositoryFile(t, ctx, session, "path with space", revision, "space\n")
	})
	t.Run("modify-multiple-files", func(t *testing.T) {
		if err := os.WriteFile(rootFile, []byte("root multiple\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(selected, []byte("selected multiple\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, working, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("multiple files")}, Depth: svn.DepthFiles})
		assertRepositoryFile(t, ctx, session, "root-file", revision, "root multiple\n")
		assertRepositoryFile(t, ctx, session, "selected", revision, "selected multiple\n")
	})
	copiedDirectory := filepath.Join(working, "copied-directory")
	t.Run("copy-directory", func(t *testing.T) {
		if err := database.Copy(ctx, moved, copiedDirectory); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, copiedDirectory, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("copy directory")}, Depth: svn.DepthInfinity})
		assertRepositoryFile(t, ctx, session, "copied-directory/child", revision, "child changed\n")
	})
	t.Run("delete-directory", func(t *testing.T) {
		if err := database.Delete(ctx, copiedDirectory, DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, copiedDirectory, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("delete directory")}, Depth: svn.DepthInfinity})
		if kind, err := session.CheckPath(ctx, "copied-directory", revision); err != nil || kind != svn.NodeNone {
			t.Fatalf("deleted directory kind = %v, error = %v", kind, err)
		}
		var nodes, actual int
		var nodeDetails string
		if err := database.sql.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(GROUP_CONCAT(local_relpath || '@' || op_depth || ':' || presence), '') FROM NODES WHERE wc_id=? AND (local_relpath=? OR local_relpath LIKE ?)`, database.wcID, "copied-directory", "copied-directory/%").Scan(&nodes, &nodeDetails); err != nil {
			t.Fatal(err)
		}
		if err := database.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM ACTUAL_NODE WHERE wc_id=? AND (local_relpath=? OR local_relpath LIKE ?)`, database.wcID, "copied-directory", "copied-directory/%").Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if nodes != 0 || actual != 0 {
			t.Fatalf("deleted directory metadata: nodes=%d (%s) actual=%d", nodes, nodeDetails, actual)
		}
	})
	t.Run("move-file", func(t *testing.T) {
		movedFile := filepath.Join(working, "moved-file")
		if err := database.Move(ctx, filepath.Join(working, "added"), movedFile, false); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, working, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("move file")}, Depth: svn.DepthInfinity})
		assertRepositoryFile(t, ctx, session, "moved-file", revision, "added\n")
		if kind, err := session.CheckPath(ctx, "added", revision); err != nil || kind != svn.NodeNone {
			t.Fatalf("moved file source kind = %v, error = %v", kind, err)
		}
	})
	t.Run("set-executable", func(t *testing.T) {
		if err := database.SetProperty(ctx, rootFile, "svn:executable", []byte("*"), false); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, rootFile, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("executable")}, Depth: svn.DepthEmpty})
		_, properties, err := session.GetFile(ctx, "root-file", revision, nil, true)
		if err != nil || string(properties["svn:executable"]) != "*" {
			t.Fatalf("executable properties = %#v, error = %v", properties, err)
		}
	})
	t.Run("normalize-eol", func(t *testing.T) {
		if err := database.SetProperty(ctx, rootFile, "svn:eol-style", []byte("LF"), false); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rootFile, []byte("one\r\ntwo\r\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, rootFile, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("normalize eol")}, Depth: svn.DepthEmpty})
		assertRepositoryFile(t, ctx, session, "root-file", revision, "one\ntwo\n")
	})
	t.Run("contract-keyword", func(t *testing.T) {
		if err := database.SetProperty(ctx, rootFile, "svn:keywords", []byte("Rev"), false); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rootFile, []byte("$Rev: 123 $\nkeyword\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, rootFile, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("contract keyword")}, Depth: svn.DepthEmpty})
		assertRepositoryFile(t, ctx, session, "root-file", revision, "$Rev$\nkeyword\n")
	})
	special := filepath.Join(working, "special")
	t.Run("modify-special-file", func(t *testing.T) {
		if err := os.Remove(special); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("selected", special); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, special, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("modify special")}, Depth: svn.DepthEmpty})
		assertRepositoryFile(t, ctx, session, "special", revision, "link selected")
	})
	t.Run("replace-special-with-file", func(t *testing.T) {
		if err := database.Delete(ctx, special, DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(special, []byte("regular replacement\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := database.Add(ctx, special, AddOptions{Force: true}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, special, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("replace special")}, Depth: svn.DepthEmpty})
		assertRepositoryFile(t, ctx, session, "special", revision, "regular replacement\n")
		_, properties, err := session.GetFile(ctx, "special", revision, nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := properties["svn:special"]; exists {
			t.Fatalf("replacement retained svn:special: %#v", properties)
		}
	})
	t.Run("add-binary-file", func(t *testing.T) {
		binary := filepath.Join(working, "binary")
		contents := "\x00\x01\x02binary\xff"
		if err := os.WriteFile(binary, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := database.Add(ctx, binary, AddOptions{}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, binary, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("binary")}, Depth: svn.DepthEmpty})
		assertRepositoryFile(t, ctx, session, "binary", revision, contents)
	})
	t.Run("add-large-file", func(t *testing.T) {
		large := filepath.Join(working, "large")
		contents := strings.Repeat("0123456789abcdef", 8192)
		if err := os.WriteFile(large, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := database.Add(ctx, large, AddOptions{}); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, large, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("large")}, Depth: svn.DepthEmpty})
		assertRepositoryFile(t, ctx, session, "large", revision, contents)
	})
	t.Run("modify-directory-property", func(t *testing.T) {
		if err := database.SetProperty(ctx, moved, "matrix:dir", []byte("changed"), false); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, moved, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("modify directory property")}, Depth: svn.DepthEmpty})
		_, _, properties, err := session.GetDir(ctx, "moved", revision, 0)
		if err != nil || string(properties["matrix:dir"]) != "changed" {
			t.Fatalf("directory properties = %#v, error = %v", properties, err)
		}
	})
	t.Run("delete-directory-property", func(t *testing.T) {
		if err := database.SetProperty(ctx, moved, "matrix:dir", nil, false); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, moved, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("delete directory property")}, Depth: svn.DepthEmpty})
		_, _, properties, err := session.GetDir(ctx, "moved", revision, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := properties["matrix:dir"]; exists {
			t.Fatalf("deleted directory property remains: %#v", properties)
		}
	})
	t.Run("modify-root-property", func(t *testing.T) {
		if err := database.SetProperty(ctx, working, "matrix:root", []byte("changed"), false); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, working, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("modify root property")}, Depth: svn.DepthEmpty})
		_, _, properties, err := session.GetDir(ctx, "", revision, 0)
		if err != nil || string(properties["matrix:root"]) != "changed" {
			t.Fatalf("root properties = %#v, error = %v", properties, err)
		}
	})
	t.Run("delete-root-property", func(t *testing.T) {
		if err := database.SetProperty(ctx, working, "matrix:root", nil, false); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, working, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("delete root property")}, Depth: svn.DepthEmpty})
		_, _, properties, err := session.GetDir(ctx, "", revision, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, exists := properties["matrix:root"]; exists {
			t.Fatalf("deleted root property remains: %#v", properties)
		}
	})
	depthDirectory := filepath.Join(working, "depth-directory")
	depthDirect := filepath.Join(depthDirectory, "direct")
	depthNested := filepath.Join(depthDirectory, "nested", "child")
	t.Run("add-depth-directory", func(t *testing.T) {
		if err := database.Mkdir(ctx, filepath.Dir(depthNested), true); err != nil {
			t.Fatal(err)
		}
		for filename, contents := range map[string]string{depthDirect: "direct base\n", depthNested: "nested base\n"} {
			if err := os.WriteFile(filename, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := database.Add(ctx, filename, AddOptions{}); err != nil {
				t.Fatal(err)
			}
		}
		commitTarget(t, depthDirectory, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("add depth directory")}, Depth: svn.DepthInfinity})
		assertRepositoryFile(t, ctx, session, "depth-directory/nested/child", revision, "nested base\n")
	})
	if err := os.WriteFile(depthDirect, []byte("direct changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(depthNested, []byte("nested changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Run("directory-files-depth", func(t *testing.T) {
		commitTarget(t, depthDirectory, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("directory files depth")}, Depth: svn.DepthFiles})
		assertRepositoryFile(t, ctx, session, "depth-directory/direct", revision, "direct changed\n")
		assertRepositoryFile(t, ctx, session, "depth-directory/nested/child", revision, "nested base\n")
	})
	t.Run("directory-infinity-depth", func(t *testing.T) {
		commitTarget(t, depthDirectory, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("directory infinity depth")}, Depth: svn.DepthInfinity})
		assertRepositoryFile(t, ctx, session, "depth-directory/nested/child", revision, "nested changed\n")
	})
	t.Run("reject-inconsistent-eol", func(t *testing.T) {
		if err := database.SetProperty(ctx, selected, "svn:eol-style", []byte("LF"), false); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(selected, []byte("one\r\ntwo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := database.Commit(ctx, session, selected, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("mixed eol")}, Depth: svn.DepthEmpty})
		if !errors.Is(err, svn.ErrIOInconsistentEol) {
			t.Fatalf("inconsistent EOL error = %v", err)
		}
		if err := database.Revert(ctx, selected, RevertOptions{Depth: svn.DepthEmpty}); err != nil {
			t.Fatal(err)
		}
	})
	var lock *svn.Lock
	t.Run("keep-lock", func(t *testing.T) {
		if err := session.Lock(ctx, map[string]svn.Revnum{"moved/child": revision}, "matrix", false, func(_ string, value *svn.Lock, callbackErr error) error {
			lock = value
			return callbackErr
		}); err != nil || lock == nil {
			t.Fatalf("lock = %#v, error = %v", lock, err)
		}
		if _, err := database.sql.Exec(`INSERT INTO LOCK (repos_id, repos_relpath, lock_token, lock_owner, lock_comment) VALUES (?, ?, ?, ?, ?)`, database.repository.ID, "trunk/moved/child", lock.Token, lock.Owner, lock.Comment); err != nil {
			t.Fatal(err)
		}
		movedChild := filepath.Join(moved, "child")
		if err := os.WriteFile(movedChild, []byte("keep lock\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, movedChild, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("keep lock")}, Depth: svn.DepthEmpty, KeepLocks: true})
		info, err := database.Info(ctx, movedChild)
		if err != nil || info.Revision != revision {
			t.Fatalf("post-commit info revision = %d, want %d, error = %v", info.Revision, revision, err)
		}
		current, err := session.GetLock(ctx, "moved/child")
		if err != nil || current == nil || current.Token != lock.Token {
			t.Fatalf("kept lock = %#v, error = %v", current, err)
		}
	})
	t.Run("release-lock", func(t *testing.T) {
		movedChild := filepath.Join(moved, "child")
		if err := os.WriteFile(movedChild, []byte("release lock\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, movedChild, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("release lock")}, Depth: svn.DepthEmpty})
		if current, err := session.GetLock(ctx, "moved/child"); err != nil || current != nil {
			t.Fatalf("released lock = %#v, error = %v", current, err)
		}
	})
	t.Run("set-needs-lock", func(t *testing.T) {
		if err := database.SetProperty(ctx, rootFile, "svn:needs-lock", []byte("*"), false); err != nil {
			t.Fatal(err)
		}
		commitTarget(t, rootFile, CommitOptions{RevisionProperties: svn.Props{"svn:log": []byte("needs lock")}, Depth: svn.DepthEmpty})
		_, properties, err := session.GetFile(ctx, "root-file", revision, nil, true)
		if err != nil || string(properties["svn:needs-lock"]) != "*" {
			t.Fatalf("needs-lock properties = %#v, error = %v", properties, err)
		}
	})
}

func assertRepositoryFile(t *testing.T, ctx context.Context, session ra.Session, name string, revision svn.Revnum, want string) {
	t.Helper()
	var contents bytes.Buffer
	if _, _, err := session.GetFile(ctx, name, revision, &contents, false); err != nil || contents.String() != want {
		t.Fatalf("repository %s = %q, error = %v, want %q", name, contents.String(), err, want)
	}
}

func TestCommitWorkingCopyRoundTrip(t *testing.T) {
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
	for name, contents := range map[string]string{"modified": "old\n", "deleted": "gone\n", "eol": "one\n", "translated": "$Rev$\nold\n"} {
		if err := os.WriteFile(filepath.Join(seed, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(seed, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "directory", "child"), []byte("child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repositoryURL := (&url.URL{Scheme: "file", Path: repository}).String()
	if output, err := exec.Command(svnTool, "import", "-q", "-m", "seed", seed, repositoryURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v\n%s", err, output)
	}
	session, _, err := ra.Open(context.Background(), repositoryURL+"/trunk", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	working := filepath.Join(root, "working")
	database, err := Checkout(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	eol := filepath.Join(working, "eol")
	if err := database.SetProperty(context.Background(), eol, "svn:eol-style", []byte("LF"), false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eol, []byte("one\r\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Commit(context.Background(), session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("mixed eol")}, Depth: svn.DepthInfinity,
	}); !errors.Is(err, svn.ErrIOInconsistentEol) {
		t.Fatalf("mixed-EOL commit error = %v", err)
	}
	if err := database.Revert(context.Background(), eol, RevertOptions{Depth: svn.DepthEmpty}); err != nil {
		t.Fatal(err)
	}
	translated := filepath.Join(working, "translated")
	if err := database.SetProperty(context.Background(), translated, "svn:keywords", []byte("Rev"), false); err != nil {
		t.Fatal(err)
	}
	if err := database.SetProperty(context.Background(), translated, "svn:eol-style", []byte("CRLF"), false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(translated, []byte("$Rev: 1 $\r\nchanged\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modified := filepath.Join(working, "modified")
	if err := os.WriteFile(modified, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.SetProperty(context.Background(), modified, "custom", []byte("value"), false); err != nil {
		t.Fatal(err)
	}
	if err := database.SetProperty(context.Background(), working, "root-custom", []byte("root-value"), false); err != nil {
		t.Fatal(err)
	}
	added := filepath.Join(working, "added")
	if err := os.WriteFile(added, []byte("added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.Add(context.Background(), added, AddOptions{AutoProps: map[string]svn.Props{
		"added": {"auto-custom": []byte("auto-value")},
	}}); err != nil {
		t.Fatal(err)
	}
	special := filepath.Join(working, "special")
	if err := os.Symlink("modified", special); err != nil {
		t.Fatal(err)
	}
	if err := database.Add(context.Background(), special, AddOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := database.Delete(context.Background(), filepath.Join(working, "deleted"), DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	info, err := database.Commit(context.Background(), session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("working copy commit")}, Depth: svn.DepthInfinity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 2 {
		t.Fatalf("committed revision = %d", info.Revision)
	}
	for name, want := range map[string]string{"modified": "new\n", "added": "added\n", "translated": "$Rev$\r\nchanged\r\n"} {
		var contents bytes.Buffer
		if _, _, err := session.GetFile(context.Background(), name, 2, &contents, false); err != nil || contents.String() != want {
			t.Fatalf("repository %s = %q, error = %v", name, contents.String(), err)
		}
	}
	_, addedProperties, err := session.GetFile(context.Background(), "added", 2, nil, true)
	if err != nil || string(addedProperties["auto-custom"]) != "auto-value" {
		t.Fatalf("added properties = %#v, error = %v", addedProperties, err)
	}
	_, _, rootProperties, err := session.GetDir(context.Background(), "", 2, 0)
	if err != nil || string(rootProperties["root-custom"]) != "root-value" {
		t.Fatalf("root properties = %#v, error = %v", rootProperties, err)
	}
	var specialContents bytes.Buffer
	_, specialProperties, err := session.GetFile(context.Background(), "special", 2, &specialContents, true)
	if err != nil || specialContents.String() != "link modified" || string(specialProperties["svn:special"]) != "*" {
		t.Fatalf("repository special = %q, properties = %#v, error = %v", specialContents.String(), specialProperties, err)
	}
	translatedInfo, err := database.Info(context.Background(), translated)
	if err != nil {
		t.Fatal(err)
	}
	if string(translatedInfo.BaseProperties["svn:keywords"]) != "Rev" || string(translatedInfo.BaseProperties["svn:eol-style"]) != "CRLF" {
		t.Fatalf("translated base properties = %#v", translatedInfo.BaseProperties)
	}
	pristine, err := database.OpenPristine(context.Background(), *translatedInfo.Checksum)
	if err != nil {
		t.Fatal(err)
	}
	pristineContents, readErr := io.ReadAll(pristine)
	pristine.Close()
	if readErr != nil || string(pristineContents) != "$Rev$\r\nchanged\r\n" {
		t.Fatalf("translated pristine = %q, error = %v", pristineContents, readErr)
	}
	if workingContents, err := os.ReadFile(translated); err != nil || string(workingContents) != "$Rev: 2 $\r\nchanged\r\n" {
		t.Fatalf("translated working file = %q, error = %v", workingContents, err)
	}
	if kind, err := session.CheckPath(context.Background(), "deleted", 2); err != nil || kind != svn.NodeNone {
		t.Fatalf("deleted kind = %v, error = %v", kind, err)
	}
	if output, err := exec.Command(svnTool, "status", working).CombinedOutput(); err != nil || len(output) != 0 {
		difference, _ := exec.Command(svnTool, "diff", translated).CombinedOutput()
		t.Fatalf("svn status after commit: %v\n%s\nsvn diff:\n%s", err, output, difference)
	}
	if output, err := exec.Command(svnadmin, "verify", "-q", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin verify: %v\n%s", err, output)
	}

	if err := database.Copy(context.Background(), modified, filepath.Join(working, "copied")); err != nil {
		t.Fatal(err)
	}
	if err := database.Move(context.Background(), filepath.Join(working, "directory"), filepath.Join(working, "moved"), false); err != nil {
		t.Fatal(err)
	}
	info, err = database.Commit(context.Background(), session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("copy and move")}, Depth: svn.DepthInfinity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 3 {
		t.Fatalf("copy/move revision = %d", info.Revision)
	}
	for name, want := range map[string]string{"copied": "new\n", "moved/child": "child\n"} {
		var contents bytes.Buffer
		if _, _, err := session.GetFile(context.Background(), name, 3, &contents, false); err != nil || contents.String() != want {
			t.Fatalf("repository %s = %q, error = %v", name, contents.String(), err)
		}
	}
	if kind, err := session.CheckPath(context.Background(), "directory", 3); err != nil || kind != svn.NodeNone {
		t.Fatalf("moved source kind = %v, error = %v", kind, err)
	}
	if output, err := exec.Command(svnTool, "status", working).CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("svn status after copy/move commit: %v\n%s", err, output)
	}
	replaced := filepath.Join(working, "copied")
	if err := database.Delete(context.Background(), replaced, DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(replaced, []byte("replacement\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.Add(context.Background(), replaced, AddOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	info, err = database.Commit(context.Background(), session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("replacement")}, Depth: svn.DepthInfinity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 4 {
		t.Fatalf("replacement revision = %d", info.Revision)
	}
	var replacement bytes.Buffer
	if _, _, err := session.GetFile(context.Background(), "copied", 4, &replacement, false); err != nil || replacement.String() != "replacement\n" {
		t.Fatalf("repository replacement = %q, error = %v", replacement.String(), err)
	}
	if output, err := exec.Command(svnTool, "status", working).CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("svn status after replacement commit: %v\n%s", err, output)
	}
	if err := os.WriteFile(eol, []byte("root depth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(working, "moved", "child")
	if err := os.WriteFile(nested, []byte("nested depth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err = database.Commit(context.Background(), session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("files depth")}, Depth: svn.DepthFiles,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 5 {
		t.Fatalf("files-depth revision = %d", info.Revision)
	}
	for name, want := range map[string]string{"eol": "root depth\n", "moved/child": "child\n"} {
		var contents bytes.Buffer
		if _, _, err := session.GetFile(context.Background(), name, 5, &contents, false); err != nil || contents.String() != want {
			t.Fatalf("files-depth repository %s = %q, error = %v", name, contents.String(), err)
		}
	}
	if output, err := exec.Command(svnTool, "status", working).CombinedOutput(); err != nil || !bytes.Contains(output, []byte("M       "+nested)) || bytes.Contains(output, []byte("M       "+eol)) {
		t.Fatalf("files-depth status: %v\n%s", err, output)
	}
	info, err = database.Commit(context.Background(), session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("infinity depth")}, Depth: svn.DepthInfinity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 6 {
		t.Fatalf("infinity-depth revision = %d", info.Revision)
	}
	if output, err := exec.Command(svnTool, "status", working).CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("svn status after depth commits: %v\n%s", err, output)
	}
	var lock *svn.Lock
	if err := session.Lock(context.Background(), map[string]svn.Revnum{"moved/child": 6}, "commit lock", false, func(_ string, value *svn.Lock, callbackErr error) error {
		lock = value
		return callbackErr
	}); err != nil || lock == nil {
		t.Fatalf("lock = %#v, error = %v", lock, err)
	}
	if _, err := database.sql.Exec(`INSERT INTO LOCK (repos_id, repos_relpath, lock_token, lock_owner, lock_comment)
		VALUES (?, ?, ?, ?, ?)`, database.repository.ID, "trunk/moved/child", lock.Token, lock.Owner, lock.Comment); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("keep lock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Commit(context.Background(), session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("keep lock")}, Depth: svn.DepthInfinity, KeepLocks: true,
	}); err != nil {
		t.Fatal(err)
	}
	if current, err := session.GetLock(context.Background(), "moved/child"); err != nil || current == nil || current.Token != lock.Token {
		t.Fatalf("kept repository lock = %#v, error = %v", current, err)
	}
	if err := os.WriteFile(nested, []byte("release lock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Commit(context.Background(), session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("release lock")}, Depth: svn.DepthInfinity,
	}); err != nil {
		t.Fatal(err)
	}
	if current, err := session.GetLock(context.Background(), "moved/child"); err != nil || current != nil {
		t.Fatalf("repository lock after commit = %#v, error = %v", current, err)
	}
	nestedInfo, err := database.Info(context.Background(), nested)
	if err != nil || nestedInfo.Lock != nil {
		t.Fatalf("working-copy lock after commit = %#v, error = %v", nestedInfo.Lock, err)
	}
	if err := os.WriteFile(nested, []byte("single target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err = database.Commit(context.Background(), session, nested, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("single file")}, Depth: svn.DepthEmpty,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 9 {
		t.Fatalf("single-file revision = %d", info.Revision)
	}
	var singleTarget bytes.Buffer
	if _, _, err := session.GetFile(context.Background(), "moved/child", 9, &singleTarget, false); err != nil || singleTarget.String() != "single target\n" {
		t.Fatalf("single-file repository text = %q, error = %v", singleTarget.String(), err)
	}
}

func TestCommitChangelistFiltering(t *testing.T) {
	svnadmin := requireTool(t, "svnadmin")
	svnTool := requireTool(t, "svn")
	ctx := context.Background()
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	seed := filepath.Join(root, "seed")
	if err := os.Mkdir(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"selected", "excluded"} {
		if err := os.WriteFile(filepath.Join(seed, name), []byte("base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repositoryURL := (&url.URL{Scheme: "file", Path: repository}).String()
	if output, err := exec.Command(svnTool, "import", "-q", "-m", "seed", seed, repositoryURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v\n%s", err, output)
	}
	session, _, err := ra.Open(ctx, repositoryURL+"/trunk", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	working := filepath.Join(root, "working")
	database, err := Checkout(ctx, session, working, 1, UpdateOptions{Depth: svn.DepthInfinity})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	selected := filepath.Join(working, "selected")
	excluded := filepath.Join(working, "excluded")
	if err := os.WriteFile(selected, []byte("selected one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(excluded, []byte("excluded one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.SetChangelist(ctx, selected, "commit-me"); err != nil {
		t.Fatal(err)
	}
	if err := database.SetChangelist(ctx, excluded, "later"); err != nil {
		t.Fatal(err)
	}
	commitInfo, err := database.Commit(ctx, session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("selected only")}, Depth: svn.DepthInfinity,
		Changelists: []string{"commit-me"}, KeepChangelists: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if commitInfo.Revision != 2 {
		t.Fatalf("selected revision = %d", commitInfo.Revision)
	}
	for name, want := range map[string]string{"selected": "selected one\n", "excluded": "base\n"} {
		var contents bytes.Buffer
		if _, _, err := session.GetFile(ctx, name, 2, &contents, false); err != nil || contents.String() != want {
			t.Fatalf("repository %s = %q, error = %v", name, contents.String(), err)
		}
	}
	selectedInfo, err := database.Info(ctx, selected)
	if err != nil || selectedInfo.Changelist != "commit-me" {
		t.Fatalf("selected changelist = %q, error = %v", selectedInfo.Changelist, err)
	}
	excludedInfo, err := database.Info(ctx, excluded)
	if err != nil || excludedInfo.Changelist != "later" {
		t.Fatalf("excluded changelist = %q, error = %v", excludedInfo.Changelist, err)
	}
	if err := os.WriteFile(selected, []byte("selected two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Commit(ctx, session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("clear selected changelist")}, Depth: svn.DepthInfinity,
		Changelists: []string{"commit-me"},
	}); err != nil {
		t.Fatal(err)
	}
	selectedInfo, err = database.Info(ctx, selected)
	if err != nil || selectedInfo.Changelist != "" {
		t.Fatalf("cleared changelist = %q, error = %v", selectedInfo.Changelist, err)
	}
	excludedInfo, err = database.Info(ctx, excluded)
	if err != nil || excludedInfo.Changelist != "later" {
		t.Fatalf("preserved excluded changelist = %q, error = %v", excludedInfo.Changelist, err)
	}
	reference := filepath.Join(root, "reference")
	if output, err := exec.Command(svnTool, "checkout", "-q", repositoryURL+"/trunk", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout reference: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(reference, "excluded"), []byte("repository change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "commit", "-q", "-m", "concurrent", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn commit concurrent: %v\n%s", err, output)
	}
	_, err = database.Commit(ctx, session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("stale")}, Depth: svn.DepthInfinity,
		Changelists: []string{"later"},
	})
	if !errors.Is(err, svn.ErrFSOutOfDate) && !errors.Is(err, svn.ErrFSTxnOutOfDate) && !errors.Is(err, svn.ErrFSConflict) {
		t.Fatalf("out-of-date commit error = %v", err)
	}
	contents, readErr := os.ReadFile(excluded)
	if readErr != nil || string(contents) != "excluded one\n" {
		t.Fatalf("local contents after failed commit = %q, error = %v", contents, readErr)
	}
	if err := os.WriteFile(selected, []byte("local after delete\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.SetChangelist(ctx, selected, "delete-case"); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "delete", "-q", filepath.Join(reference, "selected")).CombinedOutput(); err != nil {
		t.Fatalf("svn delete concurrent target: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "commit", "-q", "-m", "concurrent delete", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn commit concurrent delete: %v\n%s", err, output)
	}
	_, err = database.Commit(ctx, session, working, CommitOptions{
		RevisionProperties: svn.Props{"svn:log": []byte("stale after delete")}, Depth: svn.DepthInfinity,
		Changelists: []string{"delete-case"},
	})
	if !errors.Is(err, svn.ErrFSOutOfDate) {
		t.Fatalf("concurrently deleted commit error = %v", err)
	}
	contents, readErr = os.ReadFile(selected)
	if readErr != nil || string(contents) != "local after delete\n" {
		t.Fatalf("local deleted-target contents = %q, error = %v", contents, readErr)
	}
}
