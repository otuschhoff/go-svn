package fsfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	fsapi "github.com/otuschhoff/go-svn/fs"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/hashfile"
)

type Transaction struct {
	filesystem *FS
	name       string
	base       svn.Revnum
	mu         sync.Mutex
	root       *TxnRoot
	changes    map[string]fsapi.PathChange
	nextNode   int64
}

func (filesystem *FS) BeginTxn(ctx context.Context, base svn.Revnum) (fsapi.Txn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	youngest, err := filesystem.YoungestRevision(ctx)
	if err != nil {
		return nil, err
	}
	if base < 0 || base > youngest {
		return nil, fmt.Errorf("%w: revision %d", svn.ErrFSNoSuchRevision, base)
	}
	var transaction *Transaction
	err = withWriteLock(ctx, filepath.Join(filesystem.path, "db", "txn-current-lock"), func() error {
		currentPath := filepath.Join(filesystem.path, "db", "txn-current")
		data, readErr := os.ReadFile(currentPath)
		if readErr != nil {
			return readErr
		}
		sequence, parseErr := strconv.ParseInt(string(trimSpace(data)), 36, 64)
		if parseErr != nil || sequence < 0 {
			return fmt.Errorf("%w: invalid transaction sequence", svn.ErrFSCorrupt)
		}
		name := strconv.FormatInt(int64(base), 10) + "-" + strconv.FormatInt(sequence, 36)
		transaction = &Transaction{filesystem: filesystem, name: name, base: base}
		if createErr := transaction.create(ctx); createErr != nil {
			return createErr
		}
		if writeErr := writeFileAtomic(currentPath, []byte(strconv.FormatInt(sequence+1, 36)+"\n"), 0o666); writeErr != nil {
			_ = transaction.remove()
			return writeErr
		}
		return nil
	})
	return transaction, err
}

func (transaction *Transaction) Name() string             { return transaction.name }
func (transaction *Transaction) BaseRevision() svn.Revnum { return transaction.base }

func (transaction *Transaction) Root(ctx context.Context) (fsapi.TxnRoot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.root != nil {
		return transaction.root, nil
	}
	rootValue, err := transaction.filesystem.RevisionRoot(ctx, transaction.base)
	if err != nil {
		return nil, err
	}
	revisionRoot := rootValue.(*Root)
	transaction.root = &TxnRoot{transaction: transaction, node: newTxnNode(revisionRoot, "/", revisionRoot.root)}
	transaction.changes = make(map[string]fsapi.PathChange)
	return transaction.root, nil
}

func (transaction *Transaction) Properties(ctx context.Context) (svn.Props, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(transaction.path("props"))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return hashfile.Read(file)
}

func (transaction *Transaction) ChangeProperty(ctx context.Context, name string, value []byte) error {
	if name == "" {
		return fmt.Errorf("%w: empty transaction property name", svn.ErrIncorrectParams)
	}
	return transaction.withLock(ctx, func() error {
		properties, err := transaction.Properties(ctx)
		if err != nil {
			return err
		}
		if value == nil {
			delete(properties, name)
		} else {
			properties[name] = append([]byte(nil), value...)
		}
		return writeHashAtomic(transaction.path("props"), properties)
	})
}

func (transaction *Transaction) Abort(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := transaction.withLock(ctx, transaction.removeData); err != nil {
		return err
	}
	return removeIfExists(transaction.protorevPath() + "-lock")
}

func (transaction *Transaction) create(ctx context.Context) error {
	directory := transaction.directory()
	if err := os.Mkdir(directory, 0o777); err != nil {
		return err
	}
	properties := svn.Props{"svn:date": []byte(svn.FormatDate(time.Now().UTC()))}
	if err := writeHashAtomic(transaction.path("props"), properties); err != nil {
		_ = transaction.remove()
		return err
	}
	for name, data := range map[string][]byte{
		"changes":  nil,
		"next-ids": []byte("0 0\n\x00"),
	} {
		if err := os.WriteFile(transaction.path(name), data, 0o666); err != nil {
			_ = transaction.remove()
			return err
		}
	}
	protorev := transaction.protorevPath()
	if err := os.WriteFile(protorev, nil, 0o666); err != nil {
		_ = transaction.remove()
		return err
	}
	if err := os.WriteFile(protorev+"-lock", nil, 0o666); err != nil {
		_ = transaction.remove()
		return err
	}
	return ctx.Err()
}

func (transaction *Transaction) withLock(ctx context.Context, action func() error) error {
	return withWriteLock(ctx, transaction.protorevPath()+"-lock", action)
}

func (transaction *Transaction) remove() error {
	if err := transaction.removeData(); err != nil {
		return err
	}
	return removeIfExists(transaction.protorevPath() + "-lock")
}

func (transaction *Transaction) removeData() error {
	var result error
	for _, name := range []string{transaction.directory(), transaction.protorevPath()} {
		if err := os.RemoveAll(name); err != nil && !errors.Is(err, os.ErrNotExist) && result == nil {
			result = err
		}
	}
	return result
}

func removeIfExists(name string) error {
	err := os.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (transaction *Transaction) directory() string {
	return filepath.Join(transaction.filesystem.path, "db", "transactions", transaction.name+".txn")
}

func (transaction *Transaction) path(name string) string {
	return filepath.Join(transaction.directory(), name)
}

func (transaction *Transaction) protorevPath() string {
	return filepath.Join(transaction.filesystem.path, "db", "txn-protorevs", transaction.name+".rev")
}

func writeHashAtomic(name string, properties svn.Props) error {
	temporary, err := os.CreateTemp(filepath.Dir(name), ".tmp-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := hashfile.Write(temporary, properties); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, name)
}

func writeFileAtomic(name string, data []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(name), ".tmp-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, name)
}

func trimSpace(data []byte) []byte {
	start, end := 0, len(data)
	for start < end && (data[start] == ' ' || data[start] == '\n' || data[start] == '\r' || data[start] == '\t') {
		start++
	}
	for end > start && (data[end-1] == ' ' || data[end-1] == '\n' || data[end-1] == '\r' || data[end-1] == '\t') {
		end--
	}
	return data[start:end]
}
