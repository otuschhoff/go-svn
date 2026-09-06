package wc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/otuschhoff/go-svn/props"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/skel"
)

func (database *Database) reconcileUpdateProperties(ctx context.Context, editor *updateEditor, editorPath string, kind svn.NodeKind, base, mine, theirs svn.Props) error {
	merged := theirs.Clone()
	conflicted := make(svn.Props)
	original := make(svn.Props)
	mineConflict := make(svn.Props)
	theirsConflict := make(svn.Props)
	names := propertyNames(base, mine, theirs)
	for _, name := range names {
		baseValue, baseOK := base[name]
		mineValue, mineOK := mine[name]
		theirsValue, theirsOK := theirs[name]
		switch {
		case propertyEqual(mineValue, mineOK, baseValue, baseOK):
		case propertyEqual(theirsValue, theirsOK, baseValue, baseOK), propertyEqual(mineValue, mineOK, theirsValue, theirsOK):
			setPropertyValue(merged, name, mineValue, mineOK)
		default:
			switch editor.options.Accept {
			case ConflictBase:
				setPropertyValue(merged, name, baseValue, baseOK)
			case ConflictTheirs:
			default:
				setPropertyValue(merged, name, mineValue, mineOK)
			}
			if editor.options.Accept == ConflictPostpone {
				conflicted[name] = nil
				setPropertyValue(original, name, baseValue, baseOK)
				setPropertyValue(mineConflict, name, mineValue, mineOK)
				setPropertyValue(theirsConflict, name, theirsValue, theirsOK)
			}
		}
	}
	relpath := editor.relpath(editorPath)
	parent := path.Dir(relpath)
	if parent == "." {
		parent = ""
	}
	if propsEqual(merged, theirs) && len(conflicted) == 0 {
		_, err := database.sql.ExecContext(ctx, `UPDATE ACTUAL_NODE SET properties=NULL WHERE wc_id=? AND local_relpath=?`, database.wcID, relpath)
		if err == nil && kind == svn.NodeDir {
			err = database.recordUpdateExternals(ctx, editor, editorPath, theirs[props.Externals])
		}
		return err
	}
	_, err := database.sql.ExecContext(ctx, `INSERT INTO ACTUAL_NODE (wc_id, local_relpath, parent_relpath, properties)
		VALUES (?, ?, ?, ?) ON CONFLICT(wc_id, local_relpath) DO UPDATE SET properties=excluded.properties`,
		database.wcID, relpath, nullableParent(relpath, parent), propertyBytes(merged))
	if err != nil || len(conflicted) == 0 {
		if err == nil && kind == svn.NodeDir {
			err = database.recordUpdateExternals(ctx, editor, editorPath, theirs[props.Externals])
		}
		return err
	}
	prejName := filepath.Base(editor.localPath(editorPath)) + ".prej"
	reject := propertyRejectContents(conflicted, original, mineConflict, theirsConflict)
	if err := os.WriteFile(filepath.Join(filepath.Dir(editor.localPath(editorPath)), prejName), reject, 0o644); err != nil {
		return err
	}
	record := skel.NewList(
		skel.NewString("prop"), skel.NewList(skel.NewString(prejName)), skel.PropsToProplist(conflicted),
		skel.PropsToProplist(original), skel.PropsToProplist(mineConflict), skel.PropsToProplist(theirsConflict),
	)
	return database.appendConflictRecord(ctx, editor, editorPath, kind, record)
}

func (database *Database) recordUpdateExternals(ctx context.Context, editor *updateEditor, editorPath string, value []byte) error {
	definitionPath := editor.relpath(editorPath)
	if _, err := database.sql.ExecContext(ctx, `DELETE FROM EXTERNALS WHERE wc_id=? AND def_local_relpath=?`, database.wcID, definitionPath); err != nil {
		return err
	}
	if editor.options.IgnoreExternals || len(value) == 0 {
		return nil
	}
	definitionReposPath := editor.repositoryPath(editorPath)
	for _, rawLine := range strings.Split(string(value), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		operativeRevision := ""
		if len(fields) > 0 && fields[0] == "-r" {
			if len(fields) < 4 {
				continue
			}
			operativeRevision, fields = fields[1], fields[2:]
		} else if len(fields) > 0 && strings.HasPrefix(fields[0], "-r") {
			operativeRevision, fields = strings.TrimPrefix(fields[0], "-r"), fields[1:]
		}
		if len(fields) != 2 {
			continue
		}
		remote, local := fields[0], fields[1]
		if !externalURLLike(remote) {
			local, remote = remote, local
		}
		local = path.Clean(local)
		if local == "." || path.IsAbs(local) || local == ".." || strings.HasPrefix(local, "../") {
			continue
		}
		pegRevision := ""
		if base, peg, ok := strings.Cut(remote, "@"); ok && base != "" {
			if _, err := strconv.ParseInt(peg, 10, 64); err == nil {
				remote, pegRevision = base, peg
			}
		}
		repositoryPath, ok := sameRepositoryExternalPath(database.repository.Root, definitionReposPath, remote)
		if !ok {
			continue
		}
		localRelpath := path.Join(definitionPath, local)
		parent := path.Dir(localRelpath)
		if parent == "." {
			parent = ""
		}
		_, err := database.sql.ExecContext(ctx, `INSERT INTO EXTERNALS
			(wc_id, local_relpath, parent_relpath, repos_id, presence, kind, def_local_relpath, def_repos_relpath,
			def_operational_revision, def_revision) VALUES (?, ?, ?, ?, 'normal', 'dir', ?, ?, NULLIF(?, ''), NULLIF(?, ''))
			ON CONFLICT(wc_id, local_relpath) DO UPDATE SET parent_relpath=excluded.parent_relpath, repos_id=excluded.repos_id,
			presence=excluded.presence, kind=excluded.kind, def_local_relpath=excluded.def_local_relpath,
			def_repos_relpath=excluded.def_repos_relpath, def_operational_revision=excluded.def_operational_revision,
			def_revision=excluded.def_revision`, database.wcID, localRelpath, nullableParent(localRelpath, parent), database.repository.ID,
			definitionPath, repositoryPath, operativeRevision, pegRevision)
		if err != nil {
			return err
		}
	}
	return nil
}

func externalURLLike(value string) bool {
	return strings.Contains(value, "://") || strings.HasPrefix(value, "^") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "./")
}

func sameRepositoryExternalPath(repositoryRoot, definitionReposPath, externalURL string) (string, bool) {
	switch {
	case strings.HasPrefix(externalURL, "^/"):
		return strings.TrimPrefix(externalURL, "^/"), true
	case strings.HasPrefix(externalURL, repositoryRoot+"/"):
		return strings.TrimPrefix(externalURL, repositoryRoot+"/"), true
	case strings.Contains(externalURL, "://") || strings.HasPrefix(externalURL, "//") || strings.HasPrefix(externalURL, "/"):
		return "", false
	default:
		return path.Clean(path.Join(definitionReposPath, externalURL)), true
	}
}

func (database *Database) appendConflictRecord(ctx context.Context, editor *updateEditor, editorPath string, kind svn.NodeKind, record *skel.Node) error {
	relpath := editor.relpath(editorPath)
	var data []byte
	_ = database.sql.QueryRowContext(ctx, `SELECT conflict_data FROM ACTUAL_NODE WHERE wc_id=? AND local_relpath=?`, database.wcID, relpath).Scan(&data)
	var conflict *skel.Node
	if len(data) != 0 {
		parsed, err := skel.Parse(data)
		if err != nil {
			return err
		}
		conflict = parsed
	} else {
		version := func(revision svn.Revnum) *skel.Node {
			return skel.NewList(skel.NewString("subversion"), skel.NewString(database.repository.Root), skel.NewString(database.repository.UUID),
				skel.NewString(editor.repositoryPath(editorPath)), skel.NewString(strconv.FormatInt(int64(revision), 10)),
				skel.NewString(strconv.FormatInt(int64(revision), 10)), skel.NewString(kind.String()))
		}
		conflict = skel.NewList(
			skel.NewList(skel.NewString("update"), skel.NewList(version(editor.target-1), version(editor.target))),
			skel.NewList(),
		)
	}
	if len(conflict.Children) != 2 || !conflict.Children[1].IsList() {
		return fmt.Errorf("%w: invalid conflict skeleton", svn.ErrWCCorrupt)
	}
	conflict.Children[1].Children = append(conflict.Children[1].Children, record)
	serialized, err := conflict.MarshalBinary()
	if err != nil {
		return err
	}
	parent := path.Dir(relpath)
	if parent == "." {
		parent = ""
	}
	_, err = database.sql.ExecContext(ctx, `INSERT INTO ACTUAL_NODE (wc_id, local_relpath, parent_relpath, conflict_data)
		VALUES (?, ?, ?, ?) ON CONFLICT(wc_id, local_relpath) DO UPDATE SET conflict_data=excluded.conflict_data`,
		database.wcID, relpath, nullableParent(relpath, parent), serialized)
	return err
}

func (database *Database) recordTreeConflict(ctx context.Context, editor *updateEditor, editorPath string, kind svn.NodeKind, reason, action string) error {
	record := skel.NewList(skel.NewString("tree"), skel.NewList(), skel.NewString(reason), skel.NewString(action))
	return database.appendConflictRecord(ctx, editor, editorPath, kind, record)
}

func propertyNames(properties ...svn.Props) []string {
	seen := make(map[string]bool)
	for _, values := range properties {
		for name := range values {
			seen[name] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func propertyEqual(left []byte, leftOK bool, right []byte, rightOK bool) bool {
	return leftOK == rightOK && (!leftOK || bytes.Equal(left, right))
}

func setPropertyValue(properties svn.Props, name string, value []byte, present bool) {
	if present {
		properties[name] = append([]byte(nil), value...)
	} else {
		delete(properties, name)
	}
}

func propsEqual(left, right svn.Props) bool {
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		if !bytes.Equal(value, right[name]) {
			return false
		}
	}
	return true
}

func propertyRejectContents(names, original, mine, theirs svn.Props) []byte {
	var output bytes.Buffer
	for _, name := range propertyNames(names) {
		fmt.Fprintf(&output, "Conflict for property '%s':\n", name)
		output.WriteString("<<<<<<< (local property value)\n")
		output.Write(mine[name])
		output.WriteString("\n||||||| (incoming 'changed from' value)\n")
		output.Write(original[name])
		output.WriteString("\n=======\n")
		output.Write(theirs[name])
		output.WriteString("\n>>>>>>> (incoming 'changed to' value)\n")
	}
	return output.Bytes()
}
