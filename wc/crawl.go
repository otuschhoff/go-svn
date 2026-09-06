package wc

import (
	"context"
	"database/sql"
	"path"
	"strings"

	"github.com/otuschhoff/go-svn/ra"
	"github.com/otuschhoff/go-svn/svn"
)

type reportNode struct {
	row  *nodeRow
	lock string
}

func (database *Database) Crawl(ctx context.Context, targetPath string, depth svn.Depth, reporter ra.Reporter) (resultErr error) {
	return database.crawl(ctx, targetPath, depth, reporter, false, false)
}

func (database *Database) crawl(ctx context.Context, targetPath string, depth svn.Depth, reporter ra.Reporter, emptyRoot, suppressRootLink bool) (resultErr error) {
	target, err := database.localRelpath(targetPath)
	if err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			_ = reporter.AbortReport(ctx)
		}
	}()
	rows, err := database.sql.QueryContext(ctx, `SELECT `+nodeColumns+` FROM NODES_BASE WHERE wc_id = ? ORDER BY local_relpath`, database.wcID)
	if err != nil {
		return err
	}
	var nodes []reportNode
	for rows.Next() {
		row := &nodeRow{}
		if err := rows.Scan(&row.relpath, &row.opDepth, &row.reposID, &row.reposPath, &row.revision, &row.presence, &row.movedHere, &row.movedTo, &row.kind, &row.properties, &row.depth, &row.checksum, &row.symlinkTarget, &row.changedRevision, &row.changedDate, &row.changedAuthor, &row.translatedSize, &row.lastModified, &row.fileExternal); err != nil {
			return err
		}
		if !withinDepth(target, row.relpath, depth, row.kind != svn.NodeDir.String()) {
			continue
		}
		nodes = append(nodes, reportNode{row: row})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for index := range nodes {
		err := database.sql.QueryRowContext(ctx, `SELECT lock_token FROM LOCK WHERE repos_id = ? AND repos_relpath = ?`, nodes[index].row.reposID, nodes[index].row.reposPath).Scan(&nodes[index].lock)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
	}
	if len(nodes) == 0 {
		if emptyRoot {
			err = reporter.SetPath(ctx, "", 0, depth, true, "")
		} else {
			err = reporter.DeletePath(ctx, "")
		}
		if err != nil {
			return err
		}
		return reporter.FinishReport(ctx)
	}
	revisions := make(map[string]svn.Revnum, len(nodes))
	for index, item := range nodes {
		row := item.row
		reportPath := strings.TrimPrefix(strings.TrimPrefix(row.relpath, target), "/")
		nodeDepth := svn.DepthInfinity
		if row.depth != "" {
			nodeDepth, err = svn.ParseDepth(row.depth)
			if err != nil {
				return err
			}
		}
		if row.presence == string(PresenceExcluded) || row.presence == string(PresenceServerExcluded) || row.presence == string(PresenceNotPresent) {
			if err := reporter.SetPath(ctx, reportPath, svn.InvalidRevnum, svn.DepthExclude, false, ""); err != nil {
				return err
			}
			continue
		}
		parent := path.Dir(row.relpath)
		if parent == "." {
			parent = ""
		}
		parentReposPath := parentRepositoryPath(nodes, parent)
		if row.relpath != "" && parentReposPath == "" {
			if err := database.sql.QueryRowContext(ctx, `SELECT repos_path FROM NODES_BASE WHERE wc_id=? AND local_relpath=?`, database.wcID, parent).Scan(&parentReposPath); err != nil {
				return err
			}
		}
		expectedReposPath := path.Join(parentReposPath, path.Base(row.relpath))
		switched := row.relpath != "" && expectedReposPath != row.reposPath && !(suppressRootLink && reportPath == "")
		parentRevision, parentKnown := revisions[parent]
		mustReport := index == 0 || switched || !parentKnown || row.revision != parentRevision || item.lock != "" || row.presence == string(PresenceIncomplete) || (row.kind == svn.NodeDir.String() && nodeDepth != svn.DepthInfinity)
		if mustReport {
			startEmpty := row.presence == string(PresenceIncomplete)
			if switched {
				url := strings.TrimSuffix(database.repository.Root, "/") + "/" + strings.TrimPrefix(row.reposPath, "/")
				err = reporter.LinkPath(ctx, reportPath, url, row.revision, nodeDepth, startEmpty, item.lock)
			} else {
				err = reporter.SetPath(ctx, reportPath, row.revision, nodeDepth, startEmpty, item.lock)
			}
			if err != nil {
				return err
			}
		}
		revisions[row.relpath] = row.revision
	}
	return reporter.FinishReport(ctx)
}

func parentRepositoryPath(nodes []reportNode, relpath string) string {
	for _, item := range nodes {
		if item.row.relpath == relpath {
			return item.row.reposPath
		}
	}
	return ""
}
