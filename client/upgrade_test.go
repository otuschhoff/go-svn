package client_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/otuschhoff/go-svn/client"
	"github.com/otuschhoff/go-svn/svn"
	_ "modernc.org/sqlite"
)

func TestUpgradeRejectsPreFormat31WorkingCopy(t *testing.T) {
	root := t.TempDir()
	admin := filepath.Join(root, ".svn")
	if err := mkdirAll(admin); err != nil {
		t.Fatal(err)
	}
	handle, err := sql.Open("sqlite", filepath.Join(admin, "wc.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec("PRAGMA user_version = 30"); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.New(nil).Upgrade(context.Background(), root); !errors.Is(err, svn.ErrWCUpgradeRequired) {
		t.Fatalf("Upgrade error = %v", err)
	}
}

func mkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}
