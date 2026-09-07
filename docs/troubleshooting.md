# Troubleshooting

## No repository access backend is registered

Low-level users of `ra.Open` must blank-import the desired backend:

```go
_ "github.com/otuschhoff/go-svn/ra/ralocal" // file://
_ "github.com/otuschhoff/go-svn/radav"      // http:// and https://
_ "github.com/otuschhoff/go-svn/rasvn"      // svn:// and svn+*
```

The high-level `client` package registers all three automatically.

## No filesystem backend is registered

Import `_ "github.com/otuschhoff/go-svn/fs/fsfs"` before calling `fs.Open`.
The path is the repository root containing `format` and `db`, not a working copy
or repository URL.

## Authentication fails without prompting

Check `--non-interactive`, provider order, and whether the requested credential
kind is supported by each provider. For CLI diagnostics, retry with an explicit
`--username` and use `--no-auth-cache` to isolate stale cached credentials.
Never pass a password on a shared process command line; allow the terminal
prompt or provide credentials through an embedding application's callback.

## TLS certificate verification fails

Install the issuing CA where possible. `--trust-server-cert-failures` is for
explicitly selected failures and should not be a permanent substitute for
valid server identity. Non-interactive clients must specify their trust policy.

## A working copy is locked

Ensure no other Subversion process is active, then run:

```sh
gosvn cleanup PATH
```

Do not edit `.svn/wc.db` manually. If cleanup reports an unsupported working-copy
format, use a compatible native Subversion client or create a fresh checkout.

## A context cancellation leaves an operation incomplete

Repository operations are atomic at their backend boundary and update editors
roll back WC metadata. Files already installed before cancellation may still be
present; run `gosvn cleanup` followed by `gosvn update` to reconcile the working
copy.

## Pure-Go build unexpectedly enables cgo

Set `CGO_ENABLED=0` at build time and inspect the selected environment with
`go env CGO_ENABLED GOOS GOARCH`. The module itself does not require cgo.