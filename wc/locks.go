package wc

import (
	"context"
	"database/sql"
	"time"

	"github.com/otuschhoff/go-svn/svn"
)

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
