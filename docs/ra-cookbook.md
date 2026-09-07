# Repository Access Cookbook

The `ra` package exposes one interface across local FSFS, svnserve, and
DAV-backed repositories. Import the protocols your binary needs so their
factories register during initialization.

```go
import (
    "github.com/otuschhoff/go-svn/ra"
    _ "github.com/otuschhoff/go-svn/ra/ralocal"
    _ "github.com/otuschhoff/go-svn/radav"
    _ "github.com/otuschhoff/go-svn/rasvn"
)
```

## Open and inspect a repository

```go
session, correctedURL, err := ra.Open(ctx, repositoryURL, callbacks)
if err != nil {
    return err
}
defer session.Close()

revision, err := session.LatestRevision(ctx)
```

`correctedURL` is non-empty when the server redirects or canonicalizes the
requested URL. Operations accept a `context.Context`; cancel it to interrupt
network reads and writes.

## Authentication and TLS

Supply an `ra.Callbacks` value with an `auth.Baton`. Providers are tried in
order, so applications can layer process credentials, the on-disk Subversion
auth cache, and an interactive prompt. TLS certificate acceptance must be an
explicit application decision. In unattended programs, return an error for
unknown certificate failures rather than accepting all failures.

## Read a file

```go
var contents bytes.Buffer
revision, properties, err := session.GetFile(ctx, "README.md", svn.InvalidRevnum,
    &contents, true)
```

Paths passed to a session are repository-relative. Use `svn.InvalidRevnum` when
the youngest revision is intended, and preserve returned revision numbers when
a consistent multi-operation view matters.

## List a tree

```go
err := session.List(ctx, "", svn.InvalidRevnum, nil, svn.DepthInfinity,
    svn.DirentKind|svn.DirentSize, func(path string, entry *svn.Dirent) error {
        // Consume each entry without retaining the complete tree.
        return nil
    })
```

Callbacks execute synchronously. Keep them short or hand work to a bounded
consumer. Always close sessions; a session owns protocol connections and other
backend resources.