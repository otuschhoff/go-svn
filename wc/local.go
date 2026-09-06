package wc

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/svn"
)

type AddOptions struct {
	Depth          svn.Depth
	Force          bool
	Parents        bool
	NoIgnore       bool
	GlobalIgnores  []string
	AutoProps      map[string]svn.Props
	DetectMIMEType bool
}

type DeleteOptions struct {
	KeepLocal bool
	Force     bool
}

type RevertOptions struct {
	Depth       svn.Depth
	RemoveAdded bool
}

func (database *Database) Add(ctx context.Context, targetPath string, options AddOptions) error {
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return err
	}
	if relpath == "" {
		return fmt.Errorf("%w: cannot add a working copy root", svn.ErrEntryExists)
	}
	stat, err := os.Lstat(targetPath)
	if err != nil {
		return err
	}
	parentRelpath := path.Dir(relpath)
	if parentRelpath == "." {
		parentRelpath = ""
	}
	parent, err := database.Info(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(parentRelpath)))
	if (err != nil || parent.Kind != svn.NodeDir) && options.Parents {
		if err := database.addParents(ctx, parentRelpath); err != nil {
			return err
		}
		parent, err = database.Info(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(parentRelpath)))
	}
	if err != nil || parent.Kind != svn.NodeDir {
		return fmt.Errorf("%w: parent of %s is not versioned", svn.ErrWCPathNotFound, targetPath)
	}
	if !options.NoIgnore {
		ignored, err := database.isIgnored(ctx, relpath, options.GlobalIgnores)
		if err != nil {
			return err
		}
		if ignored {
			return fmt.Errorf("%w: %s is ignored", svn.ErrEntryNotFound, targetPath)
		}
	}
	if _, err := database.Info(ctx, targetPath); err == nil && !options.Force {
		return fmt.Errorf("%w: %s", svn.ErrEntryExists, targetPath)
	}
	if options.Depth == svn.DepthUnknown {
		options.Depth = svn.DepthInfinity
	}
	opDepth := relpathDepth(relpath)
	return database.addPath(ctx, relpath, stat, opDepth, options.Depth, options)
}

func (database *Database) addPath(ctx context.Context, relpath string, stat os.FileInfo, opDepth int, depth svn.Depth, options AddOptions) error {
	kind := "file"
	nodeDepth := ""
	properties := make(svn.Props)
	if stat.IsDir() {
		kind, nodeDepth = "dir", svn.DepthInfinity.String()
	} else {
		for pattern, values := range options.AutoProps {
			if matched, _ := path.Match(pattern, path.Base(relpath)); matched {
				for name, value := range values {
					properties[name] = append([]byte(nil), value...)
				}
			}
		}
		if options.DetectMIMEType && len(properties[props.MIMEType]) == 0 {
			file, err := os.Open(filepath.Join(database.wcRoot, filepath.FromSlash(relpath)))
			if err != nil {
				return err
			}
			data, readErr := io.ReadAll(io.LimitReader(file, 8192))
			file.Close()
			if readErr != nil {
				return readErr
			}
			if mimeType := props.DetectMIMEType(relpath, data); mimeType != "" {
				properties[props.MIMEType] = []byte(mimeType)
			}
		}
	}
	parent := path.Dir(relpath)
	if parent == "." {
		parent = ""
	}
	_, err := database.sql.ExecContext(ctx, `INSERT INTO NODES
		(wc_id, local_relpath, op_depth, parent_relpath, presence, kind, properties, depth)
		VALUES (?, ?, ?, ?, 'normal', ?, ?, NULLIF(?, ''))
		ON CONFLICT(wc_id, local_relpath, op_depth) DO UPDATE SET presence='normal', kind=excluded.kind, properties=excluded.properties`,
		database.wcID, relpath, opDepth, parent, kind, propertyBytes(properties), nodeDepth)
	if err != nil || !stat.IsDir() || depth == svn.DepthEmpty {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(database.wcRoot, filepath.FromSlash(relpath)))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".svn" {
			continue
		}
		childDepth := depth
		if depth == svn.DepthFiles || depth == svn.DepthImmediates {
			childDepth = svn.DepthEmpty
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if depth == svn.DepthFiles && info.IsDir() {
			continue
		}
		childRelpath := path.Join(relpath, entry.Name())
		if !options.NoIgnore {
			ignored, err := database.isIgnored(ctx, childRelpath, options.GlobalIgnores)
			if err != nil {
				return err
			}
			if ignored {
				continue
			}
		}
		if err := database.addPath(ctx, childRelpath, info, opDepth, childDepth, options); err != nil {
			return err
		}
	}
	return nil
}

func (database *Database) addParents(ctx context.Context, parentRelpath string) error {
	var missing []string
	for current := parentRelpath; current != ""; current = path.Dir(current) {
		if _, err := database.Info(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(current))); err == nil {
			break
		}
		missing = append(missing, current)
		if path.Dir(current) == "." {
			break
		}
	}
	for index := len(missing) - 1; index >= 0; index-- {
		stat, err := os.Stat(filepath.Join(database.wcRoot, filepath.FromSlash(missing[index])))
		if err != nil || !stat.IsDir() {
			return fmt.Errorf("%w: missing parent %s", svn.ErrWCPathNotFound, missing[index])
		}
		if err := database.addPath(ctx, missing[index], stat, relpathDepth(missing[index]), svn.DepthEmpty, AddOptions{NoIgnore: true}); err != nil {
			return err
		}
	}
	return nil
}

func (database *Database) Mkdir(ctx context.Context, targetPath string, parents bool) error {
	mode := os.FileMode(0o755)
	var err error
	if parents {
		err = os.MkdirAll(targetPath, mode)
	} else {
		err = os.Mkdir(targetPath, mode)
	}
	if err != nil {
		return err
	}
	return database.Add(ctx, targetPath, AddOptions{Depth: svn.DepthInfinity})
}

func (database *Database) Copy(ctx context.Context, sourcePath, destinationPath string) error {
	sourceRelpath, err := database.localRelpath(sourcePath)
	if err != nil {
		return err
	}
	destinationRelpath, err := database.localRelpath(destinationPath)
	if err != nil {
		return err
	}
	if sourceRelpath == "" || destinationRelpath == "" || strings.HasPrefix(destinationRelpath, sourceRelpath+"/") {
		return fmt.Errorf("%w: invalid WC copy", svn.ErrUnsupportedFeature)
	}
	if _, err := database.Info(ctx, sourcePath); err != nil {
		return err
	}
	if _, err := os.Lstat(destinationPath); !os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", svn.ErrEntryExists, destinationPath)
	}
	if err := copyTree(sourcePath, destinationPath); err != nil {
		return err
	}
	rows, err := database.sql.QueryContext(ctx, `SELECT `+nodeColumns+` FROM NODES_CURRENT WHERE wc_id=? AND (local_relpath=? OR local_relpath LIKE ? ESCAPE '#') ORDER BY local_relpath`, database.wcID, sourceRelpath, descendantPattern(sourceRelpath))
	if err != nil {
		return err
	}
	var nodes []nodeRow
	for rows.Next() {
		var node nodeRow
		if err := scanNode(rows, &node); err != nil {
			rows.Close()
			return err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	opDepth := relpathDepth(destinationRelpath)
	transaction, err := database.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for _, node := range nodes {
		suffix := strings.TrimPrefix(node.relpath, sourceRelpath)
		relpath := destinationRelpath + suffix
		parent := path.Dir(relpath)
		if parent == "." {
			parent = ""
		}
		_, err = transaction.ExecContext(ctx, `INSERT INTO NODES
			(wc_id, local_relpath, op_depth, parent_relpath, repos_id, repos_path, revision, presence, moved_here,
			kind, properties, depth, checksum, symlink_target, changed_revision, changed_date, changed_author, file_external)
			VALUES (?, ?, ?, ?, NULLIF(?, 0), NULLIF(?, ''), NULLIF(?, -1), 'normal', 0, ?, ?, NULLIF(?, ''),
			NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, -1), NULLIF(?, 0), NULLIF(?, ''), ?)`,
			database.wcID, relpath, opDepth, parent, node.reposID, node.reposPath, node.revision, node.kind,
			[]byte(node.properties), node.depth, node.checksum, node.symlinkTarget, node.changedRevision, node.changedDate,
			node.changedAuthor, node.fileExternal)
		if err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func (database *Database) Move(ctx context.Context, sourcePath, destinationPath string, force bool) error {
	sourceRelpath, err := database.localRelpath(sourcePath)
	if err != nil {
		return err
	}
	destinationRelpath, err := database.localRelpath(destinationPath)
	if err != nil {
		return err
	}
	if err := database.Copy(ctx, sourcePath, destinationPath); err != nil {
		return err
	}
	if err := database.Delete(ctx, sourcePath, DeleteOptions{Force: true}); err != nil {
		return err
	}
	if _, err := database.sql.ExecContext(ctx, `UPDATE NODES SET moved_here=1 WHERE wc_id=? AND local_relpath=? AND op_depth=?`, database.wcID, destinationRelpath, relpathDepth(destinationRelpath)); err != nil {
		return err
	}
	_, err = database.sql.ExecContext(ctx, `UPDATE NODES SET moved_to=? WHERE wc_id=? AND local_relpath=? AND op_depth=?`, destinationRelpath, database.wcID, sourceRelpath, relpathDepth(sourceRelpath))
	return err
}

func (database *Database) Delete(ctx context.Context, targetPath string, options DeleteOptions) error {
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return err
	}
	if relpath == "" {
		return fmt.Errorf("%w: cannot delete working copy root", svn.ErrWCInvalidOpOnCwd)
	}
	info, err := database.Info(ctx, targetPath)
	if err != nil {
		return err
	}
	if !options.Force {
		if err := database.checkDeleteSafe(ctx, targetPath); err != nil {
			return err
		}
	}
	if info.OperationDepth > 0 {
		var baseCount int
		_ = database.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM NODES WHERE wc_id=? AND local_relpath=? AND op_depth=0`, database.wcID, relpath).Scan(&baseCount)
		if baseCount == 0 {
			_, err = database.sql.ExecContext(ctx, `DELETE FROM NODES WHERE wc_id=? AND (local_relpath=? OR local_relpath LIKE ? ESCAPE '#') AND op_depth>0`, database.wcID, relpath, escapeLike(relpath)+"/%")
		} else {
			err = database.insertDeleteLayer(ctx, relpath)
		}
	} else {
		err = database.insertDeleteLayer(ctx, relpath)
	}
	if err != nil {
		return err
	}
	if !options.KeepLocal {
		return os.RemoveAll(targetPath)
	}
	return nil
}

func (database *Database) checkDeleteSafe(ctx context.Context, targetPath string) error {
	return database.Status(ctx, targetPath, StatusOptions{Depth: svn.DepthInfinity, NoIgnore: true}, func(status *Status) error {
		unsafe := status.TextStatus == StatusModified || status.PropertyStatus == StatusModified || status.Conflicted ||
			status.NodeStatus == StatusMissing || status.NodeStatus == StatusObstructed || status.NodeStatus == StatusUnversioned
		if unsafe {
			return fmt.Errorf("%w: %s has local modifications", svn.ErrClientModified, status.Path)
		}
		return nil
	})
}

func (database *Database) insertDeleteLayer(ctx context.Context, relpath string) error {
	opDepth := relpathDepth(relpath)
	_, err := database.sql.ExecContext(ctx, `INSERT OR REPLACE INTO NODES
		(wc_id, local_relpath, op_depth, parent_relpath, presence, kind)
		SELECT wc_id, local_relpath, ?, parent_relpath, 'base-deleted', kind FROM NODES_BASE
		WHERE wc_id=? AND (local_relpath=? OR local_relpath LIKE ? ESCAPE '#')`, opDepth, database.wcID, relpath, escapeLike(relpath)+"/%")
	return err
}

func (database *Database) SetProperty(ctx context.Context, targetPath, name string, value []byte, force bool) error {
	info, err := database.Info(ctx, targetPath)
	if err != nil {
		return err
	}
	if value != nil && !force {
		value, err = props.Canonicalize(name, value, info.Kind)
		if err != nil {
			return err
		}
	}
	properties := info.WorkingProperties.Clone()
	if value == nil {
		delete(properties, name)
	} else {
		properties[name] = append([]byte(nil), value...)
	}
	if info.OperationDepth > 0 && info.Schedule == ScheduleAdd {
		_, err = database.sql.ExecContext(ctx, `UPDATE NODES SET properties=? WHERE wc_id=? AND local_relpath=? AND op_depth=?`, propertyBytes(properties), database.wcID, info.RelativePath, info.OperationDepth)
		return err
	}
	parent := path.Dir(info.RelativePath)
	if parent == "." {
		parent = ""
	}
	_, err = database.sql.ExecContext(ctx, `INSERT INTO ACTUAL_NODE (wc_id, local_relpath, parent_relpath, properties)
		VALUES (?, ?, ?, ?) ON CONFLICT(wc_id, local_relpath) DO UPDATE SET properties=excluded.properties`, database.wcID, info.RelativePath, nullableParent(info.RelativePath, parent), propertyBytes(properties))
	return err
}

func (database *Database) SetChangelist(ctx context.Context, targetPath, changelist string) error {
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return err
	}
	parent := path.Dir(relpath)
	if parent == "." {
		parent = ""
	}
	if changelist == "" {
		_, err = database.sql.ExecContext(ctx, `UPDATE ACTUAL_NODE SET changelist=NULL WHERE wc_id=? AND local_relpath=?`, database.wcID, relpath)
	} else {
		_, err = database.sql.ExecContext(ctx, `INSERT INTO ACTUAL_NODE (wc_id, local_relpath, parent_relpath, changelist)
			VALUES (?, ?, ?, ?) ON CONFLICT(wc_id, local_relpath) DO UPDATE SET changelist=excluded.changelist`, database.wcID, relpath, nullableParent(relpath, parent), changelist)
	}
	return err
}

func (database *Database) Revert(ctx context.Context, targetPath string, options RevertOptions) error {
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return err
	}
	if options.Depth == svn.DepthUnknown {
		options.Depth = svn.DepthEmpty
	}
	paths, err := database.revertPaths(ctx, relpath, options.Depth)
	if err != nil {
		return err
	}
	var addedPaths []string
	for _, current := range paths {
		if options.RemoveAdded {
			var added bool
			err := database.sql.QueryRowContext(ctx, `SELECT EXISTS(
				SELECT 1 FROM NODES n WHERE n.wc_id=? AND n.local_relpath=? AND n.op_depth>0
				AND NOT EXISTS (SELECT 1 FROM NODES b WHERE b.wc_id=n.wc_id AND b.local_relpath=n.local_relpath AND b.op_depth=0))`,
				database.wcID, current).Scan(&added)
			if err != nil {
				return err
			}
			if added {
				addedPaths = append(addedPaths, current)
			}
		}
		if _, err := database.sql.ExecContext(ctx, `DELETE FROM NODES WHERE wc_id=? AND local_relpath=? AND op_depth>0`, database.wcID, current); err != nil {
			return err
		}
		if _, err := database.sql.ExecContext(ctx, `DELETE FROM ACTUAL_NODE WHERE wc_id=? AND local_relpath=?`, database.wcID, current); err != nil {
			return err
		}
	}
	for index := len(addedPaths) - 1; index >= 0; index-- {
		added := addedPaths[index]
		if err := os.RemoveAll(filepath.Join(database.wcRoot, filepath.FromSlash(added))); err != nil {
			return err
		}
	}
	for _, current := range paths {
		var kind string
		err := database.sql.QueryRowContext(ctx, `SELECT kind FROM NODES_BASE WHERE wc_id=? AND local_relpath=? AND presence='normal'`, database.wcID, current).Scan(&kind)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return err
		}
		if kind == "dir" {
			if err := os.MkdirAll(filepath.Join(database.wcRoot, filepath.FromSlash(current)), 0o755); err != nil {
				return err
			}
		} else if _, err := database.Enqueue(ctx, WorkItem(workFileInstall, current, "0", "1")); err != nil {
			return err
		}
	}
	return database.RunWorkQueue(ctx)
}

func (database *Database) revertPaths(ctx context.Context, relpath string, depth svn.Depth) ([]string, error) {
	condition := "local_relpath=?"
	arguments := []any{database.wcID, relpath}
	switch depth {
	case svn.DepthEmpty:
	case svn.DepthFiles:
		condition = "(local_relpath=? OR (parent_relpath=? AND kind!='dir'))"
		arguments = append(arguments, relpath)
	case svn.DepthImmediates:
		condition = "(local_relpath=? OR parent_relpath=?)"
		arguments = append(arguments, relpath)
	case svn.DepthInfinity:
		condition = "(local_relpath=? OR local_relpath LIKE ? ESCAPE '#')"
		arguments = append(arguments, descendantPattern(relpath))
	default:
		return nil, fmt.Errorf("%w: invalid revert depth %s", svn.ErrIncorrectParams, depth)
	}
	rows, err := database.sql.QueryContext(ctx, `SELECT local_relpath FROM NODES_CURRENT WHERE wc_id=? AND `+condition+` ORDER BY LENGTH(local_relpath), local_relpath`, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var current string
		if err := rows.Scan(&current); err != nil {
			return nil, err
		}
		paths = append(paths, current)
	}
	return paths, rows.Err()
}

func (database *Database) Resolve(ctx context.Context, targetPath string) error {
	return database.ResolveWithChoice(ctx, targetPath, ConflictWorking)
}

func (database *Database) ResolveWithChoice(ctx context.Context, targetPath string, choice ConflictChoice) error {
	info, err := database.Info(ctx, targetPath)
	if err != nil {
		return err
	}
	if info.Conflict != nil {
		if info.Conflict.Tree {
			switch choice {
			case ConflictWorking, ConflictMine:
			case ConflictTheirs:
				if err := database.resolveTreeWithIncoming(ctx, info); err != nil {
					return err
				}
			case ConflictBase:
				return fmt.Errorf("%w: base is not a tree-conflict resolution choice", svn.ErrClientConflictOptionNotApplicable)
			default:
				return fmt.Errorf("%w: unsupported resolution choice", svn.ErrClientConflictOptionNotApplicable)
			}
		}
		selected := ""
		if info.Conflict.Text {
			switch choice {
			case ConflictWorking:
			case ConflictMine:
				selected = info.Conflict.WorkingPath
			case ConflictBase:
				selected = info.Conflict.OldPath
			case ConflictTheirs:
				selected = info.Conflict.NewPath
			default:
				return fmt.Errorf("%w: unsupported resolution choice", svn.ErrClientConflictOptionNotApplicable)
			}
		}
		if selected != "" {
			source := filepath.Join(filepath.Dir(targetPath), filepath.FromSlash(selected))
			if err := copyFile(source, targetPath); err != nil {
				return err
			}
		}
		for _, marker := range []string{info.Conflict.OldPath, info.Conflict.NewPath, info.Conflict.WorkingPath, info.Conflict.PropertyPath} {
			if marker != "" {
				_ = os.Remove(filepath.Join(filepath.Dir(targetPath), filepath.FromSlash(marker)))
			}
		}
	}
	_, err = database.sql.ExecContext(ctx, `UPDATE ACTUAL_NODE SET conflict_old=NULL, conflict_new=NULL, conflict_working=NULL,
		prop_reject=NULL, tree_conflict_data=NULL, conflict_data=NULL WHERE wc_id=? AND local_relpath=?`, database.wcID, info.RelativePath)
	return err
}

func (database *Database) resolveTreeWithIncoming(ctx context.Context, info *Info) error {
	transaction, err := database.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `DELETE FROM NODES WHERE wc_id=? AND
		(local_relpath=? OR local_relpath LIKE ? ESCAPE '#') AND op_depth>0`, database.wcID, info.RelativePath, descendantPattern(info.RelativePath)); err != nil {
		return err
	}
	var baseCount int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM NODES WHERE wc_id=? AND local_relpath=? AND op_depth=0`, database.wcID, info.RelativePath).Scan(&baseCount); err != nil {
		return err
	}
	if baseCount == 0 {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM ACTUAL_NODE WHERE wc_id=? AND
			(local_relpath=? OR local_relpath LIKE ? ESCAPE '#')`, database.wcID, info.RelativePath, descendantPattern(info.RelativePath)); err != nil {
			return err
		}
	} else if _, err := transaction.ExecContext(ctx, `UPDATE ACTUAL_NODE SET properties=NULL WHERE wc_id=? AND
		(local_relpath=? OR local_relpath LIKE ? ESCAPE '#')`, database.wcID, info.RelativePath, descendantPattern(info.RelativePath)); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return err
	}
	if err := os.RemoveAll(info.Path); err != nil {
		return err
	}
	if baseCount == 0 {
		return nil
	}
	rows, err := database.sql.QueryContext(ctx, `SELECT local_relpath, kind FROM NODES_BASE WHERE wc_id=? AND
		(local_relpath=? OR local_relpath LIKE ? ESCAPE '#') ORDER BY local_relpath`, database.wcID, info.RelativePath, descendantPattern(info.RelativePath))
	if err != nil {
		return err
	}
	var nodes []struct{ relpath, kind string }
	for rows.Next() {
		var node struct{ relpath, kind string }
		if err := rows.Scan(&node.relpath, &node.kind); err != nil {
			rows.Close()
			return err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, node := range nodes {
		if node.kind == svn.NodeDir.String() {
			if err := os.MkdirAll(filepath.Join(database.wcRoot, filepath.FromSlash(node.relpath)), 0o755); err != nil {
				return err
			}
		} else if _, err := database.Enqueue(ctx, WorkItem(workFileInstall, node.relpath, "0", "1")); err != nil {
			return err
		}
	}
	return database.RunWorkQueue(ctx)
}

func relpathDepth(relpath string) int {
	if relpath == "" {
		return 0
	}
	return strings.Count(relpath, "/") + 1
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "#", "##")
	value = strings.ReplaceAll(value, "%", "#%")
	return strings.ReplaceAll(value, "_", "#_")
}

func descendantPattern(relpath string) string {
	if relpath == "" {
		return "%"
	}
	return escapeLike(relpath) + "/%"
}

type nodeScanner interface {
	Scan(...any) error
}

func scanNode(scanner nodeScanner, node *nodeRow) error {
	return scanner.Scan(&node.relpath, &node.opDepth, &node.reposID, &node.reposPath, &node.revision, &node.presence,
		&node.movedHere, &node.movedTo, &node.kind, &node.properties, &node.depth, &node.checksum, &node.symlinkTarget,
		&node.changedRevision, &node.changedDate, &node.changedAuthor, &node.translatedSize, &node.lastModified, &node.fileExternal)
}

func copyTree(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		return os.Symlink(target, destination)
	}
	if !info.IsDir() {
		return copyFile(source, destination)
	}
	if err := os.MkdirAll(destination, info.Mode().Perm()); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == ".svn" {
			continue
		}
		if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
