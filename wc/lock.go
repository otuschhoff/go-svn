package wc

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/otuschhoff/go-svn/svn"
)

type WCLock struct {
	database *Database
	relpath  string
	released bool
}

func (database *Database) AcquireLock(ctx context.Context, targetPath string, levels int) (*WCLock, error) {
	if !database.writable {
		return nil, fmt.Errorf("%w: database is read-only", svn.ErrWCNotLocked)
	}
	relpath, err := database.localRelpath(targetPath)
	if err != nil {
		return nil, err
	}
	transaction, err := database.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer transaction.Rollback()
	rows, err := transaction.QueryContext(ctx, `SELECT local_dir_relpath, locked_levels FROM WC_LOCK WHERE wc_id = ?`, database.wcID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var existing string
		var existingLevels int
		if err := rows.Scan(&existing, &existingLevels); err != nil {
			rows.Close()
			return nil, err
		}
		if lockCovers(existing, existingLevels, relpath) || lockCovers(relpath, levels, existing) {
			rows.Close()
			return nil, fmt.Errorf("%w: %s", svn.ErrWCLocked, targetPath)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO WC_LOCK (wc_id, local_dir_relpath, locked_levels) VALUES (?, ?, ?)`, database.wcID, relpath, levels); err != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrWCLocked, err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, err
	}
	return &WCLock{database: database, relpath: relpath}, nil
}

func (lock *WCLock) Release(ctx context.Context) error {
	if lock == nil || lock.released {
		return nil
	}
	result, err := lock.database.sql.ExecContext(ctx, `DELETE FROM WC_LOCK WHERE wc_id = ? AND local_dir_relpath = ?`, lock.database.wcID, lock.relpath)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("%w: %s", svn.ErrWCNotLocked, lock.relpath)
	}
	lock.released = true
	return nil
}

func lockCovers(lockPath string, levels int, candidate string) bool {
	if lockPath == candidate {
		return true
	}
	prefix := lockPath
	if prefix != "" {
		prefix += "/"
	}
	if !strings.HasPrefix(candidate, prefix) {
		return false
	}
	remainder := strings.TrimPrefix(candidate, prefix)
	distance := strings.Count(path.Clean(remainder), "/") + 1
	return levels < 0 || distance <= levels
}
