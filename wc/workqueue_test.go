package wc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/skel"
)

func TestWorkItemReferenceGrammar(t *testing.T) {
	tests := []struct {
		item *skel.Node
		want string
	}{
		{FileInstallWork("path", true, false, "source"), "(file-install path 1 1 1 0 source)"},
		{FileRemoveWork("path"), "(file-remove path)"},
		{DirectoryRemoveWork("path", false), "(dir-remove path)"},
		{DirectoryRemoveWork("path", true), "(dir-remove path 1 1)"},
		{FileMoveWork("source", "target"), "(file-move source target)"},
		{DirectoryInstallWork("path"), "(dir-install path)"},
		{SyncFileFlagsWork("path"), "(sync-file-flags path)"},
		{PropertyRejectInstallWork("path"), "(prej-install path)"},
		{RecordFileInfoWork("path"), "(record-fileinfo path)"},
		{PostUpgradeWork(), "(postupgrade)"},
	}
	for _, test := range tests {
		data, err := test.item.MarshalBinary()
		if err != nil || string(data) != test.want {
			t.Errorf("work item = %q, error = %v, want %q", data, err, test.want)
		}
	}
}

func TestWorkQueueReplayAndIdempotence(t *testing.T) {
	database := createTestWorkingCopy(t)
	defer database.Close()
	root := database.RootPath()
	if err := os.WriteFile(filepath.Join(root, "source"), []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	items := []*skel.Node{
		WorkItem(workDirInstall, "dir"),
		WorkItem(workFileMove, "source", "dir/moved"),
		WorkItem(workFileRemove, "missing"),
		WorkItem(workDirRemove, "missing-dir", "1"),
		WorkItem(workPostUpgrade),
	}
	for _, item := range items {
		if _, err := database.Enqueue(context.Background(), item); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.RunWorkQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(filepath.Join(root, "dir", "moved")); err != nil || string(contents) != "source" {
		t.Fatalf("moved = %q, error = %v", contents, err)
	}
	if err := database.RunWorkQueue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := database.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkQueueFailureRetained(t *testing.T) {
	database := createTestWorkingCopy(t)
	defer database.Close()
	if _, err := database.Enqueue(context.Background(), WorkItem("unknown-operation")); err != nil {
		t.Fatal(err)
	}
	if err := database.RunWorkQueue(context.Background()); !errors.Is(err, svn.ErrWCBadAdmLog) {
		t.Fatalf("RunWorkQueue error = %v", err)
	}
	var count int
	if err := database.sql.QueryRow(`SELECT COUNT(*) FROM WORK_QUEUE`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("queued count = %d, error = %v", count, err)
	}
}

func TestWorkQueueCrashResumeAfterReopen(t *testing.T) {
	database := createTestWorkingCopy(t)
	root := database.RootPath()
	bundle := skel.NewList(DirectoryInstallWork("directory"), FileRemoveWork("already-missing"))
	if _, err := database.Enqueue(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	if err := database.runWorkItem(context.Background(), DirectoryInstallWork("directory")); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(context.Background(), root, Options{Writable: true})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stat, err := os.Stat(filepath.Join(root, "directory")); err != nil || !stat.IsDir() {
		t.Fatalf("resumed directory: %v, error = %v", stat, err)
	}
	var count int
	if err := database.sql.QueryRow(`SELECT COUNT(*) FROM WORK_QUEUE`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("queued count = %d, error = %v", count, err)
	}
}

func createTestWorkingCopy(t *testing.T) *Database {
	t.Helper()
	database, err := Create(context.Background(), filepath.Join(t.TempDir(), "wc"), CreateOptions{
		RepositoryRoot: "file:///tmp/repository", RepositoryUUID: "00000000-0000-0000-0000-000000000001",
		RepositoryPath: "trunk", Revision: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func TestResolveWorkPathRejectsSymlinkAncestor(t *testing.T) {
	database := createTestWorkingCopy(t)
	defer database.Close()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "victim"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(database.RootPath(), "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := database.resolveWorkPath("link/victim"); err == nil {
		t.Fatal("symlink ancestor accepted")
	}
	if _, err := database.Enqueue(context.Background(), DirectoryRemoveWork("link/victim", true)); err != nil {
		t.Fatal(err)
	}
	if err := database.RunWorkQueue(context.Background()); err == nil {
		t.Fatal("unsafe work item succeeded")
	}
	if contents, err := os.ReadFile(filepath.Join(outside, "victim")); err != nil || string(contents) != "outside" {
		t.Fatalf("outside file=%q error=%v", contents, err)
	}
}
