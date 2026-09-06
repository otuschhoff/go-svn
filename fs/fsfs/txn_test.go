package fsfs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/svn"
)

func TestTransactionLifecycleWithSVNAdmin(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	repositoryPath := filepath.Join(t.TempDir(), "repository")
	if output, err := exec.Command(svnadmin, "create", repositoryPath).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	filesystem, err := Open(context.Background(), repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	transactionValue, err := filesystem.BeginTxn(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	transaction := transactionValue.(*Transaction)
	output, err := exec.Command(svnadmin, "lstxns", repositoryPath).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != transaction.Name() {
		t.Fatalf("svnadmin lstxns: %v, output %q", err, output)
	}
	if err := transaction.ChangeProperty(context.Background(), "svn:log", []byte("transaction lifecycle")); err != nil {
		t.Fatal(err)
	}
	properties, err := transaction.Properties(context.Background())
	if err != nil || string(properties["svn:log"]) != "transaction lifecycle" || len(properties["svn:date"]) == 0 {
		t.Fatalf("properties = %v, error = %v", properties, err)
	}
	if err := transaction.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	output, err = exec.Command(svnadmin, "lstxns", repositoryPath).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "" {
		t.Fatalf("svnadmin lstxns after abort: %v, output %q", err, output)
	}
	for _, name := range []string{transaction.directory(), transaction.protorevPath(), transaction.protorevPath() + "-lock"} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("transaction artifact remains: %s", name)
		}
	}
}

func TestLiveTransactionWithSVNLook(t *testing.T) {
	svnlook, err := exec.LookPath("svnlook")
	if err != nil {
		t.Skip("svnlook is not installed")
	}
	for _, format := range []int{6, 8} {
		t.Run(strconv.Itoa(format), func(t *testing.T) {
			repositoryPath := filepath.Join(t.TempDir(), "repository")
			filesystem, err := Create(context.Background(), repositoryPath, CreateOptions{Format: format})
			if err != nil {
				t.Fatal(err)
			}
			transactionValue, err := filesystem.BeginTxn(context.Background(), 0)
			if err != nil {
				t.Fatal(err)
			}
			transaction := transactionValue.(*Transaction)
			rootValue, err := transaction.Root(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			root := rootValue.(*TxnRoot)
			if err := root.MakeDir(context.Background(), "/project"); err != nil {
				t.Fatal(err)
			}
			if err := root.MakeFile(context.Background(), "/project/readme"); err != nil {
				t.Fatal(err)
			}
			content := []byte("live transaction\n")
			if err := root.ApplyText(context.Background(), "/project/readme", bytes.NewReader(content)); err != nil {
				t.Fatal(err)
			}
			if output, err := exec.Command(svnlook, "tree", "-t", transaction.Name(), repositoryPath).CombinedOutput(); err != nil || !strings.Contains(string(output), "readme") {
				t.Fatalf("svnlook tree: %v\n%s", err, output)
			}
			if output, err := exec.Command(svnlook, "cat", "-t", transaction.Name(), repositoryPath, "project/readme").CombinedOutput(); err != nil || !bytes.Equal(output, content) {
				t.Fatalf("svnlook cat: %v, content %q", err, output)
			}
		})
	}
}

func TestConcurrentTransactionMergeAndConflict(t *testing.T) {
	repositoryPath := filepath.Join(t.TempDir(), "repository")
	filesystem, err := Create(context.Background(), repositoryPath, CreateOptions{Format: 8})
	if err != nil {
		t.Fatal(err)
	}
	beginWithFile := func(name string, base svn.Revnum) *Transaction {
		transactionValue, err := filesystem.BeginTxn(context.Background(), base)
		if err != nil {
			t.Fatal(err)
		}
		transaction := transactionValue.(*Transaction)
		rootValue, err := transaction.Root(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := rootValue.MakeFile(context.Background(), name); err != nil {
			t.Fatal(err)
		}
		return transaction
	}
	left := beginWithFile("/left", 0)
	right := beginWithFile("/right", 0)
	if _, err := left.Commit(context.Background(), nil, false); err != nil {
		t.Fatal(err)
	}
	if revision, err := right.Commit(context.Background(), nil, false); err != nil || revision != 2 {
		t.Fatalf("merged commit = r%d, error = %v", revision, err)
	}
	root, err := filesystem.RevisionRoot(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"/left", "/right"} {
		if kind, err := root.CheckPath(context.Background(), name); err != nil || kind != svn.NodeFile {
			t.Fatalf("%s kind = %s, error = %v", name, kind, err)
		}
	}
	first := beginWithFile("/same", 2)
	second := beginWithFile("/same", 2)
	if _, err := first.Commit(context.Background(), nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Commit(context.Background(), nil, false); !errors.Is(err, svn.ErrFSConflict) {
		t.Fatalf("overlapping commit error = %v", err)
	}
	if err := second.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	seed := beginWithFile("/orthogonal", 3)
	seedRoot, _ := seed.Root(context.Background())
	if err := seedRoot.ApplyText(context.Background(), "/orthogonal", bytes.NewBufferString("base\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Commit(context.Background(), nil, false); err != nil {
		t.Fatal(err)
	}
	textTxnValue, _ := filesystem.BeginTxn(context.Background(), 4)
	propTxnValue, _ := filesystem.BeginTxn(context.Background(), 4)
	textTxn, propTxn := textTxnValue.(*Transaction), propTxnValue.(*Transaction)
	textRoot, _ := textTxn.Root(context.Background())
	propRoot, _ := propTxn.Root(context.Background())
	if err := textRoot.ApplyText(context.Background(), "/orthogonal", bytes.NewBufferString("changed\n")); err != nil {
		t.Fatal(err)
	}
	if err := propRoot.ChangeNodeProp(context.Background(), "/orthogonal", "custom", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if _, err := propTxn.Commit(context.Background(), nil, false); err != nil {
		t.Fatal(err)
	}
	if _, err := textTxn.Commit(context.Background(), nil, false); err != nil {
		t.Fatalf("orthogonal commit: %v", err)
	}
	mergedRoot, err := filesystem.RevisionRoot(context.Background(), 6)
	if err != nil {
		t.Fatal(err)
	}
	properties, err := mergedRoot.NodeProps(context.Background(), "/orthogonal")
	if err != nil || string(properties["custom"]) != "value" {
		t.Fatalf("merged properties = %v, error = %v", properties, err)
	}
	var content bytes.Buffer
	if err := mergedRoot.FileContents(context.Background(), "/orthogonal", &content); err != nil || content.String() != "changed\n" {
		t.Fatalf("merged content = %q, error = %v", content.String(), err)
	}
}

func TestCommitLogicalRevisionWithSVNAdmin(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	repositoryPath := filepath.Join(t.TempDir(), "repository")
	if output, err := exec.Command(svnadmin, "create", repositoryPath).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	filesystem, err := Open(context.Background(), repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	transactionValue, err := filesystem.BeginTxn(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	transaction := transactionValue.(*Transaction)
	if err := transaction.ChangeProperty(context.Background(), "svn:log", []byte("go-svn logical commit")); err != nil {
		t.Fatal(err)
	}
	rootValue, err := transaction.Root(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := rootValue.(*TxnRoot)
	if err := root.MakeDir(context.Background(), "/project"); err != nil {
		t.Fatal(err)
	}
	if err := root.MakeFile(context.Background(), "/project/hello.txt"); err != nil {
		t.Fatal(err)
	}
	content := []byte("hello from go-svn\n")
	if err := root.ApplyText(context.Background(), "/project/hello.txt", bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if err := root.ChangeNodeProp(context.Background(), "/project/hello.txt", "custom:property", []byte("value")); err != nil {
		t.Fatal(err)
	}
	revision, err := transaction.Commit(context.Background(), nil, false)
	if err != nil || revision != 1 {
		t.Fatalf("commit revision = %d, error = %v", revision, err)
	}
	revisionRoot, err := filesystem.RevisionRoot(context.Background(), revision)
	if err != nil {
		t.Fatal(err)
	}
	var actual bytes.Buffer
	if err := revisionRoot.FileContents(context.Background(), "/project/hello.txt", &actual); err != nil || !bytes.Equal(actual.Bytes(), content) {
		t.Fatalf("content = %q, error = %v", actual.Bytes(), err)
	}
	properties, err := revisionRoot.NodeProps(context.Background(), "/project/hello.txt")
	if err != nil || string(properties["custom:property"]) != "value" {
		t.Fatalf("properties = %v, error = %v", properties, err)
	}
	secondValue, err := filesystem.BeginTxn(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	second := secondValue.(*Transaction)
	secondRootValue, err := second.Root(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondRoot := secondRootValue.(*TxnRoot)
	if err := secondRoot.Copy(context.Background(), 1, "/project/hello.txt", "/project/copied.txt"); err != nil {
		t.Fatal(err)
	}
	if err := secondRoot.Delete(context.Background(), "/project/hello.txt"); err != nil {
		t.Fatal(err)
	}
	if err := secondRoot.MakeFile(context.Background(), "/project/hello.txt"); err != nil {
		t.Fatal(err)
	}
	windows, err := secondRoot.ApplyTextDelta(context.Background(), "/project/hello.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement := []byte("replacement\n")
	if err := windows.Window(&delta.Window{TargetLength: len(replacement), Ops: []delta.Op{{Kind: delta.OpNew, Length: len(replacement)}}, NewData: replacement}); err != nil {
		t.Fatal(err)
	}
	if err := windows.Close(); err != nil {
		t.Fatal(err)
	}
	if revision, err = second.Commit(context.Background(), nil, false); err != nil || revision != 2 {
		t.Fatalf("second commit revision = %d, error = %v", revision, err)
	}
	secondRevisionRoot, err := filesystem.RevisionRoot(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	actual.Reset()
	if err := secondRevisionRoot.FileContents(context.Background(), "/project/copied.txt", &actual); err != nil || !bytes.Equal(actual.Bytes(), content) {
		t.Fatalf("copied content = %q, error = %v", actual.Bytes(), err)
	}
	actual.Reset()
	if err := secondRevisionRoot.FileContents(context.Background(), "/project/hello.txt", &actual); err != nil || !bytes.Equal(actual.Bytes(), replacement) {
		t.Fatalf("replacement content = %q, error = %v", actual.Bytes(), err)
	}
	if output, err := exec.Command(svnadmin, "verify", repositoryPath).CombinedOutput(); err != nil {
		indexOutput, _ := exec.Command("svnfsfs", "dump-index", repositoryPath, "-r", "1").CombinedOutput()
		treeOutput, _ := exec.Command("svnlook", "tree", repositoryPath, "-r", "1").CombinedOutput()
		revisionData, _ := os.ReadFile(filesystem.unpackedRevisionPath(1))
		footer, _ := parseLogicalFooter(revisionData)
		t.Fatalf("svnadmin verify: %v\n%s\nsvnfsfs dump-index:\n%s\nsvnlook tree:\n%s\nrevision data:\n%s\nindexes: %x", err, output, indexOutput, treeOutput, revisionData[:footer.l2pOffset], revisionData[footer.l2pOffset:])
	}
}

func TestCommitPhysicalRevisionWithSVNAdmin(t *testing.T) {
	svnadmin, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin is not installed")
	}
	repositoryPath := filepath.Join(t.TempDir(), "repository")
	if output, err := exec.Command(svnadmin, "create", "--compatible-version", "1.8", repositoryPath).CombinedOutput(); err != nil {
		t.Fatalf("svnadmin create: %v\n%s", err, output)
	}
	filesystem, err := Open(context.Background(), repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	transactionValue, err := filesystem.BeginTxn(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	transaction := transactionValue.(*Transaction)
	rootValue, err := transaction.Root(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := rootValue.(*TxnRoot)
	if err := root.MakeFile(context.Background(), "/physical.txt"); err != nil {
		t.Fatal(err)
	}
	content := []byte("physical addressing\n")
	if err := root.ApplyText(context.Background(), "/physical.txt", bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	if revision, err := transaction.Commit(context.Background(), nil, false); err != nil || revision != 1 {
		t.Fatalf("commit revision = %d, error = %v", revision, err)
	}
	revisionRoot, err := filesystem.RevisionRoot(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	var actual bytes.Buffer
	if err := revisionRoot.FileContents(context.Background(), "/physical.txt", &actual); err != nil || !bytes.Equal(actual.Bytes(), content) {
		t.Fatalf("content = %q, error = %v", actual.Bytes(), err)
	}
	if output, err := exec.Command(svnadmin, "verify", repositoryPath).CombinedOutput(); err != nil {
		revisionData, _ := os.ReadFile(filesystem.unpackedRevisionPath(1))
		t.Fatalf("svnadmin verify: %v\n%s\nrevision data:\n%s", err, output, revisionData)
	}
}
