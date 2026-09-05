package delta

import (
	"context"
	"errors"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

func Compose(editors ...Editor) Editor {
	filtered := make([]Editor, 0, len(editors))
	for _, editor := range editors {
		if editor != nil {
			filtered = append(filtered, editor)
		}
	}
	return &composedEditor{editors: filtered}
}

type composedEditor struct{ editors []Editor }
type composedDir struct{ editors []DirEditor }
type composedFile struct{ editors []FileEditor }
type composedWindows struct{ handlers []WindowHandler }

func (editor *composedEditor) SetTargetRevision(ctx context.Context, revision svn.Revnum) error {
	var result error
	for _, child := range editor.editors {
		result = errors.Join(result, child.SetTargetRevision(ctx, revision))
	}
	return result
}

func (editor *composedEditor) OpenRoot(ctx context.Context, revision svn.Revnum) (DirEditor, error) {
	children := make([]DirEditor, 0, len(editor.editors))
	for _, child := range editor.editors {
		opened, err := child.OpenRoot(ctx, revision)
		if err != nil {
			return nil, err
		}
		children = append(children, opened)
	}
	return &composedDir{editors: children}, nil
}

func (editor *composedEditor) CloseEdit(ctx context.Context) error {
	var result error
	for _, child := range editor.editors {
		result = errors.Join(result, child.CloseEdit(ctx))
	}
	return result
}

func (editor *composedEditor) AbortEdit(ctx context.Context) error {
	var result error
	for _, child := range editor.editors {
		result = errors.Join(result, child.AbortEdit(ctx))
	}
	return result
}

func (directory *composedDir) DeleteEntry(ctx context.Context, path string, revision svn.Revnum) error {
	var result error
	for _, child := range directory.editors {
		result = errors.Join(result, child.DeleteEntry(ctx, path, revision))
	}
	return result
}

func (directory *composedDir) AddDirectory(ctx context.Context, path string, source *CopySource) (DirEditor, error) {
	return directory.openDirs(func(child DirEditor) (DirEditor, error) { return child.AddDirectory(ctx, path, source) })
}

func (directory *composedDir) OpenDirectory(ctx context.Context, path string, revision svn.Revnum) (DirEditor, error) {
	return directory.openDirs(func(child DirEditor) (DirEditor, error) { return child.OpenDirectory(ctx, path, revision) })
}

func (directory *composedDir) openDirs(open func(DirEditor) (DirEditor, error)) (DirEditor, error) {
	children := make([]DirEditor, 0, len(directory.editors))
	for _, child := range directory.editors {
		opened, err := open(child)
		if err != nil {
			return nil, err
		}
		children = append(children, opened)
	}
	return &composedDir{editors: children}, nil
}

func (directory *composedDir) ChangeProp(ctx context.Context, name string, value []byte) error {
	var result error
	for _, child := range directory.editors {
		result = errors.Join(result, child.ChangeProp(ctx, name, value))
	}
	return result
}

func (directory *composedDir) AbsentDirectory(ctx context.Context, path string) error {
	var result error
	for _, child := range directory.editors {
		result = errors.Join(result, child.AbsentDirectory(ctx, path))
	}
	return result
}

func (directory *composedDir) AddFile(ctx context.Context, path string, source *CopySource) (FileEditor, error) {
	return directory.openFiles(func(child DirEditor) (FileEditor, error) { return child.AddFile(ctx, path, source) })
}

func (directory *composedDir) OpenFile(ctx context.Context, path string, revision svn.Revnum) (FileEditor, error) {
	return directory.openFiles(func(child DirEditor) (FileEditor, error) { return child.OpenFile(ctx, path, revision) })
}

func (directory *composedDir) openFiles(open func(DirEditor) (FileEditor, error)) (FileEditor, error) {
	children := make([]FileEditor, 0, len(directory.editors))
	for _, child := range directory.editors {
		opened, err := open(child)
		if err != nil {
			return nil, err
		}
		children = append(children, opened)
	}
	return &composedFile{editors: children}, nil
}

func (directory *composedDir) AbsentFile(ctx context.Context, path string) error {
	var result error
	for _, child := range directory.editors {
		result = errors.Join(result, child.AbsentFile(ctx, path))
	}
	return result
}

func (directory *composedDir) Close(ctx context.Context) error {
	var result error
	for _, child := range directory.editors {
		result = errors.Join(result, child.Close(ctx))
	}
	return result
}

func (file *composedFile) ApplyTextDelta(ctx context.Context, checksum *svn.Checksum) (WindowHandler, error) {
	handlers := make([]WindowHandler, 0, len(file.editors))
	for _, child := range file.editors {
		handler, err := child.ApplyTextDelta(ctx, checksum)
		if err != nil {
			return nil, err
		}
		handlers = append(handlers, handler)
	}
	return &composedWindows{handlers: handlers}, nil
}

func (file *composedFile) ChangeProp(ctx context.Context, name string, value []byte) error {
	var result error
	for _, child := range file.editors {
		result = errors.Join(result, child.ChangeProp(ctx, name, value))
	}
	return result
}

func (file *composedFile) Close(ctx context.Context, checksum *svn.Checksum) error {
	var result error
	for _, child := range file.editors {
		result = errors.Join(result, child.Close(ctx, checksum))
	}
	return result
}

func (windows *composedWindows) Window(window *Window) error {
	var result error
	for _, child := range windows.handlers {
		result = errors.Join(result, child.Window(window))
	}
	return result
}

func (windows *composedWindows) Close() error {
	var result error
	for _, child := range windows.handlers {
		result = errors.Join(result, child.Close())
	}
	return result
}
