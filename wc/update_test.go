package wc

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/ra/inmem"
	_ "github.com/otuschhoff/go-svn/ra/ralocal"
	"github.com/otuschhoff/go-svn/svn"
)

func TestInMemorySparseCheckoutAndUpdate(t *testing.T) {
	ctx := context.Background()
	repository := inmem.NewRepository("memory://wc-update", "wc-update-uuid")
	repository.AddRevision(inmem.Revision{
		Root: inmem.Directory(map[string]*inmem.Node{
			"trunk": inmem.Directory(map[string]*inmem.Node{
				"root-file": inmem.File([]byte("one\n")),
				"dir":       inmem.Directory(map[string]*inmem.Node{"child": inmem.File([]byte("child one\n"))}),
			}),
		}),
	})
	session, err := repository.Open("memory://wc-update/trunk")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	working := filepath.Join(t.TempDir(), "working")
	database, err := Checkout(ctx, session, working, 1, UpdateOptions{Depth: svn.DepthFiles})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if contents, err := os.ReadFile(filepath.Join(working, "root-file")); err != nil || string(contents) != "one\n" {
		t.Fatalf("root file = %q, error = %v", contents, err)
	}
	if _, err := os.Stat(filepath.Join(working, "dir")); !os.IsNotExist(err) {
		t.Fatalf("depth-files directory exists, error = %v", err)
	}
	repository.AddRevision(inmem.Revision{
		Root: inmem.Directory(map[string]*inmem.Node{
			"trunk": inmem.Directory(map[string]*inmem.Node{
				"root-file": inmem.File([]byte("two\n")),
				"added":     inmem.File([]byte("added\n")),
				"dir":       inmem.Directory(map[string]*inmem.Node{"child": inmem.File([]byte("child two\n"))}),
			}),
		}),
	})
	if kind, err := session.CheckPath(ctx, "added", 1); err != nil || kind != svn.NodeNone {
		t.Fatalf("added at r1 = %v, error = %v", kind, err)
	}
	if info, err := database.Info(ctx, working); err != nil || info.Revision != 1 {
		t.Fatalf("working root before update = %#v, error = %v", info, err)
	}
	if _, err := database.Update(ctx, session, working, 2, UpdateOptions{Depth: svn.DepthFiles}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"root-file": "two\n", "added": "added\n"} {
		contents, err := os.ReadFile(filepath.Join(working, name))
		if err != nil || string(contents) != want {
			t.Fatalf("%s = %q, error = %v", name, contents, err)
		}
	}
	if _, err := os.Stat(filepath.Join(working, "dir")); !os.IsNotExist(err) {
		t.Fatalf("updated depth-files directory exists, error = %v", err)
	}
}

func TestInMemoryCheckoutDepthMatrix(t *testing.T) {
	ctx := context.Background()
	repository := inmem.NewRepository("memory://wc-depths", "wc-depths-uuid")
	repository.AddRevision(inmem.Revision{
		Root: inmem.Directory(map[string]*inmem.Node{
			"trunk": inmem.Directory(map[string]*inmem.Node{
				"root-file": inmem.File([]byte("root\n")),
				"dir":       inmem.Directory(map[string]*inmem.Node{"child": inmem.File([]byte("child\n"))}),
			}),
		}),
	})
	tests := []struct {
		name               string
		depth              svn.Depth
		rootFile, dir, kid bool
	}{
		{name: "empty", depth: svn.DepthEmpty},
		{name: "files", depth: svn.DepthFiles, rootFile: true},
		{name: "immediates", depth: svn.DepthImmediates, rootFile: true, dir: true},
		{name: "infinity", depth: svn.DepthInfinity, rootFile: true, dir: true, kid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, err := repository.Open("memory://wc-depths/trunk")
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			working := filepath.Join(t.TempDir(), "working")
			database, err := Checkout(ctx, session, working, 1, UpdateOptions{Depth: test.depth})
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			for name, want := range map[string]bool{
				"root-file": test.rootFile,
				"dir":       test.dir,
				"dir/child": test.kid,
			} {
				_, err := os.Lstat(filepath.Join(working, filepath.FromSlash(name)))
				if exists := err == nil; exists != want || err != nil && !os.IsNotExist(err) {
					t.Fatalf("%s exists = %t, error = %v, want %t", name, exists, err, want)
				}
			}
			info, err := database.Info(ctx, working)
			if err != nil || info.Depth != test.depth || info.Revision != 1 {
				t.Fatalf("root info = %#v, error = %v", info, err)
			}
		})
	}
}

func TestInMemoryUpdateDepthMatrix(t *testing.T) {
	ctx := context.Background()
	repository := inmem.NewRepository("memory://wc-update-depths", "wc-update-depths-uuid")
	repository.AddRevision(inmem.Revision{Root: inmem.Directory(map[string]*inmem.Node{
		"trunk": inmem.Directory(map[string]*inmem.Node{
			"root-file":    inmem.File([]byte("root one\n")),
			"root-deleted": inmem.File([]byte("delete root\n")),
			"dir": inmem.Directory(map[string]*inmem.Node{
				"child":         inmem.File([]byte("child one\n")),
				"child-deleted": inmem.File([]byte("delete child\n")),
			}),
		}),
	})})
	repository.AddRevision(inmem.Revision{Root: inmem.Directory(map[string]*inmem.Node{
		"trunk": inmem.Directory(map[string]*inmem.Node{
			"root-file":  inmem.File([]byte("root two\n")),
			"root-added": inmem.File([]byte("add root\n")),
			"dir": inmem.Directory(map[string]*inmem.Node{
				"child":       inmem.File([]byte("child two\n")),
				"child-added": inmem.File([]byte("add child\n")),
			}),
		}),
	})})
	tests := []struct {
		name                       string
		depth                      svn.Depth
		updateRoot, updateChildren bool
	}{
		{name: "empty", depth: svn.DepthEmpty},
		{name: "files", depth: svn.DepthFiles, updateRoot: true},
		{name: "immediates", depth: svn.DepthImmediates, updateRoot: true},
		{name: "infinity", depth: svn.DepthInfinity, updateRoot: true, updateChildren: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, err := repository.Open("memory://wc-update-depths/trunk")
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
			if _, err := database.Update(ctx, session, working, 2, UpdateOptions{Depth: test.depth}); err != nil {
				t.Fatal(err)
			}
			files := []struct {
				name, before, after string
				updated             bool
			}{
				{name: "root-file", before: "root one\n", after: "root two\n", updated: test.updateRoot},
				{name: "root-added", after: "add root\n", updated: test.updateRoot},
				{name: "root-deleted", before: "delete root\n", updated: test.updateRoot},
				{name: "dir/child", before: "child one\n", after: "child two\n", updated: test.updateChildren},
				{name: "dir/child-added", after: "add child\n", updated: test.updateChildren},
				{name: "dir/child-deleted", before: "delete child\n", updated: test.updateChildren},
			}
			for _, file := range files {
				want := file.before
				if file.updated {
					want = file.after
				}
				contents, err := os.ReadFile(filepath.Join(working, filepath.FromSlash(file.name)))
				if want == "" {
					if !os.IsNotExist(err) {
						t.Fatalf("%s exists with %q, error = %v", file.name, contents, err)
					}
				} else if err != nil || string(contents) != want {
					t.Fatalf("%s = %q, error = %v, want %q", file.name, contents, err, want)
				}
			}
		})
	}
}

func TestInMemorySwitch(t *testing.T) {
	ctx := context.Background()
	repository := inmem.NewRepository("memory://wc-switch", "wc-switch-uuid")
	repository.AddRevision(inmem.Revision{
		Root: inmem.Directory(map[string]*inmem.Node{
			"trunk": inmem.Directory(map[string]*inmem.Node{
				"switched":    inmem.Directory(map[string]*inmem.Node{"file": inmem.File([]byte("trunk\n"))}),
				"target-file": inmem.File([]byte("trunk file\n")),
			}),
			"branches": inmem.Directory(map[string]*inmem.Node{
				"other":       inmem.Directory(map[string]*inmem.Node{"file": inmem.File([]byte("branch\n"))}),
				"source-file": inmem.File([]byte("branch file\n")),
			}),
		}),
	})
	session, err := repository.Open("memory://wc-switch/trunk")
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
	target := filepath.Join(working, "switched")
	if _, err := database.Switch(ctx, session, target, "memory://wc-switch/branches/other", 1, UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(filepath.Join(target, "file")); err != nil || string(contents) != "branch\n" {
		t.Fatalf("switched content = %q, error = %v", contents, err)
	}
	info, err := database.Info(ctx, target)
	if err != nil || info.RepositoryPath != "branches/other" {
		t.Fatalf("switched info = %#v, error = %v", info, err)
	}
	fileTarget := filepath.Join(working, "target-file")
	if _, err := database.Switch(ctx, session, fileTarget, "memory://wc-switch/branches/source-file", 1, UpdateOptions{Depth: svn.DepthEmpty}); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(fileTarget); err != nil || string(contents) != "branch file\n" {
		t.Fatalf("switched file content = %q, error = %v", contents, err)
	}
	info, err = database.Info(ctx, fileTarget)
	if err != nil || info.RepositoryPath != "branches/source-file" {
		t.Fatalf("switched file info = %#v, error = %v", info, err)
	}
	repository.AddRevision(inmem.Revision{
		Root: inmem.Directory(map[string]*inmem.Node{
			"trunk": inmem.Directory(map[string]*inmem.Node{
				"switched":    inmem.Directory(map[string]*inmem.Node{"file": inmem.File([]byte("trunk two\n"))}),
				"target-file": inmem.File([]byte("trunk file two\n")),
			}),
			"branches": inmem.Directory(map[string]*inmem.Node{
				"other":       inmem.Directory(map[string]*inmem.Node{"file": inmem.File([]byte("branch two\n"))}),
				"source-file": inmem.File([]byte("branch file two\n")),
			}),
		}),
	})
	if _, err := database.Update(ctx, session, target, 2, UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Update(ctx, session, fileTarget, 2, UpdateOptions{Depth: svn.DepthEmpty}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"switched/file": "branch two\n",
		"target-file":   "branch file two\n",
	} {
		contents, err := os.ReadFile(filepath.Join(working, filepath.FromSlash(name)))
		if err != nil || string(contents) != want {
			t.Fatalf("updated switched %s = %q, error = %v", name, contents, err)
		}
	}
}

func TestLargeCheckoutAndAlternatingUpdates(t *testing.T) {
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
	for index := 0; index < 1000; index++ {
		name := filepath.Join(seed, fmt.Sprintf("file-%04d", index))
		if err := os.WriteFile(name, fmt.Appendf(nil, "initial %d\n", index), 0o644); err != nil {
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
	database, err := Checkout(ctx, session, working, svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if entries, err := os.ReadDir(working); err != nil || len(entries) != 1001 {
		t.Fatalf("checkout entries = %d, error = %v", len(entries), err)
	}
	author := filepath.Join(root, "author")
	if output, err := exec.Command(svnTool, "checkout", "-q", repositoryURL+"/trunk", author).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout author: %v\n%s", err, output)
	}
	for revision := 2; revision <= 21; revision++ {
		name := fmt.Sprintf("file-%04d", revision-2)
		want := fmt.Sprintf("revision %d\n", revision)
		if err := os.WriteFile(filepath.Join(author, name), []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(svnTool, "commit", "-q", "-m", fmt.Sprintf("revision %d", revision), author).CombinedOutput(); err != nil {
			t.Fatalf("svn commit r%d: %v\n%s", revision, err, output)
		}
		if revision%2 == 0 {
			if _, err := database.Update(ctx, session, working, svn.Revnum(revision), UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
				t.Fatalf("go-svn update r%d: %v", revision, err)
			}
		} else if output, err := exec.Command(svnTool, "update", "-q", "-r", fmt.Sprint(revision), working).CombinedOutput(); err != nil {
			t.Fatalf("svn update r%d: %v\n%s", revision, err, output)
		}
		contents, err := os.ReadFile(filepath.Join(working, name))
		if err != nil || string(contents) != want {
			t.Fatalf("working %s at r%d = %q, error = %v", name, revision, contents, err)
		}
		info, err := database.Info(ctx, working)
		if err != nil || info.Revision != svn.Revnum(revision) {
			t.Fatalf("working root at r%d = %#v, error = %v", revision, info, err)
		}
		if output, err := exec.Command(svnTool, "status", "-q", working).CombinedOutput(); err != nil || len(output) != 0 {
			t.Fatalf("svn status at r%d: %v\n%s", revision, err, output)
		}
	}
}

func TestCheckoutAndUpdateReferenceCompatible(t *testing.T) {
	svnadmin := requireTool(t, "svnadmin")
	svnTool := requireTool(t, "svn")
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	importPath := filepath.Join(root, "import")
	if err := os.MkdirAll(filepath.Join(importPath, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(importPath, "README"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(importPath, "dir", "nested"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	repositoryURL := (&url.URL{Scheme: "file", Path: repository}).String()
	if output, err := exec.Command(svnTool, "import", "-q", "-m", "initial", importPath, repositoryURL+"/trunk").CombinedOutput(); err != nil {
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
	if contents, err := os.ReadFile(filepath.Join(working, "README")); err != nil || string(contents) != "first\n" {
		t.Fatalf("README = %q, error = %v", contents, err)
	}
	if output, err := exec.Command(svnTool, "status", working).CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("svn status: %v\n%s", err, output)
	}

	reference := filepath.Join(root, "reference")
	if output, err := exec.Command(svnTool, "checkout", "-q", repositoryURL+"/trunk", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(reference, "README"), []byte("second\ncommon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "commit", "-q", "-m", "update", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn commit: %v\n%s", err, output)
	}
	if _, err := database.Update(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(filepath.Join(working, "README")); err != nil || string(contents) != "second\ncommon\n" {
		t.Fatalf("updated README = %q, error = %v", contents, err)
	}
	if output, err := exec.Command(svnTool, "status", working).CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("updated svn status: %v\n%s", err, output)
	}

	if err := os.WriteFile(filepath.Join(working, "README"), []byte("local\ncommon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reference, "README"), []byte("second\nremote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "commit", "-q", "-m", "disjoint", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn commit disjoint: %v\n%s", err, output)
	}
	if _, err := database.Update(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(filepath.Join(working, "README")); err != nil || string(contents) != "local\nremote\n" {
		t.Fatalf("merged README = %q, error = %v", contents, err)
	}

	if output, err := exec.Command(svnTool, "update", "-q", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn update reference: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(working, "README"), []byte("mine\nremote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reference, "README"), []byte("theirs\nremote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "commit", "-q", "-m", "conflict", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn commit conflict: %v\n%s", err, output)
	}
	if _, err := database.Update(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(svnTool, "status", working).CombinedOutput()
	if err != nil || len(output) == 0 || output[0] != 'C' {
		t.Fatalf("conflicted svn status: %v\n%s", err, output)
	}
	for _, suffix := range []string{".mine", ".r3", ".r4"} {
		if _, err := os.Stat(filepath.Join(working, "README"+suffix)); err != nil {
			t.Fatalf("missing conflict marker %s: %v", suffix, err)
		}
	}
}

func TestSwitchPersistsRepositoryPath(t *testing.T) {
	svnadmin := requireTool(t, "svnadmin")
	svnTool := requireTool(t, "svn")
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	repositoryURL := (&url.URL{Scheme: "file", Path: repository}).String()
	for _, branch := range []string{"trunk", "branch"} {
		seed := filepath.Join(root, branch)
		if err := os.Mkdir(seed, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(seed, "name"), []byte(branch+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command(svnTool, "import", "-q", "-m", branch, seed, repositoryURL+"/"+branch).CombinedOutput(); err != nil {
			t.Fatalf("svn import %s: %v\n%s", branch, err, output)
		}
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
	if _, err := database.Switch(context.Background(), session, working, repositoryURL+"/branch", svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(filepath.Join(working, "name")); err != nil || string(contents) != "branch\n" {
		t.Fatalf("switched contents = %q, error = %v", contents, err)
	}
	output, err := exec.Command(svnTool, "info", "--show-item", "url", working).CombinedOutput()
	if err != nil || string(output) != repositoryURL+"/branch\n" {
		t.Fatalf("switched URL: %v\n%s", err, output)
	}
}

func TestUpdatePropertyConflictReferenceCompatible(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(seed, "file"), []byte("content\n"), 0o644); err != nil {
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
	target := filepath.Join(working, "file")
	if err := database.SetProperty(context.Background(), target, "custom", []byte("mine"), false); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(root, "reference")
	if output, err := exec.Command(svnTool, "checkout", "-q", repositoryURL+"/trunk", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "propset", "custom", "theirs", filepath.Join(reference, "file")).CombinedOutput(); err != nil {
		t.Fatalf("svn propset: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "commit", "-q", "-m", "property", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn commit: %v\n%s", err, output)
	}
	if _, err := database.Update(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(svnTool, "status", target).CombinedOutput()
	if err != nil || len(output) < 2 || output[1] != 'C' {
		t.Fatalf("property conflict status: %v\n%s", err, output)
	}
	info, err := database.Info(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if string(info.BaseProperties["custom"]) != "theirs" || string(info.WorkingProperties["custom"]) != "mine" || info.Conflict == nil || !info.Conflict.Property {
		t.Fatalf("property conflict info = %#v", info)
	}
	if _, err := os.Stat(filepath.Join(working, "file.prej")); err != nil {
		t.Fatal(err)
	}
	if err := database.Resolve(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "status", target).CombinedOutput(); err != nil || len(output) < 2 || output[1] != 'M' {
		t.Fatalf("resolved property status: %v\n%s", err, output)
	}
}

func TestUpdateTreeConflictReferenceCompatible(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(seed, "file"), []byte("base\n"), 0o644); err != nil {
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
	target := filepath.Join(working, "file")
	if err := database.Delete(context.Background(), target, DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(root, "reference")
	if output, err := exec.Command(svnTool, "checkout", "-q", repositoryURL+"/trunk", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(reference, "file"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "commit", "-q", "-m", "remote edit", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn commit: %v\n%s", err, output)
	}
	if _, err := database.Update(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(svnTool, "status", target).CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("D     C")) {
		var conflictData []byte
		_ = database.sql.QueryRow(`SELECT conflict_data FROM ACTUAL_NODE WHERE wc_id=? AND local_relpath='file'`, database.wcID).Scan(&conflictData)
		current, _ := database.Info(context.Background(), target)
		t.Fatalf("tree conflict status: %v\n%s\nskel=%s\ninfo=%#v", err, output, conflictData, current)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("locally deleted file was recreated: %v", err)
	}
	info, err := database.Info(context.Background(), target)
	if err != nil || info.Conflict == nil || !info.Conflict.Tree {
		t.Fatalf("tree conflict info = %#v, error = %v", info, err)
	}
	if info.Conflict.TreeReason != "deleted" || info.Conflict.TreeAction != "edited" {
		t.Fatalf("tree conflict details = %#v", info.Conflict)
	}
	if err := database.ResolveWithChoice(context.Background(), target, ConflictTheirs); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "remote\n" {
		t.Fatalf("resolved incoming contents = %q, error = %v", contents, err)
	}
	if output, err := exec.Command(svnTool, "status", target).CombinedOutput(); err != nil || len(output) != 0 {
		t.Fatalf("resolved tree status: %v\n%s", err, output)
	}
}

func TestUpdateIncomingDeletePreservesLocalEdit(t *testing.T) {
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
	if err := os.Mkdir(filepath.Join(seed, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(seed, "clean-directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "file"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "directory", "child"), []byte("base child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "clean-directory", "child"), []byte("clean child\n"), 0o644); err != nil {
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
	target := filepath.Join(working, "file")
	if err := os.WriteFile(target, []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	directoryTarget := filepath.Join(working, "directory")
	childTarget := filepath.Join(directoryTarget, "child")
	if err := os.WriteFile(childTarget, []byte("local child\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collisionTarget := filepath.Join(working, "collision")
	if err := os.WriteFile(collisionTarget, []byte("local add\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := database.Add(context.Background(), collisionTarget, AddOptions{}); err != nil {
		t.Fatal(err)
	}
	directoryCollisionTarget := filepath.Join(working, "directory-collision")
	if err := database.Mkdir(context.Background(), directoryCollisionTarget, false); err != nil {
		t.Fatal(err)
	}
	obstructionTarget := filepath.Join(working, "obstruction")
	if err := os.WriteFile(obstructionTarget, []byte("local obstruction\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	directoryObstructionTarget := filepath.Join(working, "directory-obstruction")
	if err := os.Mkdir(directoryObstructionTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directoryObstructionTarget, "local"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(root, "reference")
	if output, err := exec.Command(svnTool, "checkout", "-q", repositoryURL+"/trunk", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "delete", "-q", filepath.Join(reference, "file")).CombinedOutput(); err != nil {
		t.Fatalf("svn delete: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "delete", "-q", filepath.Join(reference, "directory")).CombinedOutput(); err != nil {
		t.Fatalf("svn delete directory: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "delete", "-q", filepath.Join(reference, "clean-directory")).CombinedOutput(); err != nil {
		t.Fatalf("svn delete clean directory: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(reference, "collision"), []byte("remote add\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "add", "-q", filepath.Join(reference, "collision")).CombinedOutput(); err != nil {
		t.Fatalf("svn add collision: %v\n%s", err, output)
	}
	if err := os.Mkdir(filepath.Join(reference, "directory-collision"), 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "add", "-q", filepath.Join(reference, "directory-collision")).CombinedOutput(); err != nil {
		t.Fatalf("svn add directory collision: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(reference, "obstruction"), []byte("remote obstruction\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "add", "-q", filepath.Join(reference, "obstruction")).CombinedOutput(); err != nil {
		t.Fatalf("svn add obstruction: %v\n%s", err, output)
	}
	if err := os.Mkdir(filepath.Join(reference, "directory-obstruction"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reference, "directory-obstruction", "remote"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "add", "-q", filepath.Join(reference, "directory-obstruction")).CombinedOutput(); err != nil {
		t.Fatalf("svn add directory obstruction: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "commit", "-q", "-m", "delete", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn commit: %v\n%s", err, output)
	}
	if _, err := database.Update(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "local\n" {
		t.Fatalf("preserved contents = %q, error = %v", contents, err)
	}
	output, err := exec.Command(svnTool, "status", target).CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("A  +  C")) {
		t.Fatalf("tree conflict status: %v\n%s", err, output)
	}
	info, err := database.Info(context.Background(), target)
	if err != nil || info.Conflict == nil || !info.Conflict.Tree || !info.Copied || info.Schedule != ScheduleAdd {
		t.Fatalf("tree conflict info = %#v, error = %v", info, err)
	}
	contents, err = os.ReadFile(childTarget)
	if err != nil || string(contents) != "local child\n" {
		t.Fatalf("preserved child contents = %q, error = %v", contents, err)
	}
	output, err = exec.Command(svnTool, "status", directoryTarget).CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("A  +  C")) || !bytes.Contains(output, []byte("M  +")) {
		t.Fatalf("directory tree conflict status: %v\n%s", err, output)
	}
	var baseDescendants int
	if err := database.sql.QueryRow(`SELECT COUNT(*) FROM NODES WHERE wc_id=? AND local_relpath LIKE 'directory/%' AND op_depth=0`, database.wcID).Scan(&baseDescendants); err != nil {
		t.Fatal(err)
	}
	if baseDescendants != 0 {
		t.Fatalf("orphaned BASE descendants = %d", baseDescendants)
	}
	if _, err := os.Stat(filepath.Join(working, "clean-directory")); !os.IsNotExist(err) {
		t.Fatalf("clean deleted directory remains: %v", err)
	}
	var cleanRows int
	if err := database.sql.QueryRow(`SELECT COUNT(*) FROM NODES WHERE wc_id=? AND (local_relpath='clean-directory' OR local_relpath LIKE 'clean-directory/%')`, database.wcID).Scan(&cleanRows); err != nil {
		t.Fatal(err)
	}
	if cleanRows != 0 {
		t.Fatalf("clean deleted directory metadata rows = %d", cleanRows)
	}
	contents, err = os.ReadFile(collisionTarget)
	if err != nil || string(contents) != "local add\n" {
		t.Fatalf("colliding add contents = %q, error = %v", contents, err)
	}
	output, err = exec.Command(svnTool, "status", collisionTarget).CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("R     C")) {
		t.Fatalf("add collision status: %v\n%s", err, output)
	}
	output, err = exec.Command(svnTool, "status", directoryCollisionTarget).CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("R     C")) {
		t.Fatalf("directory add collision status: %v\n%s", err, output)
	}
	contents, err = os.ReadFile(obstructionTarget)
	if err != nil || string(contents) != "local obstruction\n" {
		t.Fatalf("obstruction contents = %q, error = %v", contents, err)
	}
	output, err = exec.Command(svnTool, "status", obstructionTarget).CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("D     C")) {
		t.Fatalf("obstruction status: %v\n%s", err, output)
	}
	output, err = exec.Command(svnTool, "status", directoryObstructionTarget).CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("D     C")) {
		t.Fatalf("directory obstruction status: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(directoryObstructionTarget, "local")); err != nil {
		t.Fatalf("local obstruction child: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directoryObstructionTarget, "remote")); !os.IsNotExist(err) {
		t.Fatalf("suppressed remote obstruction child: %v", err)
	}
	forcedWorking := filepath.Join(root, "forced-working")
	forcedDatabase, err := Checkout(context.Background(), session, forcedWorking, svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity})
	if err != nil {
		t.Fatal(err)
	}
	defer forcedDatabase.Close()
	forcedTarget := filepath.Join(forcedWorking, "forced-obstruction")
	if err := os.WriteFile(forcedTarget, []byte("forced local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	forcedDirectoryTarget := filepath.Join(forcedWorking, "forced-directory-obstruction")
	if err := os.Mkdir(forcedDirectoryTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(forcedDirectoryTarget, "local"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reference, "forced-obstruction"), []byte("forced remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "add", "-q", filepath.Join(reference, "forced-obstruction")).CombinedOutput(); err != nil {
		t.Fatalf("svn add forced obstruction: %v\n%s", err, output)
	}
	if err := os.Mkdir(filepath.Join(reference, "forced-directory-obstruction"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reference, "forced-directory-obstruction", "remote"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(svnTool, "add", "-q", filepath.Join(reference, "forced-directory-obstruction")).CombinedOutput(); err != nil {
		t.Fatalf("svn add forced directory obstruction: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "commit", "-q", "-m", "forced obstruction", reference).CombinedOutput(); err != nil {
		t.Fatalf("svn commit forced obstruction: %v\n%s", err, output)
	}
	if _, err := forcedDatabase.Update(context.Background(), session, forcedWorking, svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity, Force: true}); err != nil {
		t.Fatal(err)
	}
	contents, err = os.ReadFile(forcedTarget)
	if err != nil || string(contents) != "forced local\n" {
		t.Fatalf("forced obstruction contents = %q, error = %v", contents, err)
	}
	output, err = exec.Command(svnTool, "status", forcedTarget).CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("M       ")) || bytes.Contains(output, []byte("C")) {
		t.Fatalf("forced obstruction status: %v\n%s", err, output)
	}
	for _, name := range []string{"local", "remote"} {
		if _, err := os.Stat(filepath.Join(forcedDirectoryTarget, name)); err != nil {
			t.Fatalf("forced directory obstruction child %s: %v", name, err)
		}
	}
	if _, err := database.Update(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: svn.DepthInfinity}); err != nil {
		t.Fatalf("update with unresolved tree conflicts: %v", err)
	}
	output, err = exec.Command(svnTool, "status", collisionTarget).CombinedOutput()
	if err != nil || !bytes.Contains(output, []byte("R     C")) {
		t.Fatalf("preserved add collision status: %v\n%s", err, output)
	}
	for _, conflictedPath := range []string{target, directoryTarget} {
		if err := database.ResolveWithChoice(context.Background(), conflictedPath, ConflictTheirs); err != nil {
			t.Fatalf("resolve incoming deletion for %s: %v", conflictedPath, err)
		}
		if _, err := os.Stat(conflictedPath); !os.IsNotExist(err) {
			t.Fatalf("incoming-deleted path remains %s: %v", conflictedPath, err)
		}
	}
}

func TestCheckoutAndSetDepth(t *testing.T) {
	svnadmin := requireTool(t, "svnadmin")
	svnTool := requireTool(t, "svn")
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(filepath.Join(seed, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name := range map[string]bool{"root-file": true, "directory/child": true} {
		if err := os.WriteFile(filepath.Join(seed, filepath.FromSlash(name)), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repositoryURL := (&url.URL{Scheme: "file", Path: repository}).String()
	if output, err := exec.Command(svnTool, "import", "-q", "-m", "seed", seed, repositoryURL+"/trunk").CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v\n%s", err, output)
	}
	for _, test := range []struct {
		name      string
		depth     svn.Depth
		rootFile  bool
		directory bool
		child     bool
	}{
		{name: "empty", depth: svn.DepthEmpty},
		{name: "files", depth: svn.DepthFiles, rootFile: true},
		{name: "immediates", depth: svn.DepthImmediates, rootFile: true, directory: true},
		{name: "infinity", depth: svn.DepthInfinity, rootFile: true, directory: true, child: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			session, _, err := ra.Open(context.Background(), repositoryURL+"/trunk", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			working := filepath.Join(root, "wc-"+test.name)
			database, err := Checkout(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: test.depth})
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			for name, want := range map[string]bool{"root-file": test.rootFile, "directory": test.directory, "directory/child": test.child} {
				_, err := os.Stat(filepath.Join(working, filepath.FromSlash(name)))
				if (err == nil) != want {
					t.Errorf("%s exists = %v, want %v", name, err == nil, want)
				}
			}
			if test.depth == svn.DepthEmpty {
				depth := svn.DepthInfinity
				if _, err := database.Update(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: depth, SetDepth: &depth}); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(working, "directory", "child")); err != nil {
					t.Fatalf("deepened child: %v", err)
				}
				filesDepth := svn.DepthFiles
				if _, err := database.Update(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: filesDepth, SetDepth: &filesDepth}); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(working, "directory")); !os.IsNotExist(err) {
					t.Fatalf("files-depth directory remains: %v", err)
				}
				if _, err := os.Stat(filepath.Join(working, "root-file")); err != nil {
					t.Fatalf("files-depth root file: %v", err)
				}
				emptyDepth := svn.DepthEmpty
				if _, err := database.Update(context.Background(), session, working, svn.InvalidRevnum, UpdateOptions{Depth: emptyDepth, SetDepth: &emptyDepth}); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(working, "root-file")); !os.IsNotExist(err) {
					t.Fatalf("empty-depth root file remains: %v", err)
				}
			}
		})
	}
}

func TestUpdateEditorUsesLocalCopySource(t *testing.T) {
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
	contents := []byte("copy source\n")
	if err := os.WriteFile(filepath.Join(seed, "source"), contents, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(seed, "source-directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	childContents := []byte("copied child\n")
	if err := os.WriteFile(filepath.Join(seed, "source-directory", "child"), childContents, 0o644); err != nil {
		t.Fatal(err)
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
	editor, err := NewUpdateEditor(ctx, database, working, UpdateOptions{Depth: svn.DepthInfinity})
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.SetTargetRevision(ctx, 1); err != nil {
		t.Fatal(err)
	}
	rootEditor, err := editor.OpenRoot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := rootEditor.DeleteEntry(ctx, "source", 1); err != nil {
		t.Fatal(err)
	}
	if err := rootEditor.DeleteEntry(ctx, "source-directory", 1); err != nil {
		t.Fatal(err)
	}
	file, err := rootEditor.AddFile(ctx, "copy", &delta.CopySource{Path: "/trunk/source", Rev: 1})
	if err != nil {
		t.Fatal(err)
	}
	checksum := svn.Sum(svn.ChecksumMD5, contents)
	if err := file.Close(ctx, &checksum); err != nil {
		t.Fatal(err)
	}
	directory, err := rootEditor.AddDirectory(ctx, "copied-directory", &delta.CopySource{Path: "/trunk/source-directory", Rev: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rootEditor.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := editor.CloseEdit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(working, "source")); !os.IsNotExist(err) {
		t.Fatalf("deleted copy source remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(working, "source-directory")); !os.IsNotExist(err) {
		t.Fatalf("deleted directory copy source remains: %v", err)
	}
	actual, err := os.ReadFile(filepath.Join(working, "copy"))
	if err != nil || !bytes.Equal(actual, contents) {
		t.Fatalf("copied contents = %q, error = %v", actual, err)
	}
	actual, err = os.ReadFile(filepath.Join(working, "copied-directory", "child"))
	if err != nil || !bytes.Equal(actual, childContents) {
		t.Fatalf("copied child contents = %q, error = %v", actual, err)
	}
}

func TestCheckoutRecordsExternalDefinitions(t *testing.T) {
	svnadmin := requireTool(t, "svnadmin")
	svnTool := requireTool(t, "svn")
	ctx := context.Background()
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	if output, err := exec.Command(svnadmin, "create", repository).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	seed := filepath.Join(root, "seed")
	if err := os.MkdirAll(filepath.Join(seed, "main"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(seed, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	repositoryURL := (&url.URL{Scheme: "file", Path: repository}).String()
	if output, err := exec.Command(svnTool, "import", "-q", "-m", "seed", seed, repositoryURL).CombinedOutput(); err != nil {
		t.Fatalf("svn import: %v\n%s", err, output)
	}
	admin := filepath.Join(root, "admin")
	if output, err := exec.Command(svnTool, "checkout", "-q", repositoryURL, admin).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout admin: %v\n%s", err, output)
	}
	definition := "-r 1 ^/lib@2 ext\n"
	if output, err := exec.Command(svnTool, "propset", "--", "svn:externals", definition, filepath.Join(admin, "main")).CombinedOutput(); err != nil {
		t.Fatalf("svn propset externals: %v\n%s", err, output)
	}
	if output, err := exec.Command(svnTool, "commit", "-q", "-m", "external", admin).CombinedOutput(); err != nil {
		t.Fatalf("svn commit external: %v\n%s", err, output)
	}
	session, _, err := ra.Open(ctx, repositoryURL+"/main", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	working := filepath.Join(root, "working")
	database, err := Checkout(ctx, session, working, 2, UpdateOptions{Depth: svn.DepthInfinity})
	if err != nil {
		t.Fatal(err)
	}
	externals, err := database.Externals(ctx)
	database.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(externals) != 1 || externals[0].LocalPath != "ext" || externals[0].ParentPath != "" ||
		externals[0].DefinitionPath != "" || externals[0].DefinitionReposPath != "lib" ||
		externals[0].OperativeRevision != 1 || externals[0].PegRevision != 2 {
		t.Fatalf("externals = %#v", externals)
	}
	ignored := filepath.Join(root, "ignored")
	ignoredDatabase, err := Checkout(ctx, session, ignored, 2, UpdateOptions{Depth: svn.DepthInfinity, IgnoreExternals: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ignoredDatabase.Close()
	externals, err = ignoredDatabase.Externals(ctx)
	if err != nil || len(externals) != 0 {
		t.Fatalf("ignored externals = %#v, error = %v", externals, err)
	}
}
