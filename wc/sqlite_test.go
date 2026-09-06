package wc

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestFormat32StatusWithoutPristineFile(t *testing.T) {
	database := createTestWorkingCopy(t)
	root := database.RootPath()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha1Checksum, md5Checksum, size, err := checksumsForFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.installPristine(context.Background(), file, sha1Checksum, md5Checksum, size); err != nil {
		t.Fatal(err)
	}
	if _, err := database.sql.Exec(`INSERT INTO NODES (wc_id, local_relpath, op_depth, parent_relpath, repos_id, repos_path, revision, presence, kind, properties, checksum, changed_revision)
		VALUES (?, 'file', 0, '', ?, 'trunk/file', 1, 'normal', 'file', X'2829', ?, 1)`, database.wcID, database.repository.ID, sha1Checksum.Serialize()); err != nil {
		t.Fatal(err)
	}
	hexValue := sha1Checksum.Hex()
	if err := os.Remove(filepath.Join(root, ".svn", "pristine", hexValue[:2], hexValue+".svn-base")); err != nil {
		t.Fatal(err)
	}
	if _, err := database.sql.Exec(`DELETE FROM PRISTINE WHERE checksum=?`, sha1Checksum.Serialize()); err != nil {
		t.Fatal(err)
	}
	if _, err := database.sql.Exec(`PRAGMA user_version = 32`); err != nil {
		t.Fatal(err)
	}
	database.Close()
	database, err = Open(context.Background(), root, Options{AllowFormat32: true})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := os.WriteFile(file, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var status *Status
	if err := database.Status(context.Background(), file, StatusOptions{Depth: svn.DepthEmpty, Verbose: true}, func(value *Status) error { status = value; return nil }); err != nil {
		t.Fatal(err)
	}
	if status == nil || status.TextStatus != StatusModified {
		t.Fatalf("format 32 status = %#v", status)
	}
}

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

func TestOpenDatabaseFormat32IsExplicitlyReadOnly(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "wc.db")
	handle, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Exec("PRAGMA user_version = 32"); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := OpenDatabase(context.Background(), databasePath, Options{AllowFormat32: true})
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	if database, err := OpenDatabase(context.Background(), databasePath, Options{AllowFormat32: true, Writable: true}); !errors.Is(err, svn.ErrWCUnsupportedFormat) {
		if database != nil {
			database.Close()
		}
		t.Fatalf("writable format 32 error = %v", err)
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
