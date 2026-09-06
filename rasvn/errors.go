package rasvn

import "github.com/otuschhoff/go-svn/svn"

var errCodeMalformed = svn.NewError(svn.ErrRASvnMalformedData, "malformed network data")
