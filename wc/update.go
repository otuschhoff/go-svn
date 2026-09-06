package wc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

func Checkout(ctx context.Context, session ra.Session, destination string, revision svn.Revnum, options UpdateOptions) (*Database, error) {
	if session == nil {
		return nil, fmt.Errorf("%w: nil RA session", svn.ErrIncorrectParams)
	}
	if !revision.IsValid() {
		var err error
		revision, err = session.LatestRevision(ctx)
		if err != nil {
			return nil, err
		}
	}
	root, err := session.RepositoryRoot(ctx)
	if err != nil {
		return nil, err
	}
	uuid, err := session.UUID(ctx)
	if err != nil {
		return nil, err
	}
	database, err := Create(ctx, destination, CreateOptions{
		RepositoryRoot: root, RepositoryUUID: uuid, RepositoryPath: sessionPath(root, session.URL()),
		Revision: 0, Depth: options.Depth,
	})
	if err != nil {
		return nil, err
	}
	if _, err := database.Update(ctx, session, destination, revision, options); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func (database *Database) Update(ctx context.Context, session ra.Session, targetPath string, revision svn.Revnum, options UpdateOptions) (svn.Revnum, error) {
	if !revision.IsValid() {
		var err error
		revision, err = session.LatestRevision(ctx)
		if err != nil {
			return svn.InvalidRevnum, err
		}
	}
	target, err := database.localRelpath(targetPath)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	editorAnchor := targetPath
	info, infoErr := database.Info(ctx, targetPath)
	isFile := infoErr == nil && info.Kind != svn.NodeDir
	if infoErr != nil {
		kind, checkErr := session.CheckPath(ctx, target, revision)
		if checkErr != nil {
			return svn.InvalidRevnum, checkErr
		}
		isFile = kind == svn.NodeFile
	}
	if isFile {
		editorAnchor = filepath.Dir(targetPath)
	}
	if options.SetDepth != nil {
		options.Depth = *options.SetDepth
		if err := database.checkDepthPrune(ctx, target, options.Depth, options.Force); err != nil {
			return svn.InvalidRevnum, err
		}
	}
	editor, err := NewUpdateEditor(ctx, database, editorAnchor, options)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	reporter, err := session.DoUpdate(ctx, revision, target, options.Depth, true, false, editor)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	if err := database.Crawl(ctx, targetPath, options.Depth, reporter); err != nil {
		_ = reporter.AbortReport(ctx)
		return svn.InvalidRevnum, err
	}
	if options.SetDepth != nil {
		if err := database.pruneDepth(ctx, target, options.Depth); err != nil {
			return svn.InvalidRevnum, err
		}
	}
	return revision, nil
}

func (database *Database) Switch(ctx context.Context, session ra.Session, targetPath, switchURL string, revision svn.Revnum, options UpdateOptions) (svn.Revnum, error) {
	if !revision.IsValid() {
		var err error
		revision, err = session.LatestRevision(ctx)
		if err != nil {
			return svn.InvalidRevnum, err
		}
	}
	target, err := database.localRelpath(targetPath)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	editorAnchor := targetPath
	info, infoErr := database.Info(ctx, targetPath)
	isFile := infoErr == nil && info.Kind != svn.NodeDir
	if isFile {
		editorAnchor = filepath.Dir(targetPath)
		options.switchTarget = filepath.Base(targetPath)
	}
	options.SwitchURL = switchURL
	if options.SetDepth != nil {
		options.Depth = *options.SetDepth
		if err := database.checkDepthPrune(ctx, target, options.Depth, options.Force); err != nil {
			return svn.InvalidRevnum, err
		}
	}
	editor, err := NewUpdateEditor(ctx, database, editorAnchor, options)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	reporter, err := session.DoSwitch(ctx, revision, target, options.Depth, switchURL, true, false, editor)
	if err != nil {
		return svn.InvalidRevnum, err
	}
	if err := database.Crawl(ctx, targetPath, options.Depth, reporter); err != nil {
		_ = reporter.AbortReport(ctx)
		return svn.InvalidRevnum, err
	}
	if options.SetDepth != nil {
		if err := database.pruneDepth(ctx, target, options.Depth); err != nil {
			return svn.InvalidRevnum, err
		}
	}
	return revision, nil
}

func (database *Database) depthPruneCandidates(ctx context.Context, target string, depth svn.Depth) ([]string, error) {
	rows, err := database.sql.QueryContext(ctx, `SELECT local_relpath, kind FROM NODES_CURRENT WHERE wc_id=? ORDER BY local_relpath`, database.wcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var relpath, kind string
		if err := rows.Scan(&relpath, &kind); err != nil {
			return nil, err
		}
		if relpath != target && !withinDepth(target, relpath, depth, kind != svn.NodeDir.String()) {
			result = append(result, relpath)
		}
	}
	return result, rows.Err()
}

func (database *Database) checkDepthPrune(ctx context.Context, target string, depth svn.Depth, force bool) error {
	if force {
		return nil
	}
	candidates, err := database.depthPruneCandidates(ctx, target, depth)
	if err != nil || len(candidates) == 0 {
		return err
	}
	removed := make(map[string]bool, len(candidates))
	for _, relpath := range candidates {
		removed[relpath] = true
	}
	return database.Status(ctx, filepath.Join(database.wcRoot, filepath.FromSlash(target)), StatusOptions{Depth: svn.DepthInfinity}, func(status *Status) error {
		if removed[status.RelativePath] && (status.NodeStatus != StatusNormal || status.TextStatus == StatusModified || status.PropertyStatus == StatusModified || status.Conflicted) {
			return fmt.Errorf("%w: %s has local modifications", svn.ErrClientModified, status.Path)
		}
		return nil
	})
}

func (database *Database) pruneDepth(ctx context.Context, target string, depth svn.Depth) error {
	candidates, err := database.depthPruneCandidates(ctx, target, depth)
	if err != nil || len(candidates) == 0 {
		return err
	}
	transaction, err := database.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for _, relpath := range candidates {
		if _, err := transaction.ExecContext(ctx, `DELETE FROM ACTUAL_NODE WHERE wc_id=? AND local_relpath=?`, database.wcID, relpath); err != nil {
			return err
		}
		if _, err := transaction.ExecContext(ctx, `DELETE FROM NODES WHERE wc_id=? AND local_relpath=?`, database.wcID, relpath); err != nil {
			return err
		}
	}
	if depth == svn.DepthImmediates {
		if _, err := transaction.ExecContext(ctx, `UPDATE NODES SET depth='empty' WHERE wc_id=? AND op_depth=0 AND kind='dir' AND parent_relpath=?`, database.wcID, target); err != nil {
			return err
		}
	}
	if err := transaction.Commit(); err != nil {
		return err
	}
	sort.Slice(candidates, func(left, right int) bool { return len(candidates[left]) < len(candidates[right]) })
	removed := make([]string, 0, len(candidates))
	for _, relpath := range candidates {
		nested := false
		for _, parent := range removed {
			if relpath != parent && len(relpath) > len(parent) && relpath[:len(parent)+1] == parent+"/" {
				nested = true
				break
			}
		}
		if !nested {
			removed = append(removed, relpath)
		}
	}
	for _, relpath := range removed {
		if err := os.RemoveAll(filepath.Join(database.wcRoot, filepath.FromSlash(relpath))); err != nil {
			return err
		}
	}
	return nil
}

func sessionPath(root, current string) string {
	if len(current) <= len(root) {
		return ""
	}
	return current[len(root)+1:]
}
