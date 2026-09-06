package wc

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/skel"
)

type Conflict struct {
	Data         *skel.Node
	OldPath      string
	NewPath      string
	WorkingPath  string
	PropertyPath string
	Text         bool
	Property     bool
	Tree         bool
	TreeReason   string
	TreeAction   string
}

type actualRow struct {
	properties []byte
	changelist string
	conflict   *Conflict
}

func (database *Database) readActual(ctx context.Context, relpath string) (*actualRow, error) {
	var properties, conflictData []byte
	var oldPath, newPath, workingPath, propertyPath, changelist, treeData sql.NullString
	err := database.sql.QueryRowContext(ctx, `SELECT properties, conflict_old, conflict_new, conflict_working,
		prop_reject, changelist, tree_conflict_data, conflict_data FROM ACTUAL_NODE WHERE wc_id = ? AND local_relpath = ?`, database.wcID, relpath).Scan(
		&properties, &oldPath, &newPath, &workingPath, &propertyPath, &changelist, &treeData, &conflictData,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	actual := &actualRow{properties: properties, changelist: changelist.String}
	if oldPath.Valid || newPath.Valid || workingPath.Valid || propertyPath.Valid || treeData.Valid || len(conflictData) != 0 {
		conflict := &Conflict{OldPath: oldPath.String, NewPath: newPath.String, WorkingPath: workingPath.String, PropertyPath: propertyPath.String, Tree: treeData.Valid}
		if len(conflictData) != 0 {
			parsed, err := skel.Parse(conflictData)
			if err != nil {
				return nil, fmt.Errorf("%w: conflict skeleton: %v", svn.ErrWCCorrupt, err)
			}
			conflict.Data = parsed
			decodeConflictRecords(parsed, conflict)
		}
		actual.conflict = conflict
	}
	return actual, nil
}

func decodeConflictRecords(node *skel.Node, conflict *Conflict) {
	if node == nil || !node.IsList() {
		return
	}
	if len(node.Children) > 0 && node.Children[0].IsAtom() {
		switch string(node.Children[0].Atom) {
		case "text":
			conflict.Text = true
			if len(node.Children) > 1 && node.Children[1].IsList() && len(node.Children[1].Children) >= 3 {
				markers := node.Children[1].Children
				conflict.OldPath = string(markers[0].Atom)
				conflict.WorkingPath = string(markers[1].Atom)
				conflict.NewPath = string(markers[2].Atom)
			}
		case "prop":
			conflict.Property = true
			if len(node.Children) > 1 && node.Children[1].IsList() && len(node.Children[1].Children) > 0 {
				conflict.PropertyPath = string(node.Children[1].Children[0].Atom)
			}
		case "tree":
			conflict.Tree = true
			if len(node.Children) >= 4 {
				conflict.TreeReason = string(node.Children[2].Atom)
				conflict.TreeAction = string(node.Children[3].Atom)
			}
		}
	}
	for _, child := range node.Children {
		decodeConflictRecords(child, conflict)
	}
}

func parseProperties(data []byte) (svn.Props, error) {
	if len(data) == 0 {
		return make(svn.Props), nil
	}
	parsed, err := skel.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: properties skeleton: %v", svn.ErrWCCorrupt, err)
	}
	properties, err := skel.ProplistToProps(parsed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrWCCorrupt, err)
	}
	return properties, nil
}
