package fsfs

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/hashfile"
)

func (filesystem *FS) GetLock(ctx context.Context, nodePath string) (*svn.Lock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonical, err := cleanPath(nodePath)
	if err != nil {
		return nil, err
	}
	digest := md5.Sum([]byte(canonical))
	name := hex.EncodeToString(digest[:])
	lock, err := readLockFile(filepath.Join(filesystem.path, "db", "locks", name[:3], name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if lock != nil && lock.Path != canonical {
		return nil, fmt.Errorf("%w: lock path digest mismatch", svn.ErrFSCorrupt)
	}
	if lock != nil && !lock.ExpirationDate.IsZero() && time.Now().After(lock.ExpirationDate) {
		return nil, nil
	}
	return lock, nil
}

func (filesystem *FS) GetLocks(ctx context.Context, nodePath string, depth svn.Depth) (map[string]*svn.Lock, error) {
	canonical, err := cleanPath(nodePath)
	if err != nil {
		return nil, err
	}
	result := make(map[string]*svn.Lock)
	root := filepath.Join(filesystem.path, "db", "locks")
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || len(entry.Name()) != md5.Size*2 {
			return nil
		}
		lock, err := readLockFile(name)
		if err != nil {
			return err
		}
		if lock != nil && (lock.ExpirationDate.IsZero() || time.Now().Before(lock.ExpirationDate)) && lockWithinDepth(canonical, lock.Path, depth) {
			result[lock.Path] = lock
		}
		return nil
	})
	if os.IsNotExist(err) {
		return result, nil
	}
	return result, err
}

func readLockFile(name string) (*svn.Lock, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	values, err := hashfile.Read(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: parse lock file: %v", svn.ErrFSCorrupt, err)
	}
	if len(values["path"]) == 0 {
		return nil, nil
	}
	davComment := string(values["is_dav_comment"])
	if davComment != "0" && davComment != "1" {
		return nil, fmt.Errorf("%w: invalid is_dav_comment in lock file", svn.ErrFSCorrupt)
	}
	lock := &svn.Lock{
		Path:         string(values["path"]),
		Token:        string(values["token"]),
		Owner:        string(values["owner"]),
		Comment:      string(values["comment"]),
		IsDAVComment: davComment == "1",
	}
	if lock.Token == "" || lock.Owner == "" || len(values["is_dav_comment"]) == 0 || len(values["creation_date"]) == 0 {
		return nil, fmt.Errorf("%w: incomplete lock for %s", svn.ErrFSCorrupt, lock.Path)
	}
	lock.CreationDate, err = svn.ParseDate(string(values["creation_date"]))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid lock creation date", svn.ErrFSCorrupt)
	}
	if len(values["expiration_date"]) != 0 {
		lock.ExpirationDate, err = svn.ParseDate(string(values["expiration_date"]))
		if err != nil {
			return nil, fmt.Errorf("%w: invalid lock expiration date", svn.ErrFSCorrupt)
		}
	}
	return lock, nil
}

func lockWithinDepth(parent, child string, depth svn.Depth) bool {
	if child == parent {
		return true
	}
	relative := strings.TrimPrefix(child, strings.TrimSuffix(parent, "/")+"/")
	if relative == child || relative == "" {
		return false
	}
	switch depth {
	case svn.DepthInfinity, svn.DepthUnknown:
		return true
	case svn.DepthFiles, svn.DepthImmediates:
		return !strings.Contains(relative, "/")
	default:
		return false
	}
}
