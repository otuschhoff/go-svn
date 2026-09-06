package wc

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestOpenDatabaseFormat(t *testing.T) {
	for _, test := range []struct {
		format int
		want   error
	}{{30, svn.ErrWCUpgradeRequired}, {31, nil}, {32, svn.ErrWCUnsupportedFormat}} {
		databasePath := filepath.Join(t.TempDir(), "wc.db")
		handle, err := sql.Open("sqlite", databasePath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := handle.Exec("PRAGMA user_version = " + strconv.Itoa(test.format)); err != nil {
			t.Fatal(err)
		}
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
		database, err := OpenDatabase(context.Background(), databasePath, Options{})
		if !errors.Is(err, test.want) {
			t.Fatalf("format %d error = %v, want %v", test.format, err, test.want)
		}
		if database != nil {
			if database.Format() != test.format {
				t.Fatalf("format = %d, want %d", database.Format(), test.format)
			}
			database.Close()
		}
	}
}

func TestOpenReferenceWorkingCopy(t *testing.T) {
	svn, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn is not installed")
	}
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	repositoryPath := filepath.Join(t.TempDir(), "repository")
	workingPath := filepath.Join(t.TempDir(), "working")
	if output, err := exec.Command(svnadmin, "create", repositoryPath).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	if output, err := exec.Command(svn, "checkout", "--quiet", "file://"+repositoryPath, workingPath).CombinedOutput(); err != nil {
		t.Fatalf("svn checkout: %v\n%s", err, output)
	}
	database, err := OpenDatabase(context.Background(), filepath.Join(workingPath, ".svn", "wc.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if database.Format() != CurrentFormat {
		t.Fatalf("format = %d, want %d", database.Format(), CurrentFormat)
	}
	for name, want := range map[string]string{"foreign_keys": "0", "locking_mode": "normal", "journal_mode": "delete", "synchronous": "1", "temp_store": "2", "busy_timeout": "10000", "query_only": "1"} {
		var got string
		if err := database.sql.QueryRow("PRAGMA " + name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %q, want %q", name, got, want)
		}
	}
	var caseInsensitive int
	if err := database.sql.QueryRow("SELECT 'a' LIKE 'A'").Scan(&caseInsensitive); err != nil {
		t.Fatal(err)
	}
	if caseInsensitive != 0 {
		t.Fatal("case_sensitive_like pragma was not applied")
	}
}
