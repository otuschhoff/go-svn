package wc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/otuschhoff/go-svn/svn"
)

func (database *Database) RecordLock(ctx context.Context, repositoryPath string, lock *svn.Lock) error {
	if lock == nil || lock.Token == "" {
		return fmt.Errorf("%w: lock token is required", svn.ErrIncorrectParams)
	}
	var date any
	if !lock.CreationDate.IsZero() {
		date = lock.CreationDate.UnixMicro()
	}
	_, err := database.sql.ExecContext(ctx, `INSERT INTO LOCK (repos_id, repos_relpath, lock_token, lock_owner, lock_comment, lock_date)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(repos_id, repos_relpath) DO UPDATE SET
		lock_token=excluded.lock_token, lock_owner=excluded.lock_owner, lock_comment=excluded.lock_comment, lock_date=excluded.lock_date`,
		database.repository.ID, strings.TrimPrefix(repositoryPath, "/"), lock.Token, lock.Owner, lock.Comment, date)
	return err
}

func (database *Database) RemoveLock(ctx context.Context, repositoryPath string) error {
	_, err := database.sql.ExecContext(ctx, `DELETE FROM LOCK WHERE repos_id=? AND repos_relpath=?`, database.repository.ID, strings.TrimPrefix(repositoryPath, "/"))
	return err
}

func (database *Database) readLock(ctx context.Context, repositoryID int64, repositoryPath string) (*svn.Lock, error) {
	if repositoryID == 0 || repositoryPath == "" {
		return nil, nil
	}
	var token, owner string
	var comment sql.NullString
	var date sql.NullInt64
	err := database.sql.QueryRowContext(ctx, `SELECT lock_token, COALESCE(lock_owner, ''), lock_comment, lock_date
		FROM LOCK WHERE repos_id = ? AND repos_relpath = ?`, repositoryID, repositoryPath).Scan(&token, &owner, &comment, &date)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lock := &svn.Lock{Path: "/" + repositoryPath, Token: token, Owner: owner, Comment: comment.String}
	if date.Valid {
		lock.CreationDate = time.UnixMicro(date.Int64).UTC()
	}
	return lock, nil
}
