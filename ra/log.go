package ra

import (
	"github.com/oliver-tuschhoff/go-svn/svn"
)

type LogOptions struct {
	Paths                []string
	Start                svn.Revnum
	End                  svn.Revnum
	Limit                int
	DiscoverChangedPaths bool
	StrictNodeHistory    bool
	IncludeMerged        bool
	RevProps             []string
}
