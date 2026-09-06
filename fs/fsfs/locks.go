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
	"sort"
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

func (filesystem *FS) Lock(ctx context.Context, nodePath, token, owner, comment string, expiration time.Time, steal bool) (*svn.Lock, error) {
	canonical, err := cleanPath(nodePath)
	if err != nil {
		return nil, err
	}
	if canonical == "/" || token == "" || owner == "" {
		return nil, fmt.Errorf("%w: invalid lock parameters", svn.ErrIncorrectParams)
	}
	var result *svn.Lock
	err = withWriteLock(ctx, filepath.Join(filesystem.path, "db", "write-lock"), func() error {
		existing, err := filesystem.GetLock(ctx, canonical)
		if err != nil {
			return err
		}
		if existing != nil && !steal {
			return fmt.Errorf("%w: %s", svn.ErrFSPathAlreadyLocked, canonical)
		}
		result = &svn.Lock{Path: canonical, Token: token, Owner: owner, Comment: comment, CreationDate: time.Now().UTC(), ExpirationDate: expiration}
		values := svn.Props{
			"path":           []byte(result.Path),
			"token":          []byte(result.Token),
			"owner":          []byte(result.Owner),
			"comment":        []byte(result.Comment),
			"is_dav_comment": []byte("0"),
			"creation_date":  []byte(svn.FormatDate(result.CreationDate)),
		}
		if !expiration.IsZero() {
			values["expiration_date"] = []byte(svn.FormatDate(expiration.UTC()))
		}
		digest := lockDigest(canonical)
		if err := os.MkdirAll(filepath.Dir(filesystem.lockPath(digest)), 0o755); err != nil {
			return err
		}
		if err := writeHashAtomic(filesystem.lockPath(digest), values); err != nil {
			return err
		}
		return filesystem.updateLockParents(canonical, digest, true)
	})
	return result, err
}

func (filesystem *FS) Unlock(ctx context.Context, nodePath, token string, breakLock bool) error {
	canonical, err := cleanPath(nodePath)
	if err != nil {
		return err
	}
	return withWriteLock(ctx, filepath.Join(filesystem.path, "db", "write-lock"), func() error {
		lock, err := filesystem.GetLock(ctx, canonical)
		if err != nil {
			return err
		}
		if lock == nil {
			return fmt.Errorf("%w: %s", svn.ErrFSNoSuchLock, canonical)
		}
		if !breakLock && token != lock.Token {
			return fmt.Errorf("%w: %s", svn.ErrFSBadLockToken, canonical)
		}
		digest := lockDigest(canonical)
		if err := os.Remove(filesystem.lockPath(digest)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return filesystem.updateLockParents(canonical, digest, false)
	})
}

func (filesystem *FS) updateLockParents(nodePath, childDigest string, add bool) error {
	for parent := filepath.ToSlash(filepath.Dir(nodePath)); ; parent = filepath.ToSlash(filepath.Dir(parent)) {
		if !strings.HasPrefix(parent, "/") {
			parent = "/" + parent
		}
		digest := lockDigest(parent)
		name := filesystem.lockPath(digest)
		values := make(svn.Props)
		if data, err := os.ReadFile(name); err == nil {
			values, err = hashfile.Read(bytes.NewReader(data))
			if err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		children := strings.Fields(string(values["children"]))
		set := make(map[string]bool, len(children)+1)
		for _, child := range children {
			set[child] = true
		}
		if add {
			set[childDigest] = true
		} else {
			delete(set, childDigest)
		}
		children = children[:0]
		for child := range set {
			children = append(children, child)
		}
		sort.Strings(children)
		if len(children) == 0 {
			if err := os.Remove(name); err != nil && !os.IsNotExist(err) {
				return err
			}
		} else {
			if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return err
			}
			values["children"] = []byte(strings.Join(children, "\n") + "\n")
			if err := writeHashAtomic(name, values); err != nil {
				return err
			}
		}
		if parent == "/" {
			break
		}
	}
	return nil
}

func lockDigest(nodePath string) string {
	digest := md5.Sum([]byte(nodePath))
	return hex.EncodeToString(digest[:])
}

func (filesystem *FS) lockPath(digest string) string {
	return filepath.Join(filesystem.path, "db", "locks", digest[:3], digest)
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
