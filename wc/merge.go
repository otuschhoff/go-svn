package wc

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/skel"
)

func (database *Database) RecordPropertyConflict(ctx context.Context, targetPath string, propertyNames []string, mine, theirs svn.Props) error {
	info, err := database.Info(ctx, targetPath)
	if err != nil {
		return err
	}
	rejectName := filepath.Base(targetPath) + ".prej"
	var reject bytes.Buffer
	for _, name := range propertyNames {
		fmt.Fprintf(&reject, "Trying to change property '%s'\n", name)
		fmt.Fprintf(&reject, "but the property has been locally changed from %q to %q.\n", mine[name], theirs[name])
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(targetPath), rejectName), reject.Bytes(), 0o644); err != nil {
		return err
	}
	root, err := database.Info(ctx, database.wcRoot)
	if err != nil {
		return err
	}
	editor := &updateEditor{database: database, repositoryBase: root.RepositoryPath, target: info.Revision}
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return err
	}
	record := skel.NewList(skel.NewString("prop"), skel.NewList(skel.NewString(rejectName)))
	return database.appendConflictRecord(ctx, editor, relpath, info.Kind, record)
}

func (database *Database) RecordTreeConflict(ctx context.Context, targetPath string, kind svn.NodeKind, reason, action string, revision svn.Revnum) error {
	root, err := database.Info(ctx, database.wcRoot)
	if err != nil {
		return err
	}
	editor := &updateEditor{database: database, repositoryBase: root.RepositoryPath, target: revision}
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return err
	}
	return database.recordTreeConflict(ctx, editor, relpath, kind, reason, action)
}

func (database *Database) RecordTextConflict(ctx context.Context, targetPath string, base, mine, theirs []byte, start, end svn.Revnum) error {
	info, err := database.Info(ctx, targetPath)
	if err != nil {
		return err
	}
	baseName := filepath.Base(targetPath)
	oldMarker := baseName + ".merge-left.r" + strconv.FormatInt(int64(start), 10)
	mineMarker := baseName + ".working"
	newMarker := baseName + ".merge-right.r" + strconv.FormatInt(int64(end), 10)
	for name, contents := range map[string][]byte{oldMarker: base, mineMarker: mine, newMarker: theirs} {
		if err := os.WriteFile(filepath.Join(filepath.Dir(targetPath), name), contents, 0o644); err != nil {
			return err
		}
	}
	root, err := database.Info(ctx, database.wcRoot)
	if err != nil {
		return err
	}
	editor := &updateEditor{database: database, repositoryBase: root.RepositoryPath, target: end}
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return err
	}
	record := skel.NewList(skel.NewString("text"), skel.NewList(skel.NewString(oldMarker), skel.NewString(mineMarker), skel.NewString(newMarker)))
	return database.appendConflictRecord(ctx, editor, relpath, info.Kind, record)
}
