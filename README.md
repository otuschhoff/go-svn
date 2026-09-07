# go-svn

`go-svn` is a pure-Go Subversion implementation for embedding repository access,
working-copy operations, and FSFS repository services in Go programs. It also
ships `gosvn`, a command-line client with familiar `svn` commands and flags.

The project is pre-1.0. Test compatibility against your repositories and
workflows before replacing the native Subversion client in production.

## Install

Go 1.26 or newer is required.

```sh
go install github.com/otuschhoff/go-svn/cmd/gosvn@latest
```

The binary and libraries do not require cgo:

```sh
CGO_ENABLED=0 go build ./cmd/gosvn
```

## Command line

```sh
gosvn checkout https://svn.example.com/repos/project/trunk project
gosvn status project
gosvn update project
gosvn commit -m "Describe the change" project
gosvn log --xml https://svn.example.com/repos/project/trunk
```

Run `gosvn help` or `gosvn help COMMAND` for the supported command surface.
Authentication can come from `--username` and `--password`, the Subversion auth
cache, or a terminal prompt. Use `--non-interactive` in automation and pass
`--no-auth-cache` when credentials must not be persisted.

## Libraries

The `client` package provides high-level checkout, update, commit, merge, and
repository operations. The `ra` package exposes protocol-neutral repository
sessions. The `fs` package opens repository storage directly, and `wc` operates
on WC-NG working copies.

See [docs/embedding.md](docs/embedding.md) for lifecycle and error-handling
guidance and [docs/ra-cookbook.md](docs/ra-cookbook.md) for lower-level RA
examples.

## Compatibility

| Surface | Support |
| --- | --- |
| Build | Pure Go with `CGO_ENABLED=0`; Linux, macOS, and Windows |
| Repository access | `file://`, `svn://`, `svn+ssh://`, `http://`, `https://` |
| Repository storage | FSFS formats used by supported Subversion 1.x repositories |
| Working copies | WC-NG format 31 read/write; format 32 read-only when enabled |
| CLI | Core read, working-copy, repository mutation, merge, lock, and property commands |
| Output | Native-style text; XML for info, log, list, status, blame, diff summary, and proplist |

Subversion has a broad compatibility surface. Unsupported server capabilities,
repository formats, or command flags fail explicitly rather than silently
changing semantics.

## Dependency policy

Runtime dependencies are kept small and pure Go. `modernc.org/sqlite` provides
WC-NG metadata access without cgo; `golang.org/x/term` is used for no-echo
credential prompts. Protocol, delta, repository, and working-copy behavior is
implemented in this module. Dependencies are pinned by `go.mod` and reviewed as
part of release validation.

## Development

```sh
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=1 go test -race ./...
FUZZTIME=10s make fuzz
make benchmark
```

Nightly CI runs each decoder fuzz target for at least ten minutes. Performance
methodology and current baselines are in [docs/perf.md](docs/perf.md). Common
setup and interoperability failures are covered by
[docs/troubleshooting.md](docs/troubleshooting.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).