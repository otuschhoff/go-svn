package fsfs

import (
	"context"
	"fmt"
	"path"
	"strings"

	fsapi "github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/svn"
)

func (transaction *Transaction) mergeYoungest(ctx context.Context, youngest svn.Revnum) error {
	for revision := transaction.base + 1; revision <= youngest; revision++ {
		rootValue, err := transaction.filesystem.RevisionRoot(ctx, revision)
		if err != nil {
			return err
		}
		upstream, err := rootValue.PathsChanged(ctx)
		if err != nil {
			return err
		}
		for localPath, localChange := range transaction.changes {
			for upstreamPath, upstreamChange := range upstream {
				if changesConflict(localPath, localChange, upstreamPath, upstreamChange) {
					return fmt.Errorf("%w: conflict at %s", svn.ErrFSConflict, conflictPath(localPath, upstreamPath))
				}
			}
		}
	}
	rootValue, err := transaction.filesystem.RevisionRoot(ctx, youngest)
	if err != nil {
		return err
	}
	latest := rootValue.(*Root)
	if err := mergeTxnDirectory(ctx, transaction.root.node, latest, "/"); err != nil {
		return err
	}
	transaction.base = youngest
	return nil
}

func changesConflict(localPath string, local fsapi.PathChange, upstreamPath string, upstream fsapi.PathChange) bool {
	localPath, upstreamPath = path.Clean(localPath), path.Clean(upstreamPath)
	if localPath == upstreamPath {
		if local.Kind != fsapi.ChangeModify || upstream.Kind != fsapi.ChangeModify {
			return true
		}
		return local.TextModified && upstream.TextModified || local.PropsModified && upstream.PropsModified
	}
	if isPathAncestor(upstreamPath, localPath) {
		return upstream.Kind == fsapi.ChangeDelete || upstream.Kind == fsapi.ChangeReplace
	}
	if isPathAncestor(localPath, upstreamPath) {
		return local.Kind == fsapi.ChangeAdd || local.Kind == fsapi.ChangeDelete || local.Kind == fsapi.ChangeReplace
	}
	return false
}

func isPathAncestor(parent, child string) bool {
	return parent == "/" || strings.HasPrefix(child, strings.TrimSuffix(parent, "/")+"/")
}

func conflictPath(left, right string) string {
	if len(left) <= len(right) {
		return left
	}
	return right
}

func mergeTxnDirectory(ctx context.Context, node *txnNode, latest *Root, nodePath string) error {
	latestNode, err := latest.nodeAt(ctx, nodePath)
	if err != nil {
		return err
	}
	if node.kind != latestNode.Kind {
		return fmt.Errorf("%w: node kind changed at %s", svn.ErrFSConflict, nodePath)
	}
	if node.kind != svn.NodeDir {
		if !node.newNode {
			node.base, node.baseNode, node.basePath = latest, latestNode, nodePath
		}
		return nil
	}
	if err := node.loadChildren(ctx); err != nil {
		return err
	}
	entries, err := latest.directoryEntries(ctx, latestNode)
	if err != nil {
		return err
	}
	latestChildren := make(map[string]NodeRevision, len(entries))
	for name, entry := range entries {
		child, err := latest.filesystem.readNode(entry.id)
		if err != nil {
			return err
		}
		latestChildren[name] = child
	}
	for name, child := range node.children {
		childPath := path.Join(nodePath, name)
		current, exists := latestChildren[name]
		if !child.dirty {
			if exists {
				node.children[name] = newTxnNode(latest, childPath, current)
			} else {
				delete(node.children, name)
			}
			continue
		}
		if exists && !child.newNode {
			if err := mergeTxnDirectory(ctx, child, latest, childPath); err != nil {
				return err
			}
		}
		delete(latestChildren, name)
	}
	for name, child := range latestChildren {
		childPath := path.Join(nodePath, name)
		node.children[name] = newTxnNode(latest, childPath, child)
	}
	if !node.newNode {
		node.base, node.baseNode, node.basePath = latest, latestNode, nodePath
	}
	return nil
}
