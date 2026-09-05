package delta

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

func Trace(writer io.Writer, editor Editor) Editor {
	return &traceEditor{trace: &traceWriter{writer: writer}, editor: editor}
}

type traceWriter struct {
	sync.Mutex
	writer io.Writer
}

func (trace *traceWriter) log(format string, args ...any) {
	trace.Lock()
	defer trace.Unlock()
	_, _ = fmt.Fprintf(trace.writer, format+"\n", args...)
}

type traceEditor struct {
	trace  *traceWriter
	editor Editor
}
type traceDir struct {
	trace  *traceWriter
	editor DirEditor
}
type traceFile struct {
	trace  *traceWriter
	editor FileEditor
}
type traceWindows struct {
	trace   *traceWriter
	handler WindowHandler
}

func (wrapper *traceEditor) SetTargetRevision(ctx context.Context, rev svn.Revnum) error {
	wrapper.trace.log("set-target-revision %d", rev)
	return wrapper.editor.SetTargetRevision(ctx, rev)
}
func (wrapper *traceEditor) OpenRoot(ctx context.Context, rev svn.Revnum) (DirEditor, error) {
	wrapper.trace.log("open-root %d", rev)
	child, err := wrapper.editor.OpenRoot(ctx, rev)
	if err != nil {
		return nil, err
	}
	return &traceDir{wrapper.trace, child}, nil
}
func (wrapper *traceEditor) CloseEdit(ctx context.Context) error {
	wrapper.trace.log("close-edit")
	return wrapper.editor.CloseEdit(ctx)
}
func (wrapper *traceEditor) AbortEdit(ctx context.Context) error {
	wrapper.trace.log("abort-edit")
	return wrapper.editor.AbortEdit(ctx)
}
func (wrapper *traceDir) DeleteEntry(ctx context.Context, path string, rev svn.Revnum) error {
	wrapper.trace.log("delete-entry %s %d", path, rev)
	return wrapper.editor.DeleteEntry(ctx, path, rev)
}
func (wrapper *traceDir) AddDirectory(ctx context.Context, path string, source *CopySource) (DirEditor, error) {
	wrapper.trace.log("add-directory %s", path)
	child, err := wrapper.editor.AddDirectory(ctx, path, source)
	if err != nil {
		return nil, err
	}
	return &traceDir{wrapper.trace, child}, nil
}
func (wrapper *traceDir) OpenDirectory(ctx context.Context, path string, rev svn.Revnum) (DirEditor, error) {
	wrapper.trace.log("open-directory %s %d", path, rev)
	child, err := wrapper.editor.OpenDirectory(ctx, path, rev)
	if err != nil {
		return nil, err
	}
	return &traceDir{wrapper.trace, child}, nil
}
func (wrapper *traceDir) ChangeProp(ctx context.Context, name string, value []byte) error {
	wrapper.trace.log("change-dir-prop %s %d", name, len(value))
	return wrapper.editor.ChangeProp(ctx, name, value)
}
func (wrapper *traceDir) AbsentDirectory(ctx context.Context, path string) error {
	wrapper.trace.log("absent-directory %s", path)
	return wrapper.editor.AbsentDirectory(ctx, path)
}
func (wrapper *traceDir) AddFile(ctx context.Context, path string, source *CopySource) (FileEditor, error) {
	wrapper.trace.log("add-file %s", path)
	child, err := wrapper.editor.AddFile(ctx, path, source)
	if err != nil {
		return nil, err
	}
	return &traceFile{wrapper.trace, child}, nil
}
func (wrapper *traceDir) OpenFile(ctx context.Context, path string, rev svn.Revnum) (FileEditor, error) {
	wrapper.trace.log("open-file %s %d", path, rev)
	child, err := wrapper.editor.OpenFile(ctx, path, rev)
	if err != nil {
		return nil, err
	}
	return &traceFile{wrapper.trace, child}, nil
}
func (wrapper *traceDir) AbsentFile(ctx context.Context, path string) error {
	wrapper.trace.log("absent-file %s", path)
	return wrapper.editor.AbsentFile(ctx, path)
}
func (wrapper *traceDir) Close(ctx context.Context) error {
	wrapper.trace.log("close-directory")
	return wrapper.editor.Close(ctx)
}
func (wrapper *traceFile) ApplyTextDelta(ctx context.Context, checksum *svn.Checksum) (WindowHandler, error) {
	wrapper.trace.log("apply-text-delta")
	handler, err := wrapper.editor.ApplyTextDelta(ctx, checksum)
	if err != nil {
		return nil, err
	}
	return &traceWindows{wrapper.trace, handler}, nil
}
func (wrapper *traceFile) ChangeProp(ctx context.Context, name string, value []byte) error {
	wrapper.trace.log("change-file-prop %s %d", name, len(value))
	return wrapper.editor.ChangeProp(ctx, name, value)
}
func (wrapper *traceFile) Close(ctx context.Context, checksum *svn.Checksum) error {
	wrapper.trace.log("close-file")
	return wrapper.editor.Close(ctx, checksum)
}
func (wrapper *traceWindows) Window(window *Window) error {
	wrapper.trace.log("window %d", window.TargetLength)
	return wrapper.handler.Window(window)
}
func (wrapper *traceWindows) Close() error {
	wrapper.trace.log("close-windows")
	return wrapper.handler.Close()
}
