package wc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
)

type Repository struct {
	ID   int64
	Root string
	UUID string
}

func Open(ctx context.Context, targetPath string, options Options) (*Database, error) {
	root, err := FindRoot(ctx, targetPath)
	if err != nil {
		return nil, err
	}
	database, err := OpenDatabase(ctx, filepath.Join(root, ".svn", "wc.db"), options)
	if err != nil {
		return nil, err
	}
	if err := database.loadRoot(ctx, root); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func FindRoot(ctx context.Context, targetPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(absolute); statErr == nil && !info.IsDir() {
		absolute = filepath.Dir(absolute)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	} else if errors.Is(statErr, os.ErrNotExist) {
		for {
			parent := filepath.Dir(absolute)
			if parent == absolute {
				break
			}
			absolute = parent
			if _, err := os.Stat(absolute); err == nil {
				break
			}
		}
	}
	for current := absolute; ; current = filepath.Dir(current) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if info, err := os.Stat(filepath.Join(current, ".svn", "wc.db")); err == nil && !info.IsDir() {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return "", fmt.Errorf("%w: %s", svn.ErrWCNotWorkingCopy, targetPath)
}

func (database *Database) loadRoot(ctx context.Context, discoveredRoot string) error {
	prepared, err := database.prepare(ctx, stmtWCROOT)
	if err != nil {
		return err
	}
	var configured sql.NullString
	if err := prepared.QueryRowContext(ctx).Scan(&database.wcID, &configured); err != nil {
		return fmt.Errorf("%w: WCROOT: %v", svn.ErrWCCorrupt, err)
	}
	database.wcRoot = discoveredRoot
	if configured.Valid && configured.String != "" {
		database.wcRoot = filepath.Clean(configured.String)
	}
	prepared, err = database.prepare(ctx, stmtRepos)
	if err != nil {
		return err
	}
	if err := prepared.QueryRowContext(ctx, database.wcID).Scan(&database.repository.ID, &database.repository.Root, &database.repository.UUID); err != nil {
		return fmt.Errorf("%w: repository metadata: %v", svn.ErrWCCorrupt, err)
	}
	return nil
}

func (database *Database) RootPath() string       { return database.wcRoot }
func (database *Database) RepositoryRoot() string { return database.repository.Root }

func (database *Database) Relocate(ctx context.Context, repositoryRoot string) error {
	repositoryRoot = strings.TrimSuffix(repositoryRoot, "/")
	parsed, err := url.Parse(repositoryRoot)
	if err != nil || parsed.Scheme == "" {
		return fmt.Errorf("%w: %s", svn.ErrRAIllegalURL, repositoryRoot)
	}
	if _, err := database.sql.ExecContext(ctx, `UPDATE REPOSITORY SET root=? WHERE id=?`, repositoryRoot, database.repository.ID); err != nil {
		return err
	}
	database.repository.Root = repositoryRoot
	return nil
}
func (database *Database) RepositoryUUID() string { return database.repository.UUID }

func (database *Database) localRelpath(targetPath string) (string, error) {
	absolute, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(database.wcRoot, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s is outside %s", svn.ErrWCPathNotFound, targetPath, database.wcRoot)
	}
	if relative == "." {
		return "", nil
	}
	return filepath.ToSlash(relative), nil
}
