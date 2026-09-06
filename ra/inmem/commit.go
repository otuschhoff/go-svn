package inmem

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"time"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

type commitEditor struct {
	session      *Session
	root         *Node
	props        svn.Props
	lockTokens   map[string]string
	keepLocks    bool
	callback     func(*ra.CommitInfo) error
	baseYoungest svn.Revnum
	changes      []svn.ChangedPath
	done         bool
}

type commitDir struct {
	editor *commitEditor
	node   *Node
	path   string
}

type commitFile struct {
	editor *commitEditor
	node   *Node
	path   string
	base   []byte
	target bytes.Buffer
	delta  bool
}

type commitWindows struct {
	file   *commitFile
	closed bool
}

func (session *Session) newCommitEditor(props svn.Props, lockTokens map[string]string, keepLocks bool, callback func(*ra.CommitInfo) error) (delta.Editor, error) {
	session.repository.mu.RLock()
	defer session.repository.mu.RUnlock()
	youngest := svn.Revnum(len(session.repository.revisions) - 1)
	root := cloneNode(session.repository.revisions[youngest].Root)
	tokens := make(map[string]string, len(lockTokens))
	for name, token := range lockTokens {
		tokens["/"+join(session.base, name)] = token
	}
	return &commitEditor{session: session, root: root, props: props.Clone(), lockTokens: tokens, keepLocks: keepLocks, callback: callback, baseYoungest: youngest}, nil
}

func (*commitEditor) SetTargetRevision(context.Context, svn.Revnum) error { return nil }

func (editor *commitEditor) OpenRoot(_ context.Context, revision svn.Revnum) (delta.DirEditor, error) {
	if editor.done || !revision.IsValid() || revision > editor.baseYoungest {
		return nil, fmt.Errorf("%w: invalid commit root revision", svn.ErrIncorrectParams)
	}
	root := findNode(editor.root, editor.session.base)
	if root == nil || root.Kind != svn.NodeDir {
		return nil, fmt.Errorf("%w: session root", svn.ErrFSNotFound)
	}
	return &commitDir{editor: editor, node: root}, nil
}

func (editor *commitEditor) CloseEdit(context.Context) error {
	if editor.done {
		return fmt.Errorf("%w: commit editor complete", svn.ErrIncorrectParams)
	}
	editor.done = true
	repository := editor.session.repository
	repository.mu.Lock()
	if svn.Revnum(len(repository.revisions)-1) != editor.baseYoungest {
		repository.mu.Unlock()
		return fmt.Errorf("%w: repository changed during commit", svn.ErrFSOutOfDate)
	}
	for changedPath := range changedPathSet(editor.changes) {
		if lock := repository.locks[changedPath]; lock != nil && editor.lockTokens[changedPath] != lock.Token {
			repository.mu.Unlock()
			return fmt.Errorf("%w: %s", svn.ErrFSBadLockToken, changedPath)
		}
	}
	now := time.Now().UTC()
	props := editor.props.Clone()
	if props == nil {
		props = make(svn.Props)
	}
	props["svn:date"] = []byte(svn.FormatDate(now))
	revision := svn.Revnum(len(repository.revisions))
	stampMetadata(editor.root, revision, now, string(props["svn:author"]))
	repository.revisions = append(repository.revisions, Revision{Root: cloneNode(editor.root), Props: props, Time: now, Changes: append([]svn.ChangedPath(nil), editor.changes...)})
	if !editor.keepLocks {
		for changedPath := range changedPathSet(editor.changes) {
			if editor.lockTokens[changedPath] != "" {
				delete(repository.locks, changedPath)
			}
		}
	}
	repository.mu.Unlock()
	if editor.callback != nil {
		return editor.callback(&ra.CommitInfo{Revision: revision, Date: now, Author: string(props["svn:author"]), ReposRoot: repository.rootURL})
	}
	return nil
}

func (editor *commitEditor) AbortEdit(context.Context) error { editor.done = true; return nil }

func (directory *commitDir) DeleteEntry(_ context.Context, name string, revision svn.Revnum) error {
	base := path.Base(clean(name))
	node := directory.node.Children[base]
	if node == nil {
		return fmt.Errorf("%w: %s", svn.ErrFSNotFound, name)
	}
	if revision.IsValid() && node.CreatedRev > revision {
		return fmt.Errorf("%w: %s", svn.ErrFSOutOfDate, name)
	}
	delete(directory.node.Children, base)
	directory.editor.touch(directory.node)
	directory.editor.change(name, svn.LogDeleted, node.Kind, nil)
	return nil
}

func (directory *commitDir) AddDirectory(_ context.Context, name string, source *delta.CopySource) (delta.DirEditor, error) {
	base := path.Base(clean(name))
	if directory.node.Children[base] != nil {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSAlreadyExists, name)
	}
	node, err := directory.editor.copyNode(source, svn.NodeDir)
	if err != nil {
		return nil, err
	}
	directory.node.Children[base] = node
	directory.editor.touch(directory.node)
	directory.editor.change(name, svn.LogAdded, svn.NodeDir, source)
	return &commitDir{editor: directory.editor, node: node, path: name}, nil
}

func (directory *commitDir) OpenDirectory(_ context.Context, name string, revision svn.Revnum) (delta.DirEditor, error) {
	node := directory.node.Children[path.Base(clean(name))]
	if node == nil || node.Kind != svn.NodeDir {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSNotFound, name)
	}
	if revision.IsValid() && node.CreatedRev > revision {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSOutOfDate, name)
	}
	return &commitDir{editor: directory.editor, node: node, path: name}, nil
}

func (directory *commitDir) ChangeProp(_ context.Context, name string, value []byte) error {
	changeNodeProp(directory.node, name, value)
	directory.editor.touch(directory.node)
	directory.editor.change(directory.path, svn.LogModified, svn.NodeDir, nil)
	return nil
}

func (*commitDir) AbsentDirectory(context.Context, string) error { return nil }

func (directory *commitDir) AddFile(_ context.Context, name string, source *delta.CopySource) (delta.FileEditor, error) {
	base := path.Base(clean(name))
	if directory.node.Children[base] != nil {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSAlreadyExists, name)
	}
	node, err := directory.editor.copyNode(source, svn.NodeFile)
	if err != nil {
		return nil, err
	}
	directory.node.Children[base] = node
	directory.editor.touch(directory.node)
	directory.editor.change(name, svn.LogAdded, svn.NodeFile, source)
	return &commitFile{editor: directory.editor, node: node, path: name, base: append([]byte(nil), node.Content...)}, nil
}

func (directory *commitDir) OpenFile(_ context.Context, name string, revision svn.Revnum) (delta.FileEditor, error) {
	node := directory.node.Children[path.Base(clean(name))]
	if node == nil || node.Kind != svn.NodeFile {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSNotFound, name)
	}
	if revision.IsValid() && node.CreatedRev > revision {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSOutOfDate, name)
	}
	return &commitFile{editor: directory.editor, node: node, path: name, base: append([]byte(nil), node.Content...)}, nil
}

func (*commitDir) AbsentFile(context.Context, string) error { return nil }
func (*commitDir) Close(context.Context) error              { return nil }

func (file *commitFile) ApplyTextDelta(_ context.Context, checksum *svn.Checksum) (delta.WindowHandler, error) {
	if checksum != nil && !svn.Sum(checksum.Kind, file.base).Equal(*checksum) {
		return nil, fmt.Errorf("%w: base checksum", svn.ErrChecksumMismatch)
	}
	file.delta = true
	file.target.Reset()
	return &commitWindows{file: file}, nil
}

func (file *commitFile) ChangeProp(_ context.Context, name string, value []byte) error {
	changeNodeProp(file.node, name, value)
	file.editor.touch(file.node)
	file.editor.change(file.path, svn.LogModified, svn.NodeFile, nil)
	return nil
}

func (file *commitFile) Close(_ context.Context, checksum *svn.Checksum) error {
	if file.delta {
		file.node.Content = append([]byte(nil), file.target.Bytes()...)
	}
	if checksum != nil && !svn.Sum(checksum.Kind, file.node.Content).Equal(*checksum) {
		return fmt.Errorf("%w: result checksum", svn.ErrChecksumMismatch)
	}
	if file.delta {
		file.editor.touch(file.node)
		file.editor.change(file.path, svn.LogModified, svn.NodeFile, nil)
	}
	return nil
}

func (windows *commitWindows) Window(window *delta.Window) error {
	if windows.closed {
		return fmt.Errorf("text delta is closed")
	}
	if window == nil {
		return windows.Close()
	}
	start := int(window.SourceOffset)
	if start < 0 || start+window.SourceLength > len(windows.file.base) {
		return fmt.Errorf("%w: source view", svn.ErrSvndiffInvalidOps)
	}
	content, err := delta.ApplyWindow(windows.file.base[start:start+window.SourceLength], *window)
	if err != nil {
		return err
	}
	_, err = windows.file.target.Write(content)
	return err
}

func (windows *commitWindows) Close() error {
	if windows.closed {
		return fmt.Errorf("text delta is already closed")
	}
	windows.closed = true
	return nil
}

func (editor *commitEditor) copyNode(source *delta.CopySource, kind svn.NodeKind) (*Node, error) {
	if source == nil {
		if kind == svn.NodeDir {
			return Directory(), nil
		}
		return File(nil), nil
	}
	name := source.Path
	if relative, ok := relativeURL(editor.session.repository.rootURL, name); ok {
		name = relative
	}
	revision, err := editor.session.revision(source.Rev)
	if err != nil {
		return nil, err
	}
	node := cloneNode(findNode(revision.Root, clean(name)))
	if node == nil || node.Kind != kind {
		return nil, fmt.Errorf("%w: copy source %s", svn.ErrFSNotFound, source.Path)
	}
	editor.touch(node)
	return node, nil
}

func (editor *commitEditor) change(name string, action svn.LogChangeAction, kind svn.NodeKind, source *delta.CopySource) {
	changed := svn.ChangedPath{Path: "/" + join(editor.session.base, name), Action: action, NodeKind: kind, CopyfromRev: svn.InvalidRevnum}
	if source != nil {
		changed.CopyfromPath = source.Path
		changed.CopyfromRev = source.Rev
	}
	for index := range editor.changes {
		if editor.changes[index].Path == changed.Path {
			if editor.changes[index].Action == svn.LogDeleted && action == svn.LogAdded {
				changed.Action = svn.LogReplaced
			} else if action == svn.LogModified && (editor.changes[index].Action == svn.LogAdded || editor.changes[index].Action == svn.LogReplaced) {
				return
			}
			editor.changes[index] = changed
			return
		}
	}
	editor.changes = append(editor.changes, changed)
}

func (*commitEditor) touch(node *Node) {
	node.CreatedRev = svn.InvalidRevnum
	node.Time = time.Time{}
	node.Author = ""
}

func changedPathSet(changes []svn.ChangedPath) map[string]struct{} {
	result := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		result[change.Path] = struct{}{}
	}
	return result
}

func changeNodeProp(node *Node, name string, value []byte) {
	if value == nil {
		delete(node.Props, name)
	} else {
		node.Props[name] = append([]byte(nil), value...)
	}
}
