package wc

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"

	"github.com/otuschhoff/go-svn/svn"
)

const CurrentFormat = 31

type Options struct {
	AllowNewerFormat bool
	Writable         bool
}

type statement string

const (
	stmtWCROOT statement = `SELECT id, local_abspath FROM WCROOT ORDER BY id LIMIT 1`
	stmtRepos  statement = `SELECT r.id, r.root, r.uuid
		FROM REPOSITORY r JOIN NODES n ON n.repos_id = r.id
		WHERE n.wc_id = ? AND n.local_relpath = '' AND n.op_depth = 0`
)

type Database struct {
	sql        *sql.DB
	path       string
	format     int
	wcRoot     string
	wcID       int64
	repository Repository
	statements map[statement]*sql.Stmt
	inherited  map[string][]InheritedProperties
	writable   bool
	mu         sync.Mutex
}

func OpenDatabase(ctx context.Context, databasePath string, options Options) (*Database, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, err
	}
	mode := "ro"
	if options.Writable {
		mode = "rw"
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=" + mode}).String()
	handle, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open working copy database: %w", err)
	}
	handle.SetMaxOpenConns(1)
	handle.SetMaxIdleConns(1)
	database := &Database{sql: handle, path: absolute, statements: make(map[statement]*sql.Stmt), inherited: make(map[string][]InheritedProperties), writable: options.Writable}
	if err := database.initialize(ctx, options); err != nil {
		handle.Close()
		return nil, err
	}
	return database, nil
}

func (database *Database) initialize(ctx context.Context, options Options) error {
	pragmas := []string{
		"PRAGMA foreign_keys = OFF",
		"PRAGMA locking_mode = NORMAL",
		"PRAGMA journal_mode = DELETE",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA case_sensitive_like = ON",
		"PRAGMA temp_store = MEMORY",
		"PRAGMA busy_timeout = 10000",
	}
	if !options.Writable {
		pragmas = append(pragmas, "PRAGMA query_only = ON")
	}
	for _, pragma := range pragmas {
		if _, err := database.sql.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("configure working copy database: %w", err)
		}
	}
	if err := database.sql.QueryRowContext(ctx, "PRAGMA user_version").Scan(&database.format); err != nil {
		return fmt.Errorf("read working copy format: %w", err)
	}
	if database.format < CurrentFormat {
		return fmt.Errorf("%w: working copy format %d, expected %d", svn.ErrWCUpgradeRequired, database.format, CurrentFormat)
	}
	if database.format > CurrentFormat && !options.AllowNewerFormat {
		return fmt.Errorf("%w: working copy format %d, expected %d", svn.ErrWCUnsupportedFormat, database.format, CurrentFormat)
	}
	return nil
}

func (database *Database) DatabasePath() string { return database.path }
func (database *Database) Format() int          { return database.format }

func (database *Database) prepare(ctx context.Context, query statement) (*sql.Stmt, error) {
	database.mu.Lock()
	defer database.mu.Unlock()
	if prepared := database.statements[query]; prepared != nil {
		return prepared, nil
	}
	prepared, err := database.sql.PrepareContext(ctx, string(query))
	if err != nil {
		return nil, err
	}
	database.statements[query] = prepared
	return prepared, nil
}

func (database *Database) Close() error {
	database.mu.Lock()
	defer database.mu.Unlock()
	var result error
	for query, prepared := range database.statements {
		if err := prepared.Close(); err != nil && result == nil {
			result = err
		}
		delete(database.statements, query)
	}
	if err := database.sql.Close(); err != nil && result == nil {
		result = err
	}
	return result
}
