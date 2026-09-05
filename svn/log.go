package svn

import "time"

type LogChangeAction byte

const (
	LogAdded    LogChangeAction = 'A'
	LogDeleted  LogChangeAction = 'D'
	LogModified LogChangeAction = 'M'
	LogReplaced LogChangeAction = 'R'
)

type ChangedPath struct {
	Path          string
	Action        LogChangeAction
	CopyfromPath  string
	CopyfromRev   Revnum
	NodeKind      NodeKind
	TextModified  Tristate
	PropsModified Tristate
}

type LogEntry struct {
	Revision         Revnum
	Author           string
	Date             time.Time
	Message          string
	ChangedPaths     []ChangedPath
	RevProps         Props
	HasChildren      bool
	SubtractiveMerge bool
}
