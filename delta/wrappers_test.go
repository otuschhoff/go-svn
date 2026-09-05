package delta

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCancelStopsDelegation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	child := &countingEditor{}
	editor := Cancel(ctx, child)
	cancel()
	if err := editor.SetTargetRevision(context.Background(), 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if child.calls != 0 {
		t.Fatalf("delegated %d calls", child.calls)
	}
	if err := editor.AbortEdit(context.Background()); err != nil || child.calls != 1 {
		t.Fatalf("abort error = %v, calls = %d", err, child.calls)
	}
}

func TestTraceCallOrder(t *testing.T) {
	var output bytes.Buffer
	editor := Trace(&output, Noop())
	ctx := context.Background()
	_ = editor.SetTargetRevision(ctx, 2)
	root, _ := editor.OpenRoot(ctx, 1)
	file, _ := root.AddFile(ctx, "a", nil)
	handler, _ := file.ApplyTextDelta(ctx, nil)
	_ = handler.Window(&Window{TargetLength: 3})
	_ = handler.Close()
	_ = file.Close(ctx, nil)
	_ = root.Close(ctx)
	_ = editor.CloseEdit(ctx)
	want := strings.Join([]string{"set-target-revision 2", "open-root 1", "add-file a", "apply-text-delta", "window 3", "close-windows", "close-file", "close-directory", "close-edit", ""}, "\n")
	if output.String() != want {
		t.Fatalf("trace = %q, want %q", output.String(), want)
	}
}
