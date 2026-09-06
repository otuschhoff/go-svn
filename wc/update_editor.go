package wc

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/internal/diff3"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/notify"
	"github.com/otuschhoff/go-svn/svn/skel"
)

type UpdateOptions struct {
	Depth           svn.Depth
	SetDepth        *svn.Depth
	Parents         bool
	IgnoreExternals bool
	Force           bool
	Accept          ConflictChoice
	SwitchURL       string
	switchTarget    string
	UseCommitTimes  bool
	Notify          notify.Func
}

type ConflictChoice uint8

const (
	ConflictPostpone ConflictChoice = iota
	ConflictWorking
	ConflictBase
	ConflictMine
	ConflictTheirs
)

type updateEditor struct {
	database         *Database
	anchor           string
	repositoryBase   string
	repositoryTarget string
	options          UpdateOptions
	target           svn.Revnum
	aborted          bool
	deleted          map[string]bool
	copySources      map[string]nodeRow
}

type updateDirectory struct {
	editor       *updateEditor
	path         string
	added        bool
	props        svn.Props
	baseProps    svn.Props
	workingProps svn.Props
	depth        svn.Depth
	localDeleted bool
	localAdded   bool
	conflicted   bool
	obstructed   bool
	shadowed     bool
}

type updateFile struct {
	editor        *updateEditor
	path          string
	added         bool
	props         svn.Props
	baseProps     svn.Props
	workingProps  svn.Props
	baseChecksum  *svn.Checksum
	baseRevision  svn.Revnum
	temporary     *os.File
	source        *os.File
	localModified bool
	localDeleted  bool
	localAdded    bool
	obstructed    bool
	conflicted    bool
	shadowed      bool
}

type updateWindows struct {
	file   *updateFile
	closed bool
}

func NewUpdateEditor(ctx context.Context, database *Database, anchorPath string, options UpdateOptions) (delta.Editor, error) {
	if database == nil || !database.writable {
		return nil, fmt.Errorf("%w: writable working copy required", svn.ErrWCNotLocked)
	}
	anchor, err := database.localRelpath(anchorPath)
	if err != nil {
		return nil, err
	}
	if options.Depth == svn.DepthUnknown {
		options.Depth = svn.DepthInfinity
	}
	repositoryBase := database.repositoryPathForAnchor(anchor)
	if options.SwitchURL != "" {
		repositoryBase = sessionPath(database.repository.Root, options.SwitchURL)
	}
	return &updateEditor{database: database, anchor: anchor, repositoryBase: repositoryBase, repositoryTarget: options.switchTarget, options: options, target: svn.InvalidRevnum, deleted: make(map[string]bool), copySources: make(map[string]nodeRow)}, nil
}

func (editor *updateEditor) SetTargetRevision(_ context.Context, revision svn.Revnum) error {
	editor.target = revision
	return nil
}

func (editor *updateEditor) OpenRoot(ctx context.Context, _ svn.Revnum) (delta.DirEditor, error) {
	info, err := editor.database.Info(ctx, editor.localPath(""))
	if err != nil {
		return nil, err
	}
	depth := info.Depth
	if editor.options.SetDepth != nil {
		depth = *editor.options.SetDepth
	}
	return &updateDirectory{editor: editor, path: "", props: info.BaseProperties.Clone(), baseProps: info.BaseProperties.Clone(), workingProps: info.WorkingProperties.Clone(), depth: depth}, nil
}

func (editor *updateEditor) CloseEdit(ctx context.Context) error {
	if editor.aborted {
		return fmt.Errorf("%w: update was aborted", svn.ErrCancelled)
	}
	if err := editor.database.RunWorkQueue(ctx); err != nil {
		return err
	}
	if editor.options.Notify != nil {
		editor.options.Notify(notify.Notify{Action: notify.ActionUpdateCompleted, Path: editor.localPath(""), Revision: editor.target})
	}
	return nil
}

func (editor *updateEditor) AbortEdit(context.Context) error { editor.aborted = true; return nil }

func (editor *updateEditor) relpath(editorPath string) string {
	return path.Join(editor.anchor, editorPath)
}
func (editor *updateEditor) localPath(editorPath string) string {
	return filepath.Join(editor.database.wcRoot, filepath.FromSlash(editor.relpath(editorPath)))
}
func (editor *updateEditor) repositoryPath(editorPath string) string {
	if editor.repositoryTarget != "" && editorPath == editor.repositoryTarget {
		return editor.repositoryBase
	}
	return path.Join(editor.repositoryBase, editorPath)
}

func copySourceKey(repositoryPath string, revision svn.Revnum) string {
	return strings.TrimPrefix(repositoryPath, "/") + "@" + strconv.FormatInt(int64(revision), 10)
}

func (editor *updateEditor) rememberCopySources(ctx context.Context, relpath string) error {
	rows, err := editor.database.sql.QueryContext(ctx, `SELECT `+nodeColumns+` FROM NODES_BASE
		WHERE wc_id=? AND (local_relpath=? OR local_relpath LIKE ? ESCAPE '#')`, editor.database.wcID, relpath, descendantPattern(relpath))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row nodeRow
		if err := scanNode(rows, &row); err != nil {
			return err
		}
		editor.copySources[copySourceKey(row.reposPath, row.revision)] = row
	}
	return rows.Err()
}

func (editor *updateEditor) copySourceNode(ctx context.Context, source *delta.CopySource) (*nodeRow, error) {
	if source == nil || !source.Rev.IsValid() {
		return nil, nil
	}
	repositoryPath := source.Path
	if strings.HasPrefix(repositoryPath, editor.database.repository.Root) {
		repositoryPath = sessionPath(editor.database.repository.Root, repositoryPath)
	}
	repositoryPath = strings.TrimPrefix(repositoryPath, "/")
	if cached, ok := editor.copySources[copySourceKey(repositoryPath, source.Rev)]; ok {
		copy := cached
		return &copy, nil
	}
	row := &nodeRow{}
	err := editor.database.sql.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM NODES_BASE
		WHERE wc_id=? AND repos_path=? AND revision=? LIMIT 1`, editor.database.wcID, repositoryPath, source.Rev).Scan(
		&row.relpath, &row.opDepth, &row.reposID, &row.reposPath, &row.revision, &row.presence,
		&row.movedHere, &row.movedTo, &row.kind, &row.properties, &row.depth, &row.checksum,
		&row.symlinkTarget, &row.changedRevision, &row.changedDate, &row.changedAuthor,
		&row.translatedSize, &row.lastModified, &row.fileExternal,
	)
	if err != nil {
		return nil, err
	}
	return row, nil
}

func (editor *updateEditor) copySourceRepositoryPath(source *delta.CopySource) string {
	repositoryPath := source.Path
	if strings.HasPrefix(repositoryPath, editor.database.repository.Root) {
		repositoryPath = sessionPath(editor.database.repository.Root, repositoryPath)
	}
	return strings.TrimPrefix(repositoryPath, "/")
}

func (editor *updateEditor) materializeCopyDirectory(ctx context.Context, editorPath string, source *delta.CopySource) (*nodeRow, error) {
	root, err := editor.copySourceNode(ctx, source)
	if err != nil || root == nil || root.kind != svn.NodeDir.String() || root.depth != svn.DepthInfinity.String() {
		return nil, err
	}
	sourcePath := editor.copySourceRepositoryPath(source)
	byPath := make(map[string]nodeRow)
	rows, err := editor.database.sql.QueryContext(ctx, `SELECT `+nodeColumns+` FROM NODES_BASE
		WHERE wc_id=? AND (repos_path=? OR repos_path LIKE ? ESCAPE '#') ORDER BY repos_path`, editor.database.wcID, sourcePath, descendantPattern(sourcePath))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var row nodeRow
		if err := scanNode(rows, &row); err != nil {
			rows.Close()
			return nil, err
		}
		if row.revision != source.Rev {
			rows.Close()
			return nil, nil
		}
		byPath[row.reposPath] = row
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, row := range editor.copySources {
		if row.revision == source.Rev && (row.reposPath == sourcePath || strings.HasPrefix(row.reposPath, sourcePath+"/")) {
			byPath[row.reposPath] = row
		}
	}
	if len(byPath) == 0 {
		return nil, nil
	}
	paths := make([]string, 0, len(byPath))
	for repositoryPath := range byPath {
		paths = append(paths, repositoryPath)
	}
	sort.Slice(paths, func(left, right int) bool {
		leftDepth, rightDepth := strings.Count(paths[left], "/"), strings.Count(paths[right], "/")
		return leftDepth < rightDepth || leftDepth == rightDepth && paths[left] < paths[right]
	})
	for _, repositoryPath := range paths {
		row := byPath[repositoryPath]
		suffix := strings.TrimPrefix(strings.TrimPrefix(repositoryPath, sourcePath), "/")
		relpath := editor.relpath(path.Join(editorPath, suffix))
		parent := path.Dir(relpath)
		if parent == "." {
			parent = ""
		}
		presence := PresenceNormal
		if suffix == "" {
			presence = PresenceIncomplete
		}
		_, err := editor.database.sql.ExecContext(ctx, `INSERT INTO NODES
			(wc_id, local_relpath, op_depth, parent_relpath, repos_id, repos_path, revision, presence, kind, properties,
			depth, checksum, symlink_target, changed_revision, changed_date, changed_author, file_external)
			VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''),
			NULLIF(?, -1), NULLIF(?, 0), NULLIF(?, ''), ?)
			ON CONFLICT(wc_id, local_relpath, op_depth) DO UPDATE SET repos_id=excluded.repos_id, repos_path=excluded.repos_path,
			revision=excluded.revision, presence=excluded.presence, kind=excluded.kind, properties=excluded.properties,
			depth=excluded.depth, checksum=excluded.checksum, symlink_target=excluded.symlink_target,
			changed_revision=excluded.changed_revision, changed_date=excluded.changed_date, changed_author=excluded.changed_author,
			file_external=excluded.file_external`, editor.database.wcID, relpath, nullableParent(relpath, parent), editor.database.repository.ID,
			editor.repositoryPath(path.Join(editorPath, suffix)), editor.target, string(presence), row.kind, []byte(row.properties),
			row.depth, row.checksum, row.symlinkTarget, row.changedRevision, row.changedDate, row.changedAuthor, row.fileExternal)
		if err != nil {
			return nil, err
		}
		op := workFileInstall
		arguments := []string{relpath, boolString(editor.options.UseCommitTimes), "1"}
		if row.kind == svn.NodeDir.String() {
			op, arguments = workDirInstall, []string{relpath}
		}
		if _, err := editor.database.Enqueue(ctx, WorkItem(op, arguments...)); err != nil {
			return nil, err
		}
	}
	return root, nil
}

func (database *Database) repositoryPathForAnchor(anchor string) string {
	var repositoryPath string
	_ = database.sql.QueryRow(`SELECT COALESCE(repos_path, '') FROM NODES_CURRENT WHERE wc_id = ? AND local_relpath = ?`, database.wcID, anchor).Scan(&repositoryPath)
	return repositoryPath
}

func (directory *updateDirectory) DeleteEntry(ctx context.Context, name string, _ svn.Revnum) error {
	if directory.obstructed {
		return nil
	}
	relpath := directory.editor.relpath(name)
	directory.editor.deleted[name] = true
	if err := directory.editor.rememberCopySources(ctx, relpath); err != nil {
		return err
	}
	info, infoErr := directory.editor.database.Info(ctx, directory.editor.localPath(name))
	locallyModified := false
	if infoErr == nil && info.Schedule != ScheduleDelete {
		if info.Kind == svn.NodeDir {
			if _, err := os.Lstat(directory.editor.localPath(name)); err == nil {
				err = directory.editor.database.Diff(ctx, directory.editor.localPath(name), svn.DepthInfinity, func(context.Context, *Difference) error {
					locallyModified = true
					return nil
				})
				if err != nil {
					return err
				}
			}
		} else {
			status, err := directory.editor.database.statusForInfo(ctx, info)
			if err != nil {
				return err
			}
			locallyModified = status.TextStatus == StatusModified || status.PropertyStatus == StatusModified
		}
	}
	if directory.editor.options.Accept == ConflictTheirs {
		locallyModified = false
	}
	transaction, err := directory.editor.database.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if locallyModified {
		opDepth := relpathDepth(relpath)
		_, err = transaction.ExecContext(ctx, `INSERT OR IGNORE INTO NODES
			(wc_id, local_relpath, op_depth, parent_relpath, repos_id, repos_path, revision, presence,
			kind, properties, depth, checksum, symlink_target, changed_revision, changed_date, changed_author, file_external)
			SELECT wc_id, local_relpath, ?, parent_relpath, repos_id, repos_path, revision, 'normal',
			kind, properties, depth, checksum, symlink_target, changed_revision, changed_date, changed_author, file_external
			FROM NODES WHERE wc_id=? AND (local_relpath=? OR local_relpath LIKE ? ESCAPE '#') AND op_depth=0`,
			opDepth, directory.editor.database.wcID, relpath, descendantPattern(relpath))
		if err != nil {
			return err
		}
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM NODES WHERE wc_id = ? AND (local_relpath = ? OR local_relpath LIKE ? ESCAPE '#') AND op_depth = 0`, directory.editor.database.wcID, relpath, descendantPattern(relpath)); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return err
	}
	if locallyModified {
		if err := directory.editor.database.recordTreeConflict(ctx, directory.editor, name, info.Kind, "edited", "deleted"); err != nil {
			return err
		}
		return nil
	}
	if infoErr == nil {
		op := workFileRemove
		args := []string{relpath}
		if info.Kind == svn.NodeDir {
			op, args = workDirRemove, []string{relpath, "1"}
		}
		_, err = directory.editor.database.Enqueue(ctx, WorkItem(op, args...))
	}
	if directory.editor.options.Notify != nil {
		directory.editor.options.Notify(notify.Notify{Action: notify.ActionUpdateDelete, Path: directory.editor.localPath(name)})
	}
	return err
}

func (directory *updateDirectory) AddDirectory(ctx context.Context, name string, source *delta.CopySource) (delta.DirEditor, error) {
	if directory.obstructed {
		child := &updateDirectory{editor: directory.editor, path: name, added: true, props: make(svn.Props), depth: svn.DepthInfinity, obstructed: true, shadowed: true}
		if err := child.writeNode(ctx, PresenceIncomplete); err != nil {
			return nil, err
		}
		return child, nil
	}
	if info, err := directory.editor.database.Info(ctx, directory.editor.localPath(name)); err == nil {
		switch info.Schedule {
		case ScheduleDelete:
			return &updateDirectory{editor: directory.editor, path: name, props: info.BaseProperties.Clone(), baseProps: info.BaseProperties.Clone(), workingProps: info.WorkingProperties.Clone(), depth: info.Depth, localDeleted: true}, nil
		case ScheduleAdd:
			child := &updateDirectory{editor: directory.editor, path: name, added: true, localAdded: true, props: make(svn.Props), depth: svn.DepthInfinity}
			if err := child.writeNode(ctx, PresenceIncomplete); err != nil {
				return nil, err
			}
			return child, nil
		}
	}
	if _, err := os.Lstat(directory.editor.localPath(name)); err == nil && !directory.editor.deleted[name] {
		child := &updateDirectory{editor: directory.editor, path: name, added: true, props: make(svn.Props), depth: svn.DepthInfinity, obstructed: !directory.editor.options.Force}
		if err := child.writeNode(ctx, PresenceIncomplete); err != nil {
			return nil, err
		}
		return child, nil
	}
	if source != nil {
		sourceNode, err := directory.editor.materializeCopyDirectory(ctx, name, source)
		if err != nil {
			return nil, err
		}
		if sourceNode != nil {
			properties, err := parseProperties([]byte(sourceNode.properties))
			if err != nil {
				return nil, err
			}
			return &updateDirectory{editor: directory.editor, path: name, added: true, props: properties.Clone(), baseProps: properties.Clone(), workingProps: properties.Clone(), depth: svn.DepthInfinity}, nil
		}
	}
	child := &updateDirectory{editor: directory.editor, path: name, added: true, props: make(svn.Props), depth: svn.DepthInfinity}
	if err := child.writeNode(ctx, PresenceIncomplete); err != nil {
		return nil, err
	}
	if _, err := directory.editor.database.Enqueue(ctx, WorkItem(workDirInstall, directory.editor.relpath(name))); err != nil {
		return nil, err
	}
	return child, nil
}

func (directory *updateDirectory) OpenDirectory(ctx context.Context, name string, _ svn.Revnum) (delta.DirEditor, error) {
	if directory.obstructed {
		return noopUpdateDirectory(ctx)
	}
	info, err := directory.editor.database.Info(ctx, directory.editor.localPath(name))
	if err != nil {
		return nil, err
	}
	if info.Conflict != nil && info.Conflict.Tree && info.OperationDepth > 0 {
		base, err := directory.editor.database.readNode(ctx, "NODES_BASE", directory.editor.relpath(name))
		if err != nil {
			return nil, err
		}
		baseProps, err := parseProperties([]byte(base.properties))
		if err != nil {
			return nil, err
		}
		return &updateDirectory{editor: directory.editor, path: name, added: true, localAdded: true, conflicted: true, props: baseProps.Clone(), baseProps: baseProps, workingProps: info.WorkingProperties.Clone(), depth: info.Depth}, nil
	}
	return &updateDirectory{editor: directory.editor, path: name, props: info.BaseProperties.Clone(), baseProps: info.BaseProperties.Clone(), workingProps: info.WorkingProperties.Clone(), depth: info.Depth, localDeleted: info.Schedule == ScheduleDelete}, nil
}

func (directory *updateDirectory) ChangeProp(_ context.Context, name string, value []byte) error {
	changeUpdateProperty(directory.props, name, value)
	return nil
}

func (directory *updateDirectory) AbsentDirectory(ctx context.Context, name string) error {
	if directory.obstructed {
		return nil
	}
	child := &updateDirectory{editor: directory.editor, path: name, props: make(svn.Props), depth: svn.DepthExclude}
	return child.writeNode(ctx, PresenceServerExcluded)
}

func (directory *updateDirectory) AddFile(ctx context.Context, name string, source *delta.CopySource) (delta.FileEditor, error) {
	if directory.obstructed {
		return &updateFile{editor: directory.editor, path: name, added: true, props: make(svn.Props), localModified: true, obstructed: true, shadowed: true}, nil
	}
	if info, err := directory.editor.database.Info(ctx, directory.editor.localPath(name)); err == nil {
		switch info.Schedule {
		case ScheduleDelete:
			return &updateFile{editor: directory.editor, path: name, props: info.BaseProperties.Clone(), baseProps: info.BaseProperties.Clone(), workingProps: info.WorkingProperties.Clone(), baseChecksum: info.Checksum, baseRevision: info.Revision, localDeleted: true}, nil
		case ScheduleAdd:
			return &updateFile{editor: directory.editor, path: name, added: true, props: make(svn.Props), localAdded: true}, nil
		}
	}
	if _, err := os.Lstat(directory.editor.localPath(name)); err == nil && !directory.editor.deleted[name] {
		return &updateFile{editor: directory.editor, path: name, added: true, props: make(svn.Props), localModified: true, obstructed: !directory.editor.options.Force}, nil
	}
	if sourceNode, err := directory.editor.copySourceNode(ctx, source); err == nil && sourceNode != nil {
		properties, err := parseProperties([]byte(sourceNode.properties))
		if err != nil {
			return nil, err
		}
		var checksum *svn.Checksum
		if sourceNode.checksum != "" {
			parsed, err := svn.ParseChecksum(sourceNode.checksum)
			if err != nil {
				return nil, err
			}
			checksum = &parsed
		}
		return &updateFile{editor: directory.editor, path: name, added: true, props: properties.Clone(), baseProps: properties.Clone(), workingProps: properties.Clone(), baseChecksum: checksum, baseRevision: source.Rev}, nil
	} else if err != nil && source != nil {
		return nil, err
	}
	return &updateFile{editor: directory.editor, path: name, added: true, props: make(svn.Props)}, nil
}

func (directory *updateDirectory) OpenFile(ctx context.Context, name string, _ svn.Revnum) (delta.FileEditor, error) {
	if directory.obstructed {
		return noopUpdateFile(ctx)
	}
	info, err := directory.editor.database.Info(ctx, directory.editor.localPath(name))
	if err != nil {
		return nil, err
	}
	status, err := directory.editor.database.statusForInfo(ctx, info)
	if err != nil {
		return nil, err
	}
	if info.Conflict != nil && info.Conflict.Tree && info.OperationDepth > 0 {
		base, err := directory.editor.database.readNode(ctx, "NODES_BASE", directory.editor.relpath(name))
		if err != nil {
			return nil, err
		}
		var checksum *svn.Checksum
		if base.checksum != "" {
			parsed, err := svn.ParseChecksum(base.checksum)
			if err != nil {
				return nil, err
			}
			checksum = &parsed
		}
		baseProps, err := parseProperties([]byte(base.properties))
		if err != nil {
			return nil, err
		}
		return &updateFile{editor: directory.editor, path: name, added: true, props: baseProps.Clone(), baseProps: baseProps, workingProps: info.WorkingProperties.Clone(), baseChecksum: checksum, baseRevision: base.revision, localAdded: true, conflicted: true}, nil
	}
	return &updateFile{editor: directory.editor, path: name, props: info.BaseProperties.Clone(), baseProps: info.BaseProperties.Clone(), workingProps: info.WorkingProperties.Clone(), baseChecksum: info.Checksum, baseRevision: info.Revision, localModified: status.TextStatus == StatusModified, localDeleted: info.Schedule == ScheduleDelete}, nil
}

func (directory *updateDirectory) AbsentFile(ctx context.Context, name string) error {
	if directory.obstructed {
		return nil
	}
	return directory.writeAbsentFile(ctx, name)
}

func (directory *updateDirectory) Close(ctx context.Context) error {
	if err := directory.writeNode(ctx, PresenceNormal); err != nil {
		return err
	}
	if !directory.added {
		if err := directory.editor.database.reconcileUpdateProperties(ctx, directory.editor, directory.path, svn.NodeDir, directory.baseProps, directory.workingProps, directory.props); err != nil {
			return err
		}
	}
	if directory.localDeleted {
		if err := directory.editor.database.recordTreeConflict(ctx, directory.editor, directory.path, svn.NodeDir, "deleted", "edited"); err != nil {
			return err
		}
	}
	if directory.localAdded && !directory.conflicted {
		if err := directory.editor.database.recordTreeConflict(ctx, directory.editor, directory.path, svn.NodeDir, "added", "added"); err != nil {
			return err
		}
	}
	if directory.obstructed {
		if err := directory.editor.database.insertDeleteLayer(ctx, directory.editor.relpath(directory.path)); err != nil {
			return err
		}
		if !directory.shadowed {
			if err := directory.editor.database.recordTreeConflict(ctx, directory.editor, directory.path, svn.NodeDir, "unversioned", "added"); err != nil {
				return err
			}
		}
	}
	if directory.editor.options.Notify != nil && directory.added {
		directory.editor.options.Notify(notify.Notify{Action: notify.ActionUpdateAdd, Path: directory.editor.localPath(directory.path), Kind: svn.NodeDir})
	}
	return nil
}

func noopUpdateDirectory(ctx context.Context) (delta.DirEditor, error) {
	return delta.Noop().OpenRoot(ctx, svn.InvalidRevnum)
}

func noopUpdateFile(ctx context.Context) (delta.FileEditor, error) {
	directory, err := noopUpdateDirectory(ctx)
	if err != nil {
		return nil, err
	}
	return directory.AddFile(ctx, "", nil)
}

func (directory *updateDirectory) writeNode(ctx context.Context, presence Presence) error {
	relpath := directory.editor.relpath(directory.path)
	parent := path.Dir(relpath)
	if parent == "." {
		parent = ""
	}
	_, err := directory.editor.database.sql.ExecContext(ctx, `INSERT INTO NODES
		(wc_id, local_relpath, op_depth, parent_relpath, repos_id, repos_path, revision, presence, kind, properties, depth, changed_revision)
		VALUES (?, ?, 0, ?, ?, ?, ?, ?, 'dir', ?, ?, ?)
		ON CONFLICT(wc_id, local_relpath, op_depth) DO UPDATE SET repos_id=excluded.repos_id, repos_path=excluded.repos_path,
		revision=excluded.revision, presence=excluded.presence, kind='dir', properties=excluded.properties, depth=excluded.depth,
		changed_revision=excluded.changed_revision`, directory.editor.database.wcID, relpath, nullableParent(relpath, parent),
		directory.editor.database.repository.ID, directory.editor.repositoryPath(directory.path), directory.editor.target,
		string(presence), propertyBytes(directory.props), directory.depth.String(), directory.editor.target)
	return err
}

func (directory *updateDirectory) writeAbsentFile(ctx context.Context, name string) error {
	relpath := directory.editor.relpath(name)
	parent := path.Dir(relpath)
	if parent == "." {
		parent = ""
	}
	_, err := directory.editor.database.sql.ExecContext(ctx, `INSERT INTO NODES
		(wc_id, local_relpath, op_depth, parent_relpath, repos_id, repos_path, revision, presence, kind, properties, changed_revision)
		VALUES (?, ?, 0, ?, ?, ?, ?, 'server-excluded', 'file', X'2829', ?) ON CONFLICT(wc_id, local_relpath, op_depth)
		DO UPDATE SET revision=excluded.revision, presence=excluded.presence`, directory.editor.database.wcID, relpath,
		parent, directory.editor.database.repository.ID, directory.editor.repositoryPath(name), directory.editor.target, directory.editor.target)
	return err
}

func (file *updateFile) ApplyTextDelta(ctx context.Context, base *svn.Checksum) (delta.WindowHandler, error) {
	if base != nil && file.baseChecksum != nil {
		pristine, err := file.editor.database.Pristine(ctx, *file.baseChecksum)
		if err != nil {
			return nil, err
		}
		actual := pristine.MD5
		if base.Kind == svn.ChecksumSHA1 {
			actual = pristine.Checksum
		}
		if !actual.Equal(*base) {
			return nil, fmt.Errorf("%w: base text", svn.ErrChecksumMismatch)
		}
	}
	var err error
	if file.baseChecksum != nil {
		reader, openErr := file.editor.database.OpenPristine(ctx, *file.baseChecksum)
		if openErr != nil {
			return nil, openErr
		}
		file.source, err = os.Open(reader.(*os.File).Name())
		reader.Close()
	} else {
		file.source, err = os.Open(os.DevNull)
	}
	if err != nil {
		return nil, err
	}
	file.temporary, err = os.CreateTemp(filepath.Join(file.editor.database.wcRoot, ".svn", "tmp"), "update-")
	if err != nil {
		file.source.Close()
		return nil, err
	}
	return &updateWindows{file: file}, nil
}

func (windows *updateWindows) Window(window *delta.Window) error {
	if windows.closed || window == nil {
		return fmt.Errorf("invalid text delta window")
	}
	source := make([]byte, window.SourceLength)
	if window.SourceLength != 0 {
		if _, err := windows.file.source.ReadAt(source, window.SourceOffset); err != nil {
			return err
		}
	}
	target, err := delta.ApplyWindow(source, *window)
	if err != nil {
		return err
	}
	_, err = windows.file.temporary.Write(target)
	return err
}

func (windows *updateWindows) Close() error {
	if windows.closed {
		return fmt.Errorf("text delta already closed")
	}
	windows.closed = true
	return nil
}

func (file *updateFile) ChangeProp(_ context.Context, name string, value []byte) error {
	changeUpdateProperty(file.props, name, value)
	return nil
}

func (file *updateFile) Close(ctx context.Context, expected *svn.Checksum) error {
	if file.source != nil {
		file.source.Close()
	}
	var checksum *svn.Checksum
	var md5Checksum svn.Checksum
	var size int64
	installSource := ""
	if file.temporary != nil {
		if err := file.temporary.Close(); err != nil {
			return err
		}
		name := file.temporary.Name()
		defer os.Remove(name)
		sha1Value, md5Value, fileSize, err := checksumsForFile(name)
		if err != nil {
			return err
		}
		if expected != nil {
			actual := md5Value
			if expected.Kind == svn.ChecksumSHA1 {
				actual = sha1Value
			}
			if !actual.Equal(*expected) {
				return fmt.Errorf("%w: result text", svn.ErrChecksumMismatch)
			}
		}
		if err := file.editor.database.installPristine(ctx, name, sha1Value, md5Value, fileSize); err != nil {
			return err
		}
		checksum, md5Checksum, size = &sha1Value, md5Value, fileSize
		if file.localModified && file.baseChecksum != nil {
			mergedSource, err := file.mergeLocalChanges(ctx, name)
			if err != nil {
				return err
			}
			installSource = mergedSource
		}
	} else if file.baseChecksum != nil {
		checksum = file.baseChecksum
		pristine, err := file.editor.database.Pristine(ctx, *checksum)
		if err != nil {
			return err
		}
		md5Checksum, size = pristine.MD5, pristine.Size
		if expected != nil {
			actual := md5Checksum
			if expected.Kind == svn.ChecksumSHA1 {
				actual = *checksum
			}
			if !actual.Equal(*expected) {
				return fmt.Errorf("%w: result text", svn.ErrChecksumMismatch)
			}
		}
	}
	if err := file.writeNode(ctx, checksum); err != nil {
		return err
	}
	if !file.added {
		if err := file.editor.database.reconcileUpdateProperties(ctx, file.editor, file.path, svn.NodeFile, file.baseProps, file.workingProps, file.props); err != nil {
			return err
		}
	}
	_ = md5Checksum
	_ = size
	if (!file.localModified || installSource != "") && !file.localDeleted && !file.localAdded {
		arguments := []string{file.editor.relpath(file.path), boolString(file.editor.options.UseCommitTimes), "1"}
		if installSource != "" {
			arguments = append(arguments, installSource)
		}
		if _, err := file.editor.database.Enqueue(ctx, WorkItem(workFileInstall, arguments...)); err != nil {
			return err
		}
		if installSource != "" {
			if _, err := file.editor.database.Enqueue(ctx, WorkItem(workFileRemove, installSource)); err != nil {
				return err
			}
		}
	}
	if file.localDeleted {
		if err := file.editor.database.recordTreeConflict(ctx, file.editor, file.path, svn.NodeFile, "deleted", "edited"); err != nil {
			return err
		}
	}
	if file.localAdded && !file.conflicted {
		if err := file.editor.database.recordTreeConflict(ctx, file.editor, file.path, svn.NodeFile, "added", "added"); err != nil {
			return err
		}
	}
	if file.obstructed {
		if err := file.editor.database.insertDeleteLayer(ctx, file.editor.relpath(file.path)); err != nil {
			return err
		}
		if !file.shadowed {
			if err := file.editor.database.recordTreeConflict(ctx, file.editor, file.path, svn.NodeFile, "unversioned", "added"); err != nil {
				return err
			}
		}
	}
	if file.editor.options.Notify != nil {
		action := notify.ActionUpdateUpdate
		if file.added {
			action = notify.ActionUpdateAdd
		}
		file.editor.options.Notify(notify.Notify{Action: action, Path: file.editor.localPath(file.path), Kind: svn.NodeFile})
	}
	return nil
}

func (file *updateFile) mergeLocalChanges(ctx context.Context, incomingName string) (string, error) {
	baseReader, err := file.editor.database.OpenPristine(ctx, *file.baseChecksum)
	if err != nil {
		return "", err
	}
	base, err := io.ReadAll(baseReader)
	baseReader.Close()
	if err != nil {
		return "", err
	}
	mine, err := os.ReadFile(file.editor.localPath(file.path))
	if err != nil {
		return "", err
	}
	theirs, err := os.ReadFile(incomingName)
	if err != nil {
		return "", err
	}
	result := diff3.Merge(base, mine, theirs, ".r"+strconv.FormatInt(int64(file.baseRevision), 10), ".r"+strconv.FormatInt(int64(file.editor.target), 10))
	if result.Conflicted {
		switch file.editor.options.Accept {
		case ConflictWorking, ConflictMine:
			result.Contents, result.Conflicted = mine, false
		case ConflictBase:
			result.Contents, result.Conflicted = base, false
		case ConflictTheirs:
			result.Contents, result.Conflicted = theirs, false
		}
	}
	merged, err := os.CreateTemp(filepath.Join(file.editor.database.wcRoot, ".svn", "tmp"), "merge-")
	if err != nil {
		return "", err
	}
	if _, err = merged.Write(result.Contents); err == nil {
		err = merged.Close()
	} else {
		merged.Close()
	}
	if err != nil {
		os.Remove(merged.Name())
		return "", err
	}
	mergedRelpath, err := file.editor.database.localRelpath(merged.Name())
	if err != nil {
		return "", err
	}
	if result.Conflicted {
		basename := filepath.Base(file.editor.localPath(file.path))
		oldMarker := basename + ".r" + strconv.FormatInt(int64(file.baseRevision), 10)
		newMarker := basename + ".r" + strconv.FormatInt(int64(file.editor.target), 10)
		mineMarker := basename + ".mine"
		for name, contents := range map[string][]byte{oldMarker: base, newMarker: theirs, mineMarker: mine} {
			if err := file.queueMarker(ctx, name, contents); err != nil {
				return "", err
			}
		}
		relpath := file.editor.relpath(file.path)
		parent := path.Dir(relpath)
		if parent == "." {
			parent = ""
		}
		conflictData, marshalErr := file.textConflictSkel(oldMarker, mineMarker, newMarker).MarshalBinary()
		if marshalErr != nil {
			return "", marshalErr
		}
		_, err = file.editor.database.sql.ExecContext(ctx, `INSERT INTO ACTUAL_NODE
			(wc_id, local_relpath, parent_relpath, conflict_data) VALUES (?, ?, ?, ?)
			ON CONFLICT(wc_id, local_relpath) DO UPDATE SET conflict_data=excluded.conflict_data`,
			file.editor.database.wcID, relpath, parent, conflictData)
		if err != nil {
			return "", err
		}
	}
	return mergedRelpath, nil
}

func (file *updateFile) textConflictSkel(oldMarker, mineMarker, newMarker string) *skel.Node {
	version := func(revision svn.Revnum) *skel.Node {
		return skel.NewList(
			skel.NewString("subversion"), skel.NewString(file.editor.database.repository.Root),
			skel.NewString(file.editor.database.repository.UUID), skel.NewString(file.editor.repositoryPath(file.path)),
			skel.NewString(strconv.FormatInt(int64(file.baseRevision), 10)), skel.NewString(strconv.FormatInt(int64(revision), 10)),
			skel.NewString("file"),
		)
	}
	operation := skel.NewList(skel.NewString("update"), skel.NewList(version(file.baseRevision), version(file.editor.target)))
	markers := skel.NewList(skel.NewString(oldMarker), skel.NewString(mineMarker), skel.NewString(newMarker))
	conflicts := skel.NewList(skel.NewList(skel.NewString("text"), markers))
	return skel.NewList(operation, conflicts)
}

func (file *updateFile) queueMarker(ctx context.Context, basename string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Join(file.editor.database.wcRoot, ".svn", "tmp"), "marker-")
	if err != nil {
		return err
	}
	if _, err = temporary.Write(contents); err == nil {
		err = temporary.Close()
	} else {
		temporary.Close()
	}
	if err != nil {
		return err
	}
	source, err := file.editor.database.localRelpath(temporary.Name())
	if err != nil {
		return err
	}
	destination, err := file.editor.database.localRelpath(filepath.Join(filepath.Dir(file.editor.localPath(file.path)), basename))
	if err != nil {
		return err
	}
	_, err = file.editor.database.Enqueue(ctx, WorkItem(workFileMove, source, destination))
	return err
}

func (file *updateFile) writeNode(ctx context.Context, checksum *svn.Checksum) error {
	relpath := file.editor.relpath(file.path)
	parent := path.Dir(relpath)
	if parent == "." {
		parent = ""
	}
	var serialized any
	if checksum != nil {
		serialized = checksum.Serialize()
	}
	_, err := file.editor.database.sql.ExecContext(ctx, `INSERT INTO NODES
		(wc_id, local_relpath, op_depth, parent_relpath, repos_id, repos_path, revision, presence, kind, properties, checksum, changed_revision)
		VALUES (?, ?, 0, ?, ?, ?, ?, 'normal', 'file', ?, ?, ?)
		ON CONFLICT(wc_id, local_relpath, op_depth) DO UPDATE SET repos_id=excluded.repos_id, repos_path=excluded.repos_path,
		revision=excluded.revision, presence='normal', kind='file', properties=excluded.properties, checksum=excluded.checksum,
		changed_revision=excluded.changed_revision, translated_size=NULL, last_mod_time=NULL`, file.editor.database.wcID, relpath, parent,
		file.editor.database.repository.ID, file.editor.repositoryPath(file.path), file.editor.target, propertyBytes(file.props), serialized, file.editor.target)
	return err
}

func (database *Database) installPristine(ctx context.Context, source string, sha1Checksum, md5Checksum svn.Checksum, size int64) error {
	hexValue := sha1Checksum.Hex()
	directory := filepath.Join(database.wcRoot, ".svn", "pristine", hexValue[:2])
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	destination := filepath.Join(directory, hexValue+".svn-base")
	if _, err := os.Stat(destination); os.IsNotExist(err) {
		if err := copyFile(source, destination); err != nil {
			return err
		}
	}
	_, err := database.sql.ExecContext(ctx, `INSERT OR IGNORE INTO PRISTINE (checksum, compression, size, refcount, md5_checksum) VALUES (?, NULL, ?, 0, ?)`, sha1Checksum.Serialize(), size, md5Checksum.Serialize())
	return err
}

func checksumsForFile(name string) (svn.Checksum, svn.Checksum, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		return svn.Checksum{}, svn.Checksum{}, 0, err
	}
	defer file.Close()
	sha1Hash, md5Hash := sha1.New(), md5.New()
	size, err := io.Copy(io.MultiWriter(sha1Hash, md5Hash), file)
	return svn.Checksum{Kind: svn.ChecksumSHA1, Digest: sha1Hash.Sum(nil)}, svn.Checksum{Kind: svn.ChecksumMD5, Digest: md5Hash.Sum(nil)}, size, err
}

func copyFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func changeUpdateProperty(properties svn.Props, name string, value []byte) {
	if value == nil {
		delete(properties, name)
	} else {
		properties[name] = append([]byte(nil), value...)
	}
}

func nullableParent(relpath, parent string) any {
	if relpath == "" {
		return nil
	}
	return parent
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
