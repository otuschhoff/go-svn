package wc

import (
	"context"
	"fmt"

	"github.com/otuschhoff/go-svn/svn"
)

type External struct {
	LocalPath           string
	ParentPath          string
	RepositoryID        int64
	Presence            Presence
	Kind                svn.NodeKind
	DefinitionPath      string
	DefinitionReposPath string
	OperativeRevision   svn.Revnum
	PegRevision         svn.Revnum
}

func (database *Database) Externals(ctx context.Context) ([]External, error) {
	rows, err := database.sql.QueryContext(ctx, `SELECT local_relpath, parent_relpath, COALESCE(repos_id, 0),
		presence, kind, def_local_relpath, def_repos_relpath,
		COALESCE(def_operational_revision, -1), COALESCE(def_revision, -1)
		FROM EXTERNALS WHERE wc_id = ? ORDER BY local_relpath`, database.wcID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []External
	for rows.Next() {
		var external External
		var kind string
		if err := rows.Scan(&external.LocalPath, &external.ParentPath, &external.RepositoryID, &external.Presence, &kind, &external.DefinitionPath, &external.DefinitionReposPath, &external.OperativeRevision, &external.PegRevision); err != nil {
			return nil, err
		}
		external.Kind, err = svn.ParseNodeKind(kind)
		if err != nil {
			return nil, fmt.Errorf("%w: external kind: %v", svn.ErrWCCorrupt, err)
		}
		result = append(result, external)
	}
	return result, rows.Err()
}
