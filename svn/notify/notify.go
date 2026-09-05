package notify

import (
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type Action uint16

const (
	ActionSkip Action = iota
	ActionUpdateDelete
	ActionUpdateAdd
	ActionUpdateUpdate
	ActionUpdateCompleted
	ActionStatusCompleted
	ActionCommitModified
	ActionCommitAdded
	ActionCommitDeleted
	ActionCommitReplaced
	ActionCommitPostfixTxdelta
	ActionBlameRevision
	ActionLocked
	ActionUnlocked
	ActionFailedLock
	ActionFailedUnlock
	ActionChangelistSet
	ActionChangelistClear
	ActionMergeBegin
	ActionForeignMergeBegin
	ActionUpdateReplace
	ActionPropertyAdded
	ActionPropertyModified
	ActionPropertyDeleted
	ActionPropertyDeletedNonexistent
	ActionRevisionPropertySet
	ActionRevisionPropertyDeleted
)

type Notify struct {
	Action   Action
	Path     string
	Kind     svn.NodeKind
	Revision svn.Revnum
	Error    error
	MimeType string
	Lock     *svn.Lock
}

type Func func(Notify)
