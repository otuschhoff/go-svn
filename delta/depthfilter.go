package delta

import (
	"context"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
	svnpath "github.com/otuschhoff/go-svn/svn/path"
)

func DepthFilter(editor Editor, depth svn.Depth, target string) Editor {
	return &depthEditor{editor: editor, depth: depth, target: svnpath.RelpathCanonicalize(target)}
}

type depthEditor struct {
	editor Editor
	depth  svn.Depth
	target string
}
type depthDir struct {
	editor    DirEditor
	depth     svn.Depth
	target    string
	delegated bool
}

func (wrapper *depthEditor) SetTargetRevision(ctx context.Context, rev svn.Revnum) error {
	return wrapper.editor.SetTargetRevision(ctx, rev)
}
func (wrapper *depthEditor) OpenRoot(ctx context.Context, rev svn.Revnum) (DirEditor, error) {
	child, err := wrapper.editor.OpenRoot(ctx, rev)
	if err != nil {
		return nil, err
	}
	return &depthDir{editor: child, depth: wrapper.depth, target: wrapper.target, delegated: true}, nil
}
func (wrapper *depthEditor) CloseEdit(ctx context.Context) error {
	return wrapper.editor.CloseEdit(ctx)
}
func (wrapper *depthEditor) AbortEdit(ctx context.Context) error {
	return wrapper.editor.AbortEdit(ctx)
}

func (wrapper *depthDir) DeleteEntry(ctx context.Context, path string, rev svn.Revnum) error {
	if !wrapper.delegated || !allowsUnknown(wrapper.depth, relativeLevel(wrapper.target, path)) {
		return nil
	}
	return wrapper.editor.DeleteEntry(ctx, path, rev)
}
func (wrapper *depthDir) AddDirectory(ctx context.Context, path string, source *CopySource) (DirEditor, error) {
	return wrapper.directory(ctx, path, func() (DirEditor, error) { return wrapper.editor.AddDirectory(ctx, path, source) })
}
func (wrapper *depthDir) OpenDirectory(ctx context.Context, path string, rev svn.Revnum) (DirEditor, error) {
	return wrapper.directory(ctx, path, func() (DirEditor, error) { return wrapper.editor.OpenDirectory(ctx, path, rev) })
}
func (wrapper *depthDir) directory(_ context.Context, path string, open func() (DirEditor, error)) (DirEditor, error) {
	if !wrapper.delegated || !allowsDirectory(wrapper.depth, relativeLevel(wrapper.target, path)) {
		return noopDir{}, nil
	}
	child, err := open()
	if err != nil {
		return nil, err
	}
	return &depthDir{editor: child, depth: wrapper.depth, target: wrapper.target, delegated: true}, nil
}
func (wrapper *depthDir) ChangeProp(ctx context.Context, name string, value []byte) error {
	if !wrapper.delegated {
		return nil
	}
	return wrapper.editor.ChangeProp(ctx, name, value)
}
func (wrapper *depthDir) AbsentDirectory(ctx context.Context, path string) error {
	if !wrapper.delegated {
		return nil
	}
	return wrapper.editor.AbsentDirectory(ctx, path)
}
func (wrapper *depthDir) AddFile(ctx context.Context, path string, source *CopySource) (FileEditor, error) {
	return wrapper.file(ctx, path, func() (FileEditor, error) { return wrapper.editor.AddFile(ctx, path, source) })
}
func (wrapper *depthDir) OpenFile(ctx context.Context, path string, rev svn.Revnum) (FileEditor, error) {
	return wrapper.file(ctx, path, func() (FileEditor, error) { return wrapper.editor.OpenFile(ctx, path, rev) })
}
func (wrapper *depthDir) file(_ context.Context, path string, open func() (FileEditor, error)) (FileEditor, error) {
	if !wrapper.delegated || !allowsFile(wrapper.depth, relativeLevel(wrapper.target, path)) {
		return noopFile{}, nil
	}
	return open()
}
func (wrapper *depthDir) AbsentFile(ctx context.Context, path string) error {
	if !wrapper.delegated {
		return nil
	}
	return wrapper.editor.AbsentFile(ctx, path)
}
func (wrapper *depthDir) Close(ctx context.Context) error {
	if !wrapper.delegated {
		return nil
	}
	return wrapper.editor.Close(ctx)
}

func relativeLevel(target, path string) int {
	path = svnpath.RelpathCanonicalize(path)
	if target != "" {
		remaining, ok := svnpath.RelpathSkipAncestor(target, path)
		if !ok {
			if _, ancestor := svnpath.RelpathSkipAncestor(path, target); ancestor {
				return 0
			}
			return -1
		}
		path = remaining
	}
	if path == "" {
		return 0
	}
	return strings.Count(path, "/") + 1
}

func allowsDirectory(depth svn.Depth, level int) bool {
	if level < 0 {
		return false
	}
	switch depth {
	case svn.DepthInfinity, svn.DepthUnknown:
		return true
	case svn.DepthImmediates:
		return level <= 1
	case svn.DepthEmpty, svn.DepthFiles:
		return level == 0
	default:
		return false
	}
}

func allowsFile(depth svn.Depth, level int) bool {
	if level < 0 {
		return false
	}
	switch depth {
	case svn.DepthInfinity, svn.DepthUnknown:
		return true
	case svn.DepthFiles, svn.DepthImmediates:
		return level <= 1
	case svn.DepthEmpty:
		return level == 0
	default:
		return false
	}
}

func allowsUnknown(depth svn.Depth, level int) bool {
	return allowsFile(depth, level)
}
