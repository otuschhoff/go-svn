package delta

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/otuschhoff/go-svn/svn"
)

func TestTreeBuilderBuildsSnapshot(t *testing.T) {
	ctx := context.Background()
	builder := NewTreeBuilder()
	if err := builder.SetTargetRevision(ctx, 7); err != nil {
		t.Fatal(err)
	}
	root, err := builder.OpenRoot(ctx, 6)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.ChangeProp(ctx, "root", []byte("yes")); err != nil {
		t.Fatal(err)
	}
	directory, err := root.AddDirectory(ctx, "trunk", &CopySource{Path: "/branches/a", Rev: 3})
	if err != nil {
		t.Fatal(err)
	}
	file, err := directory.AddFile(ctx, "trunk/readme", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.ChangeProp(ctx, "mime", []byte("text/plain")); err != nil {
		t.Fatal(err)
	}
	handler, err := file.ApplyTextDelta(ctx, &svn.EmptyMD5)
	if err != nil {
		t.Fatal(err)
	}
	checksum, err := SendContents([]byte("hello world"), handler)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(ctx, &checksum); err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := builder.CloseEdit(ctx); err != nil {
		t.Fatal(err)
	}
	node := builder.Root().Children["trunk"].Children["readme"]
	if builder.TargetRevision != 7 || !bytes.Equal(node.Content, []byte("hello world")) || string(node.Props["mime"]) != "text/plain" {
		t.Fatalf("tree = %#v", builder.Root())
	}
	if !node.ContentChecksum.Equal(svn.Sum(svn.ChecksumMD5, node.Content)) {
		t.Fatalf("content checksum = %s", node.ContentChecksum.Hex())
	}
	if source := builder.Root().Children["trunk"].CopyFrom; source == nil || source.Path != "/branches/a" || source.Rev != 3 {
		t.Fatalf("copy source = %#v", source)
	}
}

func TestTreeBuilderOpenModifyDelete(t *testing.T) {
	ctx := context.Background()
	builder := NewTreeBuilder()
	root, _ := builder.OpenRoot(ctx, 0)
	file, _ := root.AddFile(ctx, "file", nil)
	handler, _ := file.ApplyTextDelta(ctx, nil)
	_, _ = SendContents([]byte("old"), handler)
	file, err := root.OpenFile(ctx, "file", 1)
	if err != nil {
		t.Fatal(err)
	}
	handler, err = file.ApplyTextDelta(ctx, ptrChecksum(svn.Sum(svn.ChecksumMD5, []byte("old"))))
	if err != nil {
		t.Fatal(err)
	}
	_, err = SendString("old", "new contents", handler)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.DeleteEntry(ctx, "file", 1); err != nil {
		t.Fatal(err)
	}
	if len(builder.Root().Children) != 0 {
		t.Fatal("file was not deleted")
	}
}

func TestTreeBuilderRejectsInvalidOperations(t *testing.T) {
	ctx := context.Background()
	builder := NewTreeBuilder()
	root, _ := builder.OpenRoot(ctx, 0)
	file, err := root.AddFile(ctx, "file", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.AddFile(ctx, "file", nil); err == nil {
		t.Fatal("accepted duplicate file")
	}
	if _, err := root.OpenFile(ctx, "missing", 0); err == nil {
		t.Fatal("opened missing file")
	}
	badKind := svn.Checksum{Kind: svn.ChecksumKind(255)}
	if _, err := file.ApplyTextDelta(ctx, &badKind); !errors.Is(err, svn.ErrBadChecksumKind) {
		t.Fatalf("base checksum error = %v", err)
	}
	wrong := svn.Sum(svn.ChecksumMD5, []byte("wrong"))
	if _, err := file.ApplyTextDelta(ctx, &wrong); !errors.Is(err, svn.ErrChecksumMismatch) {
		t.Fatalf("base checksum error = %v", err)
	}
	if err := file.Close(ctx, &wrong); !errors.Is(err, svn.ErrChecksumMismatch) {
		t.Fatalf("result checksum error = %v", err)
	}
}

func ptrChecksum(checksum svn.Checksum) *svn.Checksum { return &checksum }
