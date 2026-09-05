package svn

import "time"

type Dirent struct {
	Path       string
	Kind       NodeKind
	Size       int64
	HasProps   bool
	CreatedRev Revnum
	Time       time.Time
	LastAuthor string
}

type DirentFields uint32

const (
	DirentKind DirentFields = 1 << iota
	DirentSize
	DirentHasProps
	DirentCreatedRev
	DirentTime
	DirentLastAuthor
	DirentAll = DirentKind | DirentSize | DirentHasProps | DirentCreatedRev | DirentTime | DirentLastAuthor
)
