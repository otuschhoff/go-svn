package wc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

type CommitOptions struct {
	RevisionProperties svn.Props
	Depth              svn.Depth
	Changelists        []string
	KeepChangelists    bool
	KeepLocks          bool
	IncludeExternals   bool
}

type commitNode struct {
	path       string
	info       *Info
	status     *Status
	base       []byte
	working    []byte
	properties svn.Props
}

func (database *Database) Commit(ctx context.Context, session ra.Session, targetPath string, options CommitOptions) (*ra.CommitInfo, error) {
	if session == nil {
		return nil, fmt.Errorf("%w: nil RA session", svn.ErrIncorrectParams)
	}
	if options.Depth == svn.DepthUnknown {
		options.Depth = svn.DepthInfinity
	}
	targetRelpath, err := database.localRelpath(targetPath)
	if err != nil {
		return nil, err
	}
	nodes, rootNode, err := database.harvestCommit(ctx, targetPath, targetRelpath, options)
	if err != nil {
		return nil, err
	}
	if len(nodes) == 0 && rootNode == nil {
		return nil, fmt.Errorf("%w: no changes to commit", svn.ErrClientNoVersionedParent)
	}
	lockTokens := make(map[string]string)
	for _, node := range nodes {
		if node.info.Lock != nil {
			lockTokens[node.path] = node.info.Lock.Token
		}
	}
	var committed *ra.CommitInfo
	editor, err := session.GetCommitEditor(ctx, options.RevisionProperties, lockTokens, options.KeepLocks, func(info *ra.CommitInfo) error {
		copy := *info
		committed = &copy
		return nil
	})
	if err != nil {
		return nil, err
	}
	baseInfo, err := database.Info(ctx, database.wcRoot)
	if err != nil {
		return nil, err
	}
	root, err := editor.OpenRoot(ctx, baseInfo.Revision)
	if err != nil {
		return nil, err
	}
	if rootNode != nil {
		err = sendPropertyChanges(ctx, root.ChangeProp, rootNode.info.BaseProperties, rootNode.properties)
	}
	if err == nil {
		directoryRevisions := map[string]svn.Revnum{"": baseInfo.Revision}
		for relpath := range nodes {
			for parent := path.Dir(relpath); parent != "."; parent = path.Dir(parent) {
				if _, exists := directoryRevisions[parent]; exists {
					continue
				}
				info, infoErr := database.Info(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(parent)))
				if infoErr != nil {
					err = infoErr
					break
				}
				directoryRevisions[parent] = info.Revision
			}
			if err != nil {
				break
			}
		}
		if err == nil {
			err = delta.PathDriverRevisions(ctx, root, commitPaths(nodes), func(relpath string) svn.Revnum {
				return directoryRevisions[relpath]
			}, func(ctx context.Context, parent delta.DirEditor, relpath string) (delta.DirEditor, error) {
				return database.driveCommitNode(ctx, parent, relpath, nodes)
			})
		}
	}
	if err == nil {
		err = root.Close(ctx)
	}
	if err == nil {
		err = editor.CloseEdit(ctx)
	} else {
		_ = editor.AbortEdit(ctx)
	}
	if err != nil {
		return nil, err
	}
	if committed == nil {
		return nil, fmt.Errorf("commit completed without revision information")
	}
	if err := database.preparePostCommit(ctx, targetRelpath, nodes, rootNode, options); err != nil {
		return committed, err
	}
	if _, err := database.Update(ctx, session, targetPath, committed.Revision, UpdateOptions{Depth: options.Depth, Force: true, Accept: ConflictTheirs}); err != nil {
		return committed, err
	}
	if err := database.finalizeCommittedMetadata(ctx, nodes, rootNode, committed); err != nil {
		return committed, err
	}
	for _, node := range nodes {
		if node.status.NodeStatus == StatusDeleted || node.info.Kind == svn.NodeDir {
			continue
		}
		if _, err := database.Enqueue(ctx, FileInstallWork(node.info.RelativePath, false, true, "")); err != nil {
			return committed, err
		}
	}
	if err := database.RunWorkQueue(ctx); err != nil {
		return committed, err
	}
	return committed, nil
}

func (database *Database) driveCommitNode(ctx context.Context, parent delta.DirEditor, relpath string, nodes map[string]*commitNode) (delta.DirEditor, error) {
	node := nodes[relpath]
	if node.status.NodeStatus == StatusDeleted {
		return nil, parent.DeleteEntry(ctx, relpath, node.info.Revision)
	}
	added := node.status.NodeStatus == StatusAdded || node.status.NodeStatus == StatusReplaced
	if node.status.NodeStatus == StatusReplaced {
		if err := parent.DeleteEntry(ctx, relpath, node.info.Revision); err != nil {
			return nil, err
		}
	}
	copiedAncestor := copiedDirectoryAncestor(nodes, relpath)
	copySource := commitCopySource(database, node.info)
	if node.info.Kind == svn.NodeDir {
		var directory delta.DirEditor
		var err error
		if added && !copiedAncestor {
			directory, err = parent.AddDirectory(ctx, relpath, copySource)
		} else {
			directory, err = parent.OpenDirectory(ctx, relpath, node.info.Revision)
		}
		if err == nil {
			err = sendPropertyChanges(ctx, directory.ChangeProp, node.info.BaseProperties, node.properties)
		}
		return directory, err
	}
	var file delta.FileEditor
	var err error
	if added && !copiedAncestor {
		file, err = parent.AddFile(ctx, relpath, copySource)
	} else {
		file, err = parent.OpenFile(ctx, relpath, node.info.Revision)
	}
	if err != nil {
		return nil, err
	}
	if err := sendPropertyChanges(ctx, file.ChangeProp, node.info.BaseProperties, node.properties); err != nil {
		return nil, err
	}
	if node.status.TextStatus == StatusModified || (added && copySource == nil) {
		if err := sendTextDelta(ctx, file, node.base, node.working, !added || copySource != nil); err != nil {
			return nil, err
		}
	} else if err := file.Close(ctx, nil); err != nil {
		return nil, err
	}
	return nil, nil
}

func (database *Database) harvestCommit(ctx context.Context, targetPath, targetRelpath string, options CommitOptions) (map[string]*commitNode, *commitNode, error) {
	selected := make(map[string]bool)
	for _, changelist := range options.Changelists {
		selected[changelist] = true
	}
	nodes := make(map[string]*commitNode)
	var rootNode *commitNode
	err := database.Diff(ctx, targetPath, options.Depth, func(_ context.Context, difference *Difference) error {
		if len(selected) != 0 && !selected[difference.Status.Changelist] {
			return nil
		}
		info, err := database.Info(ctx, difference.Path)
		if err != nil {
			return err
		}
		if info.Conflict != nil {
			return fmt.Errorf("%w: %s", svn.ErrWCFoundConflict, difference.Path)
		}
		relpath, ok := relativeCommitPath(targetRelpath, difference.RelativePath)
		if !ok {
			return nil
		}
		node := &commitNode{path: relpath, info: info, status: difference.Status, properties: difference.WorkingProperties.Clone()}
		if difference.Base != nil {
			node.base, err = io.ReadAll(difference.Base)
		}
		if err == nil && difference.Working != nil {
			node.working, err = canonicalWorking(info, difference.Working)
		}
		if err != nil {
			return err
		}
		if relpath == "" {
			rootNode = node
		} else {
			nodes[relpath] = node
		}
		return nil
	})
	return nodes, rootNode, err
}

func canonicalWorking(info *Info, source io.Reader) ([]byte, error) {
	if len(info.WorkingProperties[props.Special]) != 0 {
		return io.ReadAll(source)
	}
	keywords := props.ParseKeywords(string(info.WorkingProperties[props.Keywords]), props.KeywordContext{
		Author: info.ChangedAuthor, Basename: filepath.Base(info.Path), Date: info.ChangedDate, Path: info.RepositoryPath,
		Revision: info.ChangedRevision, RootURL: info.RepositoryRoot, URL: info.URL,
	})
	var output bytes.Buffer
	err := props.DetranslateFile(source, &output, string(info.WorkingProperties[props.EOLStyle]), keywords, false)
	return output.Bytes(), err
}

func relativeCommitPath(target, relpath string) (string, bool) {
	if target == "" {
		return relpath, true
	}
	if relpath == target {
		return relpath, true
	}
	prefix := target + "/"
	if len(relpath) > len(prefix) && relpath[:len(prefix)] == prefix {
		return relpath, true
	}
	return "", false
}

func commitPaths(nodes map[string]*commitNode) []string {
	paths := make([]string, 0, len(nodes))
	for relpath := range nodes {
		if !deletedAncestor(nodes, relpath) {
			paths = append(paths, relpath)
		}
	}
	sort.Strings(paths)
	return paths
}

func deletedAncestor(nodes map[string]*commitNode, relpath string) bool {
	for parent := path.Dir(relpath); parent != "."; parent = path.Dir(parent) {
		if node := nodes[parent]; node != nil && node.status.NodeStatus == StatusDeleted {
			return true
		}
	}
	return false
}

func copiedDirectoryAncestor(nodes map[string]*commitNode, relpath string) bool {
	for parent := path.Dir(relpath); parent != "."; parent = path.Dir(parent) {
		if node := nodes[parent]; node != nil && node.info.Kind == svn.NodeDir && node.info.Copied {
			return true
		}
	}
	return false
}

func commitCopySource(database *Database, info *Info) *delta.CopySource {
	if !info.Copied || info.CopyFromPath == "" || !info.CopyFromRevision.IsValid() {
		return nil
	}
	return &delta.CopySource{Path: database.repository.Root + "/" + info.CopyFromPath, Rev: info.CopyFromRevision}
}

func sendPropertyChanges(ctx context.Context, change func(context.Context, string, []byte) error, base, working svn.Props) error {
	names := make(map[string]bool)
	for name := range base {
		names[name] = true
	}
	for name := range working {
		names[name] = true
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		if bytes.Equal(base[name], working[name]) {
			continue
		}
		if err := change(ctx, name, working[name]); err != nil {
			return err
		}
	}
	return nil
}

func sendTextDelta(ctx context.Context, file delta.FileEditor, base, working []byte, hasBase bool) error {
	var baseChecksum *svn.Checksum
	deltaBase := base
	if hasBase {
		checksum := svn.Sum(svn.ChecksumMD5, base)
		baseChecksum = &checksum
	} else {
		deltaBase = nil
	}
	handler, err := file.ApplyTextDelta(ctx, baseChecksum)
	if err != nil {
		return err
	}
	stream := delta.NewTxDeltaStream(bytes.NewReader(deltaBase), bytes.NewReader(working))
	for {
		window, err := stream.NextWindow()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if err := handler.Window(window); err != nil {
			return err
		}
	}
	if err := handler.Close(); err != nil {
		return err
	}
	result := svn.Sum(svn.ChecksumMD5, working)
	return file.Close(ctx, &result)
}

func (database *Database) preparePostCommit(ctx context.Context, targetRelpath string, nodes map[string]*commitNode, rootNode *commitNode, options CommitOptions) error {
	transaction, err := database.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for _, node := range nodes {
		relpath := node.info.RelativePath
		if node.status.NodeStatus == StatusDeleted {
			if _, err := transaction.ExecContext(ctx, `DELETE FROM NODES WHERE wc_id=? AND (local_relpath=? OR local_relpath LIKE ? ESCAPE '#')`, database.wcID, relpath, descendantPattern(relpath)); err != nil {
				return err
			}
		} else if _, err := transaction.ExecContext(ctx, `DELETE FROM NODES WHERE wc_id=? AND local_relpath=? AND op_depth>0`, database.wcID, relpath); err != nil {
			return err
		}
		if options.KeepChangelists && node.info.Changelist != "" {
			if _, err := transaction.ExecContext(ctx, `UPDATE ACTUAL_NODE SET properties=NULL, conflict_data=NULL WHERE wc_id=? AND local_relpath=?`, database.wcID, relpath); err != nil {
				return err
			}
		} else if node.status.NodeStatus == StatusDeleted {
			if _, err := transaction.ExecContext(ctx, `DELETE FROM ACTUAL_NODE WHERE wc_id=? AND (local_relpath=? OR local_relpath LIKE ? ESCAPE '#')`, database.wcID, relpath, descendantPattern(relpath)); err != nil {
				return err
			}
		} else if _, err := transaction.ExecContext(ctx, `DELETE FROM ACTUAL_NODE WHERE wc_id=? AND local_relpath=?`, database.wcID, relpath); err != nil {
			return err
		}
		if !options.KeepLocks && node.info.Lock != nil {
			if _, err := transaction.ExecContext(ctx, `DELETE FROM LOCK WHERE repos_id=? AND repos_relpath=?`, database.repository.ID, node.info.RepositoryPath); err != nil {
				return err
			}
		}
	}
	if rootNode != nil {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM ACTUAL_NODE WHERE wc_id=? AND local_relpath=?`, database.wcID, rootNode.info.RelativePath); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (database *Database) finalizeCommittedMetadata(ctx context.Context, nodes map[string]*commitNode, rootNode *commitNode, committed *ra.CommitInfo) error {
	checksums := make(map[string]svn.Checksum)
	for _, node := range nodes {
		if node.info.Kind != svn.NodeFile || node.status.NodeStatus == StatusDeleted || node.working == nil {
			continue
		}
		temporary, err := os.CreateTemp("", "go-svn-committed-pristine-*")
		if err != nil {
			return err
		}
		name := temporary.Name()
		if _, err = temporary.Write(node.working); err == nil {
			err = temporary.Close()
		} else {
			_ = temporary.Close()
		}
		if err == nil {
			sha1Checksum := svn.Sum(svn.ChecksumSHA1, node.working)
			md5Checksum := svn.Sum(svn.ChecksumMD5, node.working)
			err = database.installPristine(ctx, name, sha1Checksum, md5Checksum, int64(len(node.working)))
			checksums[node.info.RelativePath] = sha1Checksum
		}
		_ = os.Remove(name)
		if err != nil {
			return err
		}
	}
	transaction, err := database.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	var committedDate int64
	if !committed.Date.IsZero() {
		committedDate = committed.Date.UnixMicro()
	}
	update := func(relpath string) error {
		_, err := transaction.ExecContext(ctx, `UPDATE NODES SET revision=?, changed_revision=?, changed_date=NULLIF(?, 0), changed_author=NULLIF(?, '') WHERE wc_id=? AND local_relpath=? AND op_depth=0`,
			committed.Revision, committed.Revision, committedDate, committed.Author, database.wcID, relpath)
		return err
	}
	for _, node := range nodes {
		if node.status.NodeStatus != StatusDeleted {
			if err := update(node.info.RelativePath); err != nil {
				return err
			}
			if checksum, ok := checksums[node.info.RelativePath]; ok {
				if _, err := transaction.ExecContext(ctx, `UPDATE NODES SET checksum=?, translated_size=NULL, last_mod_time=NULL WHERE wc_id=? AND local_relpath=? AND op_depth=0`,
					checksum.Serialize(), database.wcID, node.info.RelativePath); err != nil {
					return err
				}
			}
		}
	}
	if rootNode != nil {
		if err := update(rootNode.info.RelativePath); err != nil {
			return err
		}
	}
	return transaction.Commit()
}
