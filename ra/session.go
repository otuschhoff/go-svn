package ra

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/otuschhoff/go-svn/auth"
	"github.com/otuschhoff/go-svn/config"
	"github.com/otuschhoff/go-svn/delta"
	"github.com/otuschhoff/go-svn/mergeinfo"
	"github.com/otuschhoff/go-svn/svn"
	"github.com/otuschhoff/go-svn/svn/notify"
)

type Session interface {
	URL() string
	Reparent(context.Context, string) error
	RepositoryRoot(context.Context) (string, error)
	UUID(context.Context) (string, error)
	HasCapability(context.Context, Capability) (bool, error)

	LatestRevision(context.Context) (svn.Revnum, error)
	DatedRevision(context.Context, time.Time) (svn.Revnum, error)
	RevProps(context.Context, svn.Revnum) (svn.Props, error)
	RevProp(context.Context, svn.Revnum, string) ([]byte, bool, error)
	ChangeRevProp(context.Context, svn.Revnum, string, []byte, []byte, bool) error

	CheckPath(context.Context, string, svn.Revnum) (svn.NodeKind, error)
	Stat(context.Context, string, svn.Revnum) (*svn.Dirent, error)
	GetFile(context.Context, string, svn.Revnum, io.Writer, bool) (svn.Revnum, svn.Props, error)
	GetDir(context.Context, string, svn.Revnum, svn.DirentFields) ([]svn.Dirent, svn.Revnum, svn.Props, error)
	List(context.Context, string, svn.Revnum, []string, svn.Depth, svn.DirentFields, func(string, *svn.Dirent) error) error
	Log(context.Context, LogOptions, func(*svn.LogEntry) error) error
	GetLocations(context.Context, string, svn.Revnum, []svn.Revnum) (map[svn.Revnum]string, error)
	GetLocationSegments(context.Context, string, svn.Revnum, svn.Revnum, svn.Revnum, func(LocationSegment) error) error
	GetFileRevs(context.Context, string, svn.Revnum, svn.Revnum, bool, FileRevHandler) error
	GetMergeinfo(context.Context, []string, svn.Revnum, mergeinfo.Inheritance, bool) (map[string]mergeinfo.Mergeinfo, error)
	GetInheritedProps(context.Context, string, svn.Revnum) ([]InheritedProps, error)
	GetDeletedRev(context.Context, string, svn.Revnum, svn.Revnum) (svn.Revnum, error)

	DoUpdate(context.Context, svn.Revnum, string, svn.Depth, bool, bool, delta.Editor) (Reporter, error)
	DoSwitch(context.Context, svn.Revnum, string, svn.Depth, string, bool, bool, delta.Editor) (Reporter, error)
	DoStatus(context.Context, string, svn.Revnum, svn.Depth, delta.Editor) (Reporter, error)
	DoDiff(context.Context, svn.Revnum, string, svn.Depth, bool, bool, string, delta.Editor) (Reporter, error)

	GetCommitEditor(context.Context, svn.Props, map[string]string, bool, func(*CommitInfo) error) (delta.Editor, error)
	Lock(context.Context, map[string]svn.Revnum, string, bool, LockCallback) error
	Unlock(context.Context, map[string]string, bool, LockCallback) error
	GetLock(context.Context, string) (*svn.Lock, error)
	GetLocks(context.Context, string, svn.Depth) (map[string]*svn.Lock, error)
	Replay(context.Context, svn.Revnum, svn.Revnum, bool, delta.Editor) error
	ReplayRange(context.Context, svn.Revnum, svn.Revnum, svn.Revnum, bool, func(svn.Revnum, svn.Props) (delta.Editor, error), func(svn.Revnum, svn.Props, delta.Editor) error) error
	Close() error
}

type Reporter interface {
	SetPath(context.Context, string, svn.Revnum, svn.Depth, bool, string) error
	LinkPath(context.Context, string, string, svn.Revnum, svn.Depth, bool, string) error
	DeletePath(context.Context, string) error
	FinishReport(context.Context) error
	AbortReport(context.Context) error
}

type LocationSegment struct {
	Path       string
	RangeStart svn.Revnum
	RangeEnd   svn.Revnum
}

type FileRevision struct {
	Path      string
	Revision  svn.Revnum
	RevProps  svn.Props
	PropDiffs svn.Props
	Merged    bool
	Delta     delta.WindowReader
}

type FileRevHandler func(context.Context, FileRevision) error

type InheritedProps struct {
	Path  string
	Props svn.Props
}

type CommitInfo struct {
	Revision        svn.Revnum
	Date            time.Time
	Author          string
	PostCommitError string
	ReposRoot       string
}

type LockCallback func(path string, lock *svn.Lock, err error) error

type TunnelFunc func(context.Context, string, string, int, string) (io.ReadWriteCloser, error)

type Callbacks struct {
	Auth     auth.Baton
	Config   *config.Config
	Progress func(sent, total int64)
	Notify   notify.Func
	Tunnel   TunnelFunc
	HTTP     *http.Client
	Logger   *slog.Logger
}
