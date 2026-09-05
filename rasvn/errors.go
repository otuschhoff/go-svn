package rasvn

import "github.com/oliver-tuschhoff/go-svn/svn"

var errCodeMalformed = svn.NewError(svn.ErrRASvnMalformedData, "malformed network data")
