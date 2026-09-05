package delta

import (
	"context"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

func TestNoopEditor(t *testing.T) {
	ctx := context.Background()
	editor := Noop()
	root, err := editor.OpenRoot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := root.AddDirectory(ctx, "dir", nil)
	if err != nil {
		t.Fatal(err)
	}
	file, err := directory.AddFile(ctx, "dir/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := file.ApplyTextDelta(ctx, nil)
	if err != nil || handler.Window(&Window{}) != nil || handler.Close() != nil || file.Close(ctx, nil) != nil || directory.Close(ctx) != nil || root.Close(ctx) != nil || editor.CloseEdit(ctx) != nil {
		t.Fatal("noop editor returned an error")
	}
}

func TestComposeDrivesEveryEditor(t *testing.T) {
	first := &countingEditor{}
	second := &countingEditor{}
	editor := Compose(first, second)
	ctx := context.Background()
	if err := editor.SetTargetRevision(ctx, 4); err != nil {
		t.Fatal(err)
	}
	root, err := editor.OpenRoot(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.ChangeProp(ctx, "name", []byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := root.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := editor.CloseEdit(ctx); err != nil {
		t.Fatal(err)
	}
	if first.calls != 5 || second.calls != 5 {
		t.Fatalf("calls = %d, %d", first.calls, second.calls)
	}
}

type countingEditor struct{ calls int }
type countingDir struct{ owner *countingEditor }

func (editor *countingEditor) SetTargetRevision(context.Context, svn.Revnum) error {
	editor.calls++
	return nil
}
func (editor *countingEditor) OpenRoot(context.Context, svn.Revnum) (DirEditor, error) {
	editor.calls++
	return &countingDir{owner: editor}, nil
}
func (editor *countingEditor) CloseEdit(context.Context) error { editor.calls++; return nil }
func (editor *countingEditor) AbortEdit(context.Context) error { editor.calls++; return nil }
func (directory *countingDir) DeleteEntry(context.Context, string, svn.Revnum) error {
	directory.owner.calls++
	return nil
}
func (directory *countingDir) AddDirectory(context.Context, string, *CopySource) (DirEditor, error) {
	directory.owner.calls++
	return directory, nil
}
func (directory *countingDir) OpenDirectory(context.Context, string, svn.Revnum) (DirEditor, error) {
	directory.owner.calls++
	return directory, nil
}
func (directory *countingDir) ChangeProp(context.Context, string, []byte) error {
	directory.owner.calls++
	return nil
}
func (directory *countingDir) AbsentDirectory(context.Context, string) error {
	directory.owner.calls++
	return nil
}
func (directory *countingDir) AddFile(context.Context, string, *CopySource) (FileEditor, error) {
	directory.owner.calls++
	return noopFile{}, nil
}
func (directory *countingDir) OpenFile(context.Context, string, svn.Revnum) (FileEditor, error) {
	directory.owner.calls++
	return noopFile{}, nil
}
func (directory *countingDir) AbsentFile(context.Context, string) error {
	directory.owner.calls++
	return nil
}
func (directory *countingDir) Close(context.Context) error { directory.owner.calls++; return nil }
