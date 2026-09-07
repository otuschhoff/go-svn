# Embedding go-svn

Use `client.New` for user-facing operations and `ra.Open` when an application
needs protocol-level control. Both APIs take contexts and return errors wrapping
sentinels from the `svn` package, so callers can use `errors.Is`.

## High-level client

```go
callbacks := &ra.Callbacks{
    Notify: func(event notify.Notify) {
        // Translate semantic events into logs or UI updates.
    },
}
svnClient := client.New(callbacks)
revision, err := svnClient.Checkout(ctx, repositoryURL, destination, svn.InvalidRevnum,
    wc.UpdateOptions{Depth: svn.DepthInfinity})
```

The high-level client registers local, svnserve, and DAV backends. Working-copy
methods open and close WC-NG databases internally. If an application opens a
`wc.Database` or `ra.Session` itself, it must close it.

## Credentials

Construct an `auth.Baton` from providers appropriate to the application. Avoid
embedding passwords in repository URLs. Interactive applications can provide
prompt callbacks; services should use bounded, non-interactive providers and
fail when credentials are unavailable.

## Concurrency and cancellation

Treat a repository session and writable working-copy database as operation
scoped unless a backend documents stronger concurrency guarantees. Pass one
context through the complete operation. Cancellation interrupts protocol I/O
and causes an in-progress working-copy editor to roll back its metadata
transaction.

## Notifications

Notifications are semantic rather than preformatted text. They are suitable for
progress displays, structured logs, and tests. A callback should not call back
into the same active operation, and it should return quickly.

## Pure-Go deployment

The SQLite implementation used for WC-NG is pure Go. Validate deployment with:

```sh
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go build ./...
```

No Subversion shared library or command-line executable is required at runtime.