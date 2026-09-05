package delta

import (
	"bytes"
	"context"
	"fmt"

	"github.com/oliver-tuschhoff/go-svn/svn"
	svnpath "github.com/oliver-tuschhoff/go-svn/svn/path"
)

type TreeNode struct {
	Kind            svn.NodeKind
	Props           svn.Props
	Content         []byte
	ContentChecksum svn.Checksum
	CopyFrom        *CopySource
	Absent          bool
	Children        map[string]*TreeNode
}

type TreeBuilder struct {
	root           *TreeNode
	TargetRevision svn.Revnum
	closed         bool
	aborted        bool
}

func NewTreeBuilder() *TreeBuilder {
	return &TreeBuilder{root: newDirectoryNode()}
}

// NewTreeBuilderFrom continues editing an existing tree and takes ownership of root.
func NewTreeBuilderFrom(root *TreeNode) *TreeBuilder {
	if root == nil {
		root = newDirectoryNode()
	}
	return &TreeBuilder{root: root}
}

func (builder *TreeBuilder) Root() *TreeNode { return builder.root }

func (builder *TreeBuilder) SetTargetRevision(_ context.Context, revision svn.Revnum) error {
	builder.TargetRevision = revision
	return nil
}

func (builder *TreeBuilder) OpenRoot(context.Context, svn.Revnum) (DirEditor, error) {
	if builder.closed || builder.aborted {
		return nil, fmt.Errorf("tree editor is closed")
	}
	return &treeDir{node: builder.root}, nil
}

func (builder *TreeBuilder) CloseEdit(context.Context) error { builder.closed = true; return nil }
func (builder *TreeBuilder) AbortEdit(context.Context) error { builder.aborted = true; return nil }

type treeDir struct{ node *TreeNode }
type treeFile struct {
	node   *TreeNode
	source []byte
	target bytes.Buffer
}
type treeWindows struct {
	file   *treeFile
	closed bool
}

func newDirectoryNode() *TreeNode {
	return &TreeNode{Kind: svn.NodeDir, Props: make(svn.Props), Children: make(map[string]*TreeNode)}
}

func (directory *treeDir) DeleteEntry(_ context.Context, path string, _ svn.Revnum) error {
	name := svnpath.RelpathBasename(path)
	if _, ok := directory.node.Children[name]; !ok {
		return fmt.Errorf("delete missing entry %q", path)
	}
	delete(directory.node.Children, name)
	return nil
}

func (directory *treeDir) AddDirectory(_ context.Context, path string, source *CopySource) (DirEditor, error) {
	name := svnpath.RelpathBasename(path)
	if _, exists := directory.node.Children[name]; exists {
		return nil, fmt.Errorf("entry %q already exists", path)
	}
	node := newDirectoryNode()
	node.CopyFrom = cloneCopySource(source)
	directory.node.Children[name] = node
	return &treeDir{node: node}, nil
}

func (directory *treeDir) OpenDirectory(_ context.Context, path string, _ svn.Revnum) (DirEditor, error) {
	node, err := directory.child(path, svn.NodeDir)
	if err != nil {
		return nil, err
	}
	return &treeDir{node: node}, nil
}

func (directory *treeDir) ChangeProp(_ context.Context, name string, value []byte) error {
	changeNodeProp(directory.node, name, value)
	return nil
}

func (directory *treeDir) AbsentDirectory(_ context.Context, path string) error {
	name := svnpath.RelpathBasename(path)
	directory.node.Children[name] = &TreeNode{Kind: svn.NodeDir, Absent: true, Props: make(svn.Props), Children: make(map[string]*TreeNode)}
	return nil
}

func (directory *treeDir) AddFile(_ context.Context, path string, source *CopySource) (FileEditor, error) {
	name := svnpath.RelpathBasename(path)
	if _, exists := directory.node.Children[name]; exists {
		return nil, fmt.Errorf("entry %q already exists", path)
	}
	node := &TreeNode{Kind: svn.NodeFile, Props: make(svn.Props), ContentChecksum: svn.EmptyMD5, CopyFrom: cloneCopySource(source)}
	directory.node.Children[name] = node
	return &treeFile{node: node}, nil
}

func (directory *treeDir) OpenFile(_ context.Context, path string, _ svn.Revnum) (FileEditor, error) {
	node, err := directory.child(path, svn.NodeFile)
	if err != nil {
		return nil, err
	}
	return &treeFile{node: node}, nil
}

func (directory *treeDir) AbsentFile(_ context.Context, path string) error {
	name := svnpath.RelpathBasename(path)
	directory.node.Children[name] = &TreeNode{Kind: svn.NodeFile, Absent: true, Props: make(svn.Props), ContentChecksum: svn.EmptyMD5}
	return nil
}

func (directory *treeDir) Close(context.Context) error { return nil }

func (directory *treeDir) child(path string, kind svn.NodeKind) (*TreeNode, error) {
	name := svnpath.RelpathBasename(path)
	node, ok := directory.node.Children[name]
	if !ok || node.Kind != kind {
		return nil, fmt.Errorf("entry %q is not a %s", path, kind)
	}
	return node, nil
}

func (file *treeFile) ApplyTextDelta(_ context.Context, base *svn.Checksum) (WindowHandler, error) {
	if base != nil {
		if !supportedChecksumKind(base.Kind) {
			return nil, fmt.Errorf("%w: %s", svn.ErrBadChecksumKind, base.Kind)
		}
		actual := svn.Sum(base.Kind, file.node.Content)
		if !actual.Equal(*base) {
			return nil, fmt.Errorf("%w: base checksum", svn.ErrChecksumMismatch)
		}
	}
	file.source = append([]byte(nil), file.node.Content...)
	file.target.Reset()
	return &treeWindows{file: file}, nil
}

func (file *treeFile) ChangeProp(_ context.Context, name string, value []byte) error {
	changeNodeProp(file.node, name, value)
	return nil
}

func (file *treeFile) Close(_ context.Context, result *svn.Checksum) error {
	if result != nil {
		if !supportedChecksumKind(result.Kind) {
			return fmt.Errorf("%w: %s", svn.ErrBadChecksumKind, result.Kind)
		}
		actual := svn.Sum(result.Kind, file.node.Content)
		if !actual.Equal(*result) {
			return fmt.Errorf("%w: result checksum", svn.ErrChecksumMismatch)
		}
	}
	return nil
}

func (windows *treeWindows) Window(window *Window) error {
	if windows.closed {
		return fmt.Errorf("text delta is closed")
	}
	if window.SourceOffset < 0 || window.SourceOffset > int64(len(windows.file.source)) || int64(window.SourceLength) > int64(len(windows.file.source))-window.SourceOffset {
		return invalidOps("source view exceeds file contents")
	}
	start := int(window.SourceOffset)
	contents, err := ApplyWindow(windows.file.source[start:start+window.SourceLength], *window)
	if err != nil {
		return err
	}
	_, _ = windows.file.target.Write(contents)
	return nil
}

func (windows *treeWindows) Close() error {
	if windows.closed {
		return fmt.Errorf("text delta is already closed")
	}
	windows.closed = true
	windows.file.node.Content = append([]byte(nil), windows.file.target.Bytes()...)
	windows.file.node.ContentChecksum = svn.Sum(svn.ChecksumMD5, windows.file.node.Content)
	return nil
}

func changeNodeProp(node *TreeNode, name string, value []byte) {
	if value == nil {
		delete(node.Props, name)
		return
	}
	node.Props[name] = append([]byte(nil), value...)
}

func cloneCopySource(source *CopySource) *CopySource {
	if source == nil {
		return nil
	}
	copy := *source
	return &copy
}

func supportedChecksumKind(kind svn.ChecksumKind) bool {
	return kind == svn.ChecksumMD5 || kind == svn.ChecksumSHA1 || kind == svn.ChecksumFNV1a32
}
