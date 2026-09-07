package wc

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
)

type CreateOptions struct {
	RepositoryRoot string
	RepositoryUUID string
	RepositoryPath string
	Revision       svn.Revnum
	Depth          svn.Depth
}

func Create(ctx context.Context, rootPath string, options CreateOptions) (*Database, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.RepositoryRoot == "" || options.RepositoryUUID == "" || !options.Revision.IsValid() {
		return nil, fmt.Errorf("%w: repository root, UUID, and revision are required", svn.ErrIncorrectParams)
	}
	if _, err := url.ParseRequestURI(options.RepositoryRoot); err != nil {
		return nil, fmt.Errorf("%w: repository root: %v", svn.ErrRAIllegalURL, err)
	}
	if options.Depth == svn.DepthUnknown {
		options.Depth = svn.DepthInfinity
	}
	absolute, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	adminPath := filepath.Join(absolute, ".svn")
	if err := os.MkdirAll(filepath.Join(adminPath, "pristine"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(adminPath, "tmp"), 0o755); err != nil {
		return nil, err
	}
	for _, name := range []string{"format", "entries"} {
		if err := os.WriteFile(filepath.Join(adminPath, name), []byte("12\n"), 0o644); err != nil {
			return nil, err
		}
	}
	databasePath := filepath.Join(adminPath, "wc.db")
	dsn := sqliteDSN(databasePath, "rwc")
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := handle.ExecContext(ctx, schema31); err != nil {
		handle.Close()
		return nil, fmt.Errorf("create working copy schema: %w", err)
	}
	transaction, err := handle.BeginTx(ctx, nil)
	if err != nil {
		handle.Close()
		return nil, err
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(ctx, `INSERT INTO REPOSITORY (root, uuid) VALUES (?, ?)`, strings.TrimSuffix(options.RepositoryRoot, "/"), options.RepositoryUUID)
	if err != nil {
		handle.Close()
		return nil, err
	}
	repositoryID, _ := result.LastInsertId()
	result, err = transaction.ExecContext(ctx, `INSERT INTO WCROOT (local_abspath) VALUES (NULL)`)
	if err != nil {
		handle.Close()
		return nil, err
	}
	wcID, _ := result.LastInsertId()
	_, err = transaction.ExecContext(ctx, `INSERT INTO NODES
		(wc_id, local_relpath, op_depth, parent_relpath, repos_id, repos_path, revision, presence, kind, properties, depth, changed_revision)
		VALUES (?, '', 0, NULL, ?, ?, ?, 'incomplete', 'dir', X'2829', ?, ?)`,
		wcID, repositoryID, strings.Trim(options.RepositoryPath, "/"), options.Revision, options.Depth.String(), options.Revision)
	if err == nil {
		err = transaction.Commit()
	}
	closeErr := handle.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return Open(ctx, absolute, Options{Writable: true})
}
