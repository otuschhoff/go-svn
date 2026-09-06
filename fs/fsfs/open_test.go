package fsfs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestOpenFixtureFormats(t *testing.T) {
	for _, number := range []int{1, 2, 3, 4, 6, 7, 8} {
		t.Run(strconv.Itoa(number), func(t *testing.T) {
			repository := extractFixture(t, number)
			filesystem, err := Open(context.Background(), repository)
			if err != nil {
				t.Fatal(err)
			}
			if filesystem.Format().Number != number {
				t.Fatalf("format=%d", filesystem.Format().Number)
			}
			wantAddressing := AddressingPhysical
			if number >= 7 {
				wantAddressing = AddressingLogical
			}
			if filesystem.Format().Addressing != wantAddressing {
				t.Fatalf("addressing=%s", filesystem.Format().Addressing)
			}
			if revision, err := filesystem.YoungestRevision(context.Background()); err != nil || revision != 4 {
				t.Fatalf("youngest=%d error=%v", revision, err)
			}
		})
	}
}

func TestRevisionRootsInFixtureFormats(t *testing.T) {
	for _, number := range []int{1, 2, 3, 4, 6, 7, 8} {
		t.Run(strconv.Itoa(number), func(t *testing.T) {
			filesystem, err := Open(context.Background(), extractFixture(t, number))
			if err != nil {
				t.Fatal(err)
			}
			for revision := int64(0); revision <= 4; revision++ {
				root, err := filesystem.RevisionRoot(context.Background(), svn.Revnum(revision))
				if err != nil {
					t.Fatalf("r%d: %v", revision, err)
				}
				kind, err := root.CheckPath(context.Background(), "/")
				if err != nil || kind != svn.NodeDir || root.Revision() != svn.Revnum(revision) {
					t.Fatalf("r%d: kind=%s revision=%d error=%v", revision, kind, root.Revision(), err)
				}
			}
		})
	}
}

func TestReadTreesInFixtureFormats(t *testing.T) {
	wantReadme := []byte("go-svn fixture\nsecond line\n")
	for _, number := range []int{1, 2, 3, 4, 6, 7, 8} {
		t.Run(strconv.Itoa(number), func(t *testing.T) {
			filesystem, err := Open(context.Background(), extractFixture(t, number))
			if err != nil {
				t.Fatal(err)
			}
			root, err := filesystem.RevisionRoot(context.Background(), 3)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := root.DirEntries(context.Background(), "trunk")
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 2 || entries[0].Name != "README.txt" || entries[1].Name != "run.sh" {
				t.Fatalf("entries=%v", entries)
			}
			var contents bytes.Buffer
			if err := root.FileContents(context.Background(), "/trunk/README.txt", &contents); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(contents.Bytes(), wantReadme) {
				t.Fatalf("README=%q", contents.Bytes())
			}
			properties, err := root.NodeProps(context.Background(), "trunk/README.txt")
			if err != nil {
				t.Fatal(err)
			}
			if string(properties["svn:eol-style"]) != "LF" {
				t.Fatalf("properties=%v", properties)
			}
			root4, err := filesystem.RevisionRoot(context.Background(), 4)
			if err != nil {
				t.Fatal(err)
			}
			entries, err = root4.DirEntries(context.Background(), "trunk")
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name != "README.txt" {
				t.Fatalf("r4 entries=%v", entries)
			}
			kind, err := root4.CheckPath(context.Background(), "trunk/run.sh")
			if err != nil || kind != svn.NodeNone {
				t.Fatalf("deleted kind=%s error=%v", kind, err)
			}
			changes, err := root4.PathsChanged(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			wantKind := svn.NodeUnknown
			if number >= 4 {
				wantKind = svn.NodeFile
			}
			if change, ok := changes["/trunk/run.sh"]; !ok || change.Kind != "delete" || change.NodeKind != wantKind {
				t.Fatalf("changes=%v", changes)
			}
		})
	}
}

func TestRevisionPropsInFixtureFormats(t *testing.T) {
	for _, number := range []int{1, 2, 3, 4, 6, 7, 8} {
		t.Run(strconv.Itoa(number), func(t *testing.T) {
			filesystem, err := Open(context.Background(), extractFixture(t, number))
			if err != nil {
				t.Fatal(err)
			}
			for revision := svn.Revnum(0); revision <= 4; revision++ {
				properties, err := filesystem.RevisionProps(context.Background(), revision)
				if err != nil {
					t.Fatalf("r%d: %v", revision, err)
				}
				if revision > 0 && strings.TrimSpace(string(properties["svn:author"])) != "fixture" {
					t.Fatalf("r%d properties=%v", revision, properties)
				}
			}
			properties, err := filesystem.RevisionProps(context.Background(), 4)
			if err != nil || strings.TrimSpace(string(properties["svn:log"])) != "delete executable" {
				t.Fatalf("r4 properties=%v error=%v", properties, err)
			}
		})
	}
}

func extractFixture(t *testing.T, number int) string {
	t.Helper()
	archive, err := os.Open(filepath.Join("..", "..", "testdata", "fsfs", "format"+strconv.Itoa(number)+".tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	destination := t.TempDir()
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(destination, filepath.FromSlash(header.Name))
		if header.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, header.FileInfo().Mode())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(file, reader); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(destination, "format"+strconv.Itoa(number))
}
