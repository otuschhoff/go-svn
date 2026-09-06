package delta

import (
	"context"

	"github.com/otuschhoff/go-svn/svn"
)

func Cancel(ctx context.Context, editor Editor) Editor {
	return &cancelEditor{ctx: ctx, editor: editor}
}

type cancelEditor struct {
	ctx    context.Context
	editor Editor
}
type cancelDir struct {
	ctx    context.Context
	editor DirEditor
}
type cancelFile struct {
	ctx    context.Context
	editor FileEditor
}
type cancelWindows struct {
	ctx     context.Context
	handler WindowHandler
}

func checkContexts(fixed, current context.Context) error {
	if err := fixed.Err(); err != nil {
		return err
	}
	return current.Err()
}

func (wrapper *cancelEditor) SetTargetRevision(ctx context.Context, rev svn.Revnum) error {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return err
	}
	return wrapper.editor.SetTargetRevision(ctx, rev)
}
func (wrapper *cancelEditor) OpenRoot(ctx context.Context, rev svn.Revnum) (DirEditor, error) {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return nil, err
	}
	child, err := wrapper.editor.OpenRoot(ctx, rev)
	if err != nil {
		return nil, err
	}
	return &cancelDir{ctx: wrapper.ctx, editor: child}, nil
}
func (wrapper *cancelEditor) CloseEdit(ctx context.Context) error {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return err
	}
	return wrapper.editor.CloseEdit(ctx)
}
func (wrapper *cancelEditor) AbortEdit(ctx context.Context) error {
	return wrapper.editor.AbortEdit(ctx)
}

func (wrapper *cancelDir) DeleteEntry(ctx context.Context, path string, rev svn.Revnum) error {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return err
	}
	return wrapper.editor.DeleteEntry(ctx, path, rev)
}
func (wrapper *cancelDir) AddDirectory(ctx context.Context, path string, source *CopySource) (DirEditor, error) {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return nil, err
	}
	child, err := wrapper.editor.AddDirectory(ctx, path, source)
	if err != nil {
		return nil, err
	}
	return &cancelDir{ctx: wrapper.ctx, editor: child}, nil
}
func (wrapper *cancelDir) OpenDirectory(ctx context.Context, path string, rev svn.Revnum) (DirEditor, error) {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return nil, err
	}
	child, err := wrapper.editor.OpenDirectory(ctx, path, rev)
	if err != nil {
		return nil, err
	}
	return &cancelDir{ctx: wrapper.ctx, editor: child}, nil
}
func (wrapper *cancelDir) ChangeProp(ctx context.Context, name string, value []byte) error {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return err
	}
	return wrapper.editor.ChangeProp(ctx, name, value)
}
func (wrapper *cancelDir) AbsentDirectory(ctx context.Context, path string) error {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return err
	}
	return wrapper.editor.AbsentDirectory(ctx, path)
}
func (wrapper *cancelDir) AddFile(ctx context.Context, path string, source *CopySource) (FileEditor, error) {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return nil, err
	}
	child, err := wrapper.editor.AddFile(ctx, path, source)
	if err != nil {
		return nil, err
	}
	return &cancelFile{ctx: wrapper.ctx, editor: child}, nil
}
func (wrapper *cancelDir) OpenFile(ctx context.Context, path string, rev svn.Revnum) (FileEditor, error) {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return nil, err
	}
	child, err := wrapper.editor.OpenFile(ctx, path, rev)
	if err != nil {
		return nil, err
	}
	return &cancelFile{ctx: wrapper.ctx, editor: child}, nil
}
func (wrapper *cancelDir) AbsentFile(ctx context.Context, path string) error {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return err
	}
	return wrapper.editor.AbsentFile(ctx, path)
}
func (wrapper *cancelDir) Close(ctx context.Context) error {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return err
	}
	return wrapper.editor.Close(ctx)
}

func (wrapper *cancelFile) ApplyTextDelta(ctx context.Context, checksum *svn.Checksum) (WindowHandler, error) {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return nil, err
	}
	handler, err := wrapper.editor.ApplyTextDelta(ctx, checksum)
	if err != nil {
		return nil, err
	}
	return &cancelWindows{ctx: wrapper.ctx, handler: handler}, nil
}
func (wrapper *cancelFile) ChangeProp(ctx context.Context, name string, value []byte) error {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return err
	}
	return wrapper.editor.ChangeProp(ctx, name, value)
}
func (wrapper *cancelFile) Close(ctx context.Context, checksum *svn.Checksum) error {
	if err := checkContexts(wrapper.ctx, ctx); err != nil {
		return err
	}
	return wrapper.editor.Close(ctx, checksum)
}
func (wrapper *cancelWindows) Window(window *Window) error {
	if err := wrapper.ctx.Err(); err != nil {
		return err
	}
	return wrapper.handler.Window(window)
}
func (wrapper *cancelWindows) Close() error {
	if err := wrapper.ctx.Err(); err != nil {
		return err
	}
	return wrapper.handler.Close()
}
