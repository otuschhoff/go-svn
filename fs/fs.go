package fs

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/svn"
)

type Opener func(context.Context, string) (FS, error)

var (
	openerMu sync.RWMutex
	openers  []Opener
)

func RegisterOpener(opener Opener) {
	if opener == nil {
		panic("fs: nil opener")
	}
	openerMu.Lock()
	openers = append(openers, opener)
	openerMu.Unlock()
}

func Open(ctx context.Context, path string) (FS, error) {
	openerMu.RLock()
	registered := append([]Opener(nil), openers...)
	openerMu.RUnlock()
	if len(registered) == 0 {
		return nil, fmt.Errorf("%w: no filesystem backend registered", svn.ErrFSUnsupportedFormat)
	}
	var firstErr error
	for _, opener := range registered {
		filesystem, err := opener(ctx, path)
		if err == nil {
			return filesystem, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return nil, firstErr
}

type ChangeKind string

const (
	ChangeAdd     ChangeKind = "add"
	ChangeDelete  ChangeKind = "delete"
	ChangeModify  ChangeKind = "modify"
	ChangeReplace ChangeKind = "replace"
	ChangeReset   ChangeKind = "reset"
)

type PathChange struct {
	Path          string
	Kind          ChangeKind
	NodeKind      svn.NodeKind
	TextModified  bool
	PropsModified bool
	CopyFromPath  string
	CopyFromRev   svn.Revnum
}

type DirEntry struct {
	Name string
	Kind svn.NodeKind
}

type HistoryEntry struct {
	Path     string
	Revision svn.Revnum
}

type FS interface {
	Path() string
	UUID(context.Context) (string, error)
	YoungestRevision(context.Context) (svn.Revnum, error)
	RevisionProps(context.Context, svn.Revnum) (svn.Props, error)
	RevisionRoot(context.Context, svn.Revnum) (Root, error)
	GetLock(context.Context, string) (*svn.Lock, error)
	GetLocks(context.Context, string, svn.Depth) (map[string]*svn.Lock, error)
}

type Root interface {
	Revision() svn.Revnum
	CheckPath(context.Context, string) (svn.NodeKind, error)
	NodeProps(context.Context, string) (svn.Props, error)
	DirEntries(context.Context, string) ([]DirEntry, error)
	FileContents(context.Context, string, io.Writer) error
	FileLength(context.Context, string) (int64, error)
	FileChecksum(context.Context, string, svn.ChecksumKind) (*svn.Checksum, error)
	GetFileDelta(context.Context, Root, string, string) (delta.WindowReader, error)
	PathsChanged(context.Context) (map[string]PathChange, error)
	NodeID(context.Context, string) (string, error)
	NodeCreatedRevision(context.Context, string) (svn.Revnum, error)
	NodeCreatedPath(context.Context, string) (string, error)
	NodeOrigin(context.Context, string) (svn.Revnum, error)
	ClosestCopy(context.Context, string) (string, svn.Revnum, error)
	NodeHistory(context.Context, string, bool) ([]HistoryEntry, error)
	Mergeinfo(context.Context, string, bool) (map[string]mergeinfo.Mergeinfo, error)
}
