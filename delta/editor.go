package delta

import (
	"context"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

type Editor interface {
	SetTargetRevision(context.Context, svn.Revnum) error
	OpenRoot(context.Context, svn.Revnum) (DirEditor, error)
	CloseEdit(context.Context) error
	AbortEdit(context.Context) error
}

type DirEditor interface {
	DeleteEntry(context.Context, string, svn.Revnum) error
	AddDirectory(context.Context, string, *CopySource) (DirEditor, error)
	OpenDirectory(context.Context, string, svn.Revnum) (DirEditor, error)
	ChangeProp(context.Context, string, []byte) error
	AbsentDirectory(context.Context, string) error
	AddFile(context.Context, string, *CopySource) (FileEditor, error)
	OpenFile(context.Context, string, svn.Revnum) (FileEditor, error)
	AbsentFile(context.Context, string) error
	Close(context.Context) error
}

type FileEditor interface {
	ApplyTextDelta(context.Context, *svn.Checksum) (WindowHandler, error)
	ChangeProp(context.Context, string, []byte) error
	Close(context.Context, *svn.Checksum) error
}

type CopySource struct {
	Path string
	Rev  svn.Revnum
}
