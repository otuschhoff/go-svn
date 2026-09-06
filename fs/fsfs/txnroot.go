package fsfs

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/delta"
	fsapi "github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/svn"
)

type TxnRoot struct {
	transaction *Transaction
	node        *txnNode
}

type txnNode struct {
	base          *Root
	baseNode      NodeRevision
	basePath      string
	kind          svn.NodeKind
	createdPath   string
	children      map[string]*txnNode
	childrenReady bool
	properties    svn.Props
	propsReady    bool
	content       []byte
	contentReady  bool
	dirty         bool
	textModified  bool
	propsModified bool
	newNode       bool
	copyFromPath  string
	copyFromRev   svn.Revnum
	temporaryID   string
}

func newTxnNode(root *Root, nodePath string, node NodeRevision) *txnNode {
	return &txnNode{base: root, baseNode: node, basePath: nodePath, kind: node.Kind, createdPath: nodePath, copyFromRev: svn.InvalidRevnum}
}

func (root *TxnRoot) CheckPath(ctx context.Context, nodePath string) (svn.NodeKind, error) {
	root.transaction.mu.Lock()
	defer root.transaction.mu.Unlock()
	node, _, err := root.lookup(ctx, nodePath)
	if err != nil {
		return svn.NodeUnknown, err
	}
	if node == nil {
		return svn.NodeNone, nil
	}
	return node.kind, nil
}

func (root *TxnRoot) NodeProps(ctx context.Context, nodePath string) (svn.Props, error) {
	root.transaction.mu.Lock()
	defer root.transaction.mu.Unlock()
	node, _, err := root.require(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	if err := node.loadProperties(ctx); err != nil {
		return nil, err
	}
	return node.properties.Clone(), nil
}

func (root *TxnRoot) DirEntries(ctx context.Context, nodePath string) ([]fsapi.DirEntry, error) {
	root.transaction.mu.Lock()
	defer root.transaction.mu.Unlock()
	node, _, err := root.require(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	if node.kind != svn.NodeDir {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSNotDirectory, nodePath)
	}
	if err := node.loadChildren(ctx); err != nil {
		return nil, err
	}
	entries := make([]fsapi.DirEntry, 0, len(node.children))
	for name, child := range node.children {
		entries = append(entries, fsapi.DirEntry{Name: name, Kind: child.kind})
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name < entries[right].Name })
	return entries, nil
}

func (root *TxnRoot) FileContents(ctx context.Context, nodePath string, writer io.Writer) error {
	root.transaction.mu.Lock()
	defer root.transaction.mu.Unlock()
	node, _, err := root.require(ctx, nodePath)
	if err != nil {
		return err
	}
	if node.kind != svn.NodeFile {
		return fmt.Errorf("%w: %s", svn.ErrFSNotFile, nodePath)
	}
	if err := node.loadContent(ctx); err != nil {
		return err
	}
	_, err = writer.Write(node.content)
	return err
}

func (root *TxnRoot) MakeDir(ctx context.Context, nodePath string) error {
	return root.makeNode(ctx, nodePath, svn.NodeDir)
}

func (root *TxnRoot) MakeFile(ctx context.Context, nodePath string) error {
	return root.makeNode(ctx, nodePath, svn.NodeFile)
}

func (root *TxnRoot) makeNode(ctx context.Context, nodePath string, kind svn.NodeKind) error {
	root.transaction.mu.Lock()
	defer root.transaction.mu.Unlock()
	cleaned, parentPath, name, err := splitTxnPath(nodePath)
	if err != nil {
		return err
	}
	parent, ancestors, err := root.require(ctx, parentPath)
	if err != nil {
		return err
	}
	if parent.kind != svn.NodeDir {
		return fmt.Errorf("%w: %s", svn.ErrFSNotDirectory, parentPath)
	}
	if err := parent.loadChildren(ctx); err != nil {
		return err
	}
	if parent.children[name] != nil {
		return fmt.Errorf("%w: %s", svn.ErrFSAlreadyExists, cleaned)
	}
	temporaryID := "_" + strconv.FormatInt(root.transaction.nextNode, 36)
	root.transaction.nextNode++
	child := &txnNode{kind: kind, createdPath: cleaned, basePath: cleaned, newNode: true, dirty: true, textModified: kind == svn.NodeFile, propsReady: true, properties: make(svn.Props), copyFromRev: svn.InvalidRevnum, temporaryID: temporaryID}
	if kind == svn.NodeDir {
		child.childrenReady = true
		child.children = make(map[string]*txnNode)
	} else {
		child.contentReady = true
	}
	parent.children[name] = child
	root.markDirty(append(ancestors, parent))
	root.recordChange(cleaned, fsapi.ChangeAdd, kind, true, false, "", svn.InvalidRevnum)
	return root.persist(ctx)
}

func (root *TxnRoot) Delete(ctx context.Context, nodePath string) error {
	root.transaction.mu.Lock()
	defer root.transaction.mu.Unlock()
	cleaned, parentPath, name, err := splitTxnPath(nodePath)
	if err != nil {
		return err
	}
	parent, ancestors, err := root.require(ctx, parentPath)
	if err != nil {
		return err
	}
	if parent.kind != svn.NodeDir {
		return fmt.Errorf("%w: %s", svn.ErrFSNotDirectory, parentPath)
	}
	if err := parent.loadChildren(ctx); err != nil {
		return err
	}
	child := parent.children[name]
	if child == nil {
		return fmt.Errorf("%w: %s", svn.ErrFSNotFound, cleaned)
	}
	delete(parent.children, name)
	root.markDirty(append(ancestors, parent))
	root.recordChange(cleaned, fsapi.ChangeDelete, child.kind, false, false, "", svn.InvalidRevnum)
	return root.persist(ctx)
}

func (root *TxnRoot) Copy(ctx context.Context, sourceRevision svn.Revnum, sourcePath, destinationPath string) error {
	root.transaction.mu.Lock()
	defer root.transaction.mu.Unlock()
	cleaned, parentPath, name, err := splitTxnPath(destinationPath)
	if err != nil {
		return err
	}
	parent, ancestors, err := root.require(ctx, parentPath)
	if err != nil {
		return err
	}
	if parent.kind != svn.NodeDir {
		return fmt.Errorf("%w: %s", svn.ErrFSNotDirectory, parentPath)
	}
	if err := parent.loadChildren(ctx); err != nil {
		return err
	}
	if parent.children[name] != nil {
		return fmt.Errorf("%w: %s", svn.ErrFSAlreadyExists, cleaned)
	}
	sourceValue, err := root.transaction.filesystem.RevisionRoot(ctx, sourceRevision)
	if err != nil {
		return err
	}
	sourceRoot := sourceValue.(*Root)
	sourceNode, err := sourceRoot.nodeAt(ctx, sourcePath)
	if err != nil {
		return err
	}
	copyNode := newTxnNode(sourceRoot, sourceNode.CreatedPath, sourceNode)
	copyNode.createdPath = cleaned
	copyNode.copyFromPath = sourceNode.CreatedPath
	copyNode.copyFromRev = sourceRevision
	copyNode.temporaryID = "_" + strconv.FormatInt(root.transaction.nextNode, 36)
	root.transaction.nextNode++
	copyNode.dirty = true
	parent.children[name] = copyNode
	root.markDirty(append(ancestors, parent))
	root.recordChange(cleaned, fsapi.ChangeAdd, copyNode.kind, false, false, sourceNode.CreatedPath, sourceRevision)
	return root.persist(ctx)
}

func (root *TxnRoot) ChangeNodeProp(ctx context.Context, nodePath, name string, value []byte) error {
	if name == "" {
		return fmt.Errorf("%w: empty node property name", svn.ErrIncorrectParams)
	}
	root.transaction.mu.Lock()
	defer root.transaction.mu.Unlock()
	node, ancestors, err := root.require(ctx, nodePath)
	if err != nil {
		return err
	}
	if err := node.loadProperties(ctx); err != nil {
		return err
	}
	if value == nil {
		delete(node.properties, name)
	} else {
		node.properties[name] = append([]byte(nil), value...)
	}
	node.propsModified = true
	root.markDirty(append(ancestors, node))
	root.recordChange(node.createdPath, fsapi.ChangeModify, node.kind, false, true, "", svn.InvalidRevnum)
	return root.persist(ctx)
}

func (root *TxnRoot) ApplyText(ctx context.Context, nodePath string, reader io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	root.transaction.mu.Lock()
	defer root.transaction.mu.Unlock()
	node, ancestors, err := root.require(ctx, nodePath)
	if err != nil {
		return err
	}
	if node.kind != svn.NodeFile {
		return fmt.Errorf("%w: %s", svn.ErrFSNotFile, nodePath)
	}
	node.content = data
	node.contentReady = true
	node.textModified = true
	root.markDirty(append(ancestors, node))
	root.recordChange(node.createdPath, fsapi.ChangeModify, svn.NodeFile, true, false, "", svn.InvalidRevnum)
	return root.persist(ctx)
}

func (root *TxnRoot) ApplyTextDelta(ctx context.Context, nodePath string, checksum *svn.Checksum) (delta.WindowHandler, error) {
	root.transaction.mu.Lock()
	defer root.transaction.mu.Unlock()
	node, ancestors, err := root.require(ctx, nodePath)
	if err != nil {
		return nil, err
	}
	if node.kind != svn.NodeFile {
		return nil, fmt.Errorf("%w: %s", svn.ErrFSNotFile, nodePath)
	}
	if err := node.loadContent(ctx); err != nil {
		return nil, err
	}
	if checksum != nil && !svn.Sum(checksum.Kind, node.content).Equal(*checksum) {
		return nil, fmt.Errorf("%w: base checksum", svn.ErrChecksumMismatch)
	}
	return &txnWindowHandler{root: root, node: node, ancestors: ancestors, base: append([]byte(nil), node.content...)}, nil
}

type txnWindowHandler struct {
	root      *TxnRoot
	node      *txnNode
	ancestors []*txnNode
	base      []byte
	target    bytes.Buffer
	closed    bool
}

func (handler *txnWindowHandler) Window(window *delta.Window) error {
	if handler.closed {
		return fmt.Errorf("text delta is closed")
	}
	if window == nil {
		return handler.Close()
	}
	start := int(window.SourceOffset)
	if start < 0 || start+window.SourceLength > len(handler.base) {
		return fmt.Errorf("%w: source view", svn.ErrSvndiffInvalidOps)
	}
	content, err := delta.ApplyWindow(handler.base[start:start+window.SourceLength], *window)
	if err != nil {
		return err
	}
	_, err = handler.target.Write(content)
	return err
}

func (handler *txnWindowHandler) Close() error {
	if handler.closed {
		return fmt.Errorf("text delta is already closed")
	}
	handler.closed = true
	handler.root.transaction.mu.Lock()
	defer handler.root.transaction.mu.Unlock()
	handler.node.content = append([]byte(nil), handler.target.Bytes()...)
	handler.node.contentReady = true
	handler.node.textModified = true
	handler.root.markDirty(append(handler.ancestors, handler.node))
	handler.root.recordChange(handler.node.createdPath, fsapi.ChangeModify, svn.NodeFile, true, false, "", svn.InvalidRevnum)
	return handler.root.persist(context.Background())
}

func (root *TxnRoot) lookup(ctx context.Context, nodePath string) (*txnNode, []*txnNode, error) {
	cleaned, err := cleanPath(nodePath)
	if err != nil {
		return nil, nil, err
	}
	current := root.node
	if cleaned == "/" {
		return current, nil, nil
	}
	ancestors := make([]*txnNode, 0)
	for _, name := range strings.Split(strings.TrimPrefix(cleaned, "/"), "/") {
		if current.kind != svn.NodeDir {
			return nil, nil, fmt.Errorf("%w: %s", svn.ErrFSNotDirectory, current.createdPath)
		}
		if err := current.loadChildren(ctx); err != nil {
			return nil, nil, err
		}
		ancestors = append(ancestors, current)
		current = current.children[name]
		if current == nil {
			return nil, ancestors, nil
		}
	}
	return current, ancestors, nil
}

func (root *TxnRoot) require(ctx context.Context, nodePath string) (*txnNode, []*txnNode, error) {
	node, ancestors, err := root.lookup(ctx, nodePath)
	if err == nil && node == nil {
		err = fmt.Errorf("%w: %s", svn.ErrFSNotFound, nodePath)
	}
	return node, ancestors, err
}

func (node *txnNode) loadChildren(ctx context.Context) error {
	if node.childrenReady {
		return nil
	}
	if node.kind != svn.NodeDir {
		return fmt.Errorf("%w: %s", svn.ErrFSNotDirectory, node.createdPath)
	}
	entries, err := node.base.directoryEntries(ctx, node.baseNode)
	if err != nil {
		return err
	}
	node.children = make(map[string]*txnNode, len(entries))
	for name, entry := range entries {
		child, err := node.base.filesystem.readNode(entry.id)
		if err != nil {
			return err
		}
		childNode := newTxnNode(node.base, path.Join(node.basePath, name), child)
		childNode.createdPath = path.Join(node.createdPath, name)
		node.children[name] = childNode
	}
	node.childrenReady = true
	return nil
}

func (node *txnNode) loadProperties(ctx context.Context) error {
	if node.propsReady {
		return nil
	}
	properties, err := node.base.NodeProps(ctx, node.basePath)
	if err != nil {
		return err
	}
	node.properties = properties
	node.propsReady = true
	return nil
}

func (node *txnNode) loadContent(ctx context.Context) error {
	if node.contentReady {
		return nil
	}
	var content bytes.Buffer
	if err := node.base.FileContents(ctx, node.basePath, &content); err != nil {
		return err
	}
	node.content = content.Bytes()
	node.contentReady = true
	return nil
}

func (root *TxnRoot) markDirty(nodes []*txnNode) {
	for _, node := range nodes {
		node.dirty = true
	}
}

func (root *TxnRoot) recordChange(nodePath string, kind fsapi.ChangeKind, nodeKind svn.NodeKind, text, properties bool, copyPath string, copyRevision svn.Revnum) {
	change := fsapi.PathChange{Path: nodePath, Kind: kind, NodeKind: nodeKind, TextModified: text, PropsModified: properties, CopyFromPath: copyPath, CopyFromRev: copyRevision}
	if previous, found := root.transaction.changes[nodePath]; found {
		if previous.Kind == fsapi.ChangeDelete && kind == fsapi.ChangeAdd {
			change.Kind = fsapi.ChangeReplace
		} else if previous.Kind == fsapi.ChangeAdd || previous.Kind == fsapi.ChangeReplace {
			change.Kind = previous.Kind
			change.TextModified = change.TextModified || previous.TextModified
			change.PropsModified = change.PropsModified || previous.PropsModified
			if change.CopyFromPath == "" {
				change.CopyFromPath, change.CopyFromRev = previous.CopyFromPath, previous.CopyFromRev
			}
		}
	}
	root.transaction.changes[nodePath] = change
}

func splitTxnPath(value string) (string, string, string, error) {
	cleaned, err := cleanPath(value)
	if err != nil {
		return "", "", "", err
	}
	if cleaned == "/" {
		return "", "", "", fmt.Errorf("%w: cannot replace transaction root", svn.ErrIncorrectParams)
	}
	return cleaned, path.Dir(cleaned), path.Base(cleaned), nil
}
