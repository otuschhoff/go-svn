package wc

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/otuschhoff/go-svn/svn"
)

type PristineInfo struct {
	Checksum svn.Checksum
	MD5      svn.Checksum
	Size     int64
	RefCount int64
}

func (database *Database) Pristine(ctx context.Context, checksum svn.Checksum) (*PristineInfo, error) {
	var serialized, md5 string
	info := &PristineInfo{}
	err := database.sql.QueryRowContext(ctx, `SELECT checksum, md5_checksum, size, refcount FROM PRISTINE WHERE checksum = ?`, checksum.Serialize()).Scan(&serialized, &md5, &info.Size, &info.RefCount)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: pristine %s", svn.ErrWCPathNotFound, checksum.Hex())
	}
	if err != nil {
		return nil, err
	}
	var parseErr error
	info.Checksum, parseErr = svn.ParseChecksum(serialized)
	if parseErr == nil {
		info.MD5, parseErr = svn.ParseChecksum(md5)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("%w: %v", svn.ErrWCCorrupt, parseErr)
	}
	return info, nil
}

func (database *Database) OpenPristine(ctx context.Context, checksum svn.Checksum) (io.ReadCloser, error) {
	if checksum.Kind != svn.ChecksumSHA1 || len(checksum.Digest) == 0 {
		return nil, fmt.Errorf("%w: pristine key must be SHA-1", svn.ErrBadChecksumKind)
	}
	if _, err := database.Pristine(ctx, checksum); err != nil {
		return nil, err
	}
	hexValue := checksum.Hex()
	file, err := os.Open(filepath.Join(database.wcRoot, ".svn", "pristine", hexValue[:2], hexValue+".svn-base"))
	if err != nil {
		return nil, fmt.Errorf("%w: pristine %s: %v", svn.ErrWCCorruptTextBase, hexValue, err)
	}
	return file, nil
}
