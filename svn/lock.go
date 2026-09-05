package svn

import "time"

type Lock struct {
	Path           string
	Token          string
	Owner          string
	Comment        string
	IsDAVComment   bool
	CreationDate   time.Time
	ExpirationDate time.Time
}
