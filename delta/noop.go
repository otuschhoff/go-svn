package delta

import (
	"context"

	"github.com/otuschhoff/go-svn/svn"
)

func Noop() Editor { return noopEditor{} }

type noopEditor struct{}
type noopDir struct{}
type noopFile struct{}
type noopWindows struct{}

func (noopEditor) SetTargetRevision(context.Context, svn.Revnum) error { return nil }
func (noopEditor) OpenRoot(context.Context, svn.Revnum) (DirEditor, error) {
	return noopDir{}, nil
}
func (noopEditor) CloseEdit(context.Context) error { return nil }
func (noopEditor) AbortEdit(context.Context) error { return nil }

func (noopDir) DeleteEntry(context.Context, string, svn.Revnum) error { return nil }
func (noopDir) AddDirectory(context.Context, string, *CopySource) (DirEditor, error) {
	return noopDir{}, nil
}
func (noopDir) OpenDirectory(context.Context, string, svn.Revnum) (DirEditor, error) {
	return noopDir{}, nil
}
func (noopDir) ChangeProp(context.Context, string, []byte) error { return nil }
func (noopDir) AbsentDirectory(context.Context, string) error    { return nil }
func (noopDir) AddFile(context.Context, string, *CopySource) (FileEditor, error) {
	return noopFile{}, nil
}
func (noopDir) OpenFile(context.Context, string, svn.Revnum) (FileEditor, error) {
	return noopFile{}, nil
}
func (noopDir) AbsentFile(context.Context, string) error { return nil }
func (noopDir) Close(context.Context) error              { return nil }

func (noopFile) ApplyTextDelta(context.Context, *svn.Checksum) (WindowHandler, error) {
	return noopWindows{}, nil
}
func (noopFile) ChangeProp(context.Context, string, []byte) error { return nil }
func (noopFile) Close(context.Context, *svn.Checksum) error       { return nil }
func (noopWindows) Window(*Window) error                          { return nil }
func (noopWindows) Close() error                                  { return nil }
