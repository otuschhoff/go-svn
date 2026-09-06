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
	"testing"

	"github.com/otuschhoff/go-svn/ra"
	_ "github.com/otuschhoff/go-svn/ra/ralocal"
	"github.com/otuschhoff/go-svn/svn"
)

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
