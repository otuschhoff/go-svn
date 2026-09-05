# Test fixtures

All generated fixtures are deterministic and are replaced atomically by
`make fixtures`. Run `make fixtures-check` to regenerate into a temporary
directory, compare every byte with the committed copy, and enforce the 5 MiB
payload limit.

Determinism comes from fixed repository and instance UUIDs, fixed revision
dates and authors (revision 0 correctly has no `svn:author`), fixed file
contents, canonical transcript ports and client identity, and the Go archive
writer's lexical ordering plus normalized ownership, permissions and times.

Required tools are `svn`, `svnadmin`, `svnserve`, `svnmucc`, `svnlook`,
`shasum`, and Go. Set `GOSVN_SVN`, `GOSVN_SVNADMIN`, or
`GOSVN_SVNSERVE` to override Subversion executable discovery. Set
`GOSVN_FIXTURE_OUTPUT` to generate into a different testdata directory.
Generation is a POSIX-shell workflow and uses `ln` and `awk`; on Windows, run
it under WSL. Windows CI consumes the committed
fixtures and does not regenerate them.

```sh
make fixtures        # replace committed outputs
make fixtures-check  # regenerate elsewhere and compare without modifying them
```

## Repository dumps

`scripts/fixtures/generate-dumps.sh` creates format-v3 dump streams with fixed
UUIDs, authors, dates, log messages, and file contents:

| Fixture | Coverage |
|---------|----------|
| `repos/basic.dump` | project layout, text edits, EOL and keyword properties, executable files, deletion |
| `repos/copies.dump` | directory copies and file moves |
| `repos/props.dump` | custom, EOL, ignore, and global-ignore properties plus deletion |
| `repos/binary.dump` | binary and empty files, binary modification, MIME type |
| `repos/mergeinfo.dump` | branch copy and explicit mergeinfo |
| `repos/locks.dump` | a needs-lock file; locks themselves are created at test time because dump streams do not contain locks |
| `repos/unicode.dump` | UTF-8, spaces, `#`, and `%` in paths |
| `repos/symlinks.dump` | `svn:special` symlink and replacement by a regular file |
| `repos/externals.dump` | repository-relative, operative-revision, and parent-relative external definitions |

The large 10,000-file benchmark repository is intentionally generated on
demand in a later phase and is not committed.

## FSFS repositories

`scripts/fixtures/generate-fsfs.sh` loads `basic.dump` into FSFS formats 1, 2,
3, 4, 6, 7, and 8, verifies each repository, packs formats that support
packing, normalizes timestamps, and stores it as `fsfs/formatN.tar.gz`.
`fsfs/SHA256SUMS` records the generated payloads.

FSFS format 5 was understood only by Subversion 1.7 development builds and was
never released. `fsfs/format5.unreleased` records that deliberate gap. The
pipeline never fabricates a format-5 repository or relabels a different format.

## Protocol transcripts

`scripts/fixtures/generate-transcripts.sh` records a real `svn info` ra_svn
exchange through the Go TCP recorder in `scripts/fixtures/rasvn-record`. The
transcript stores complete client-to-server and server-to-client byte streams
as base64, without credentials. The recorder replaces the ephemeral port and
platform-specific `SVN/<version> (<platform>)` client identity with
same-purpose deterministic values while preserving valid ra_svn framing.

`transcripts/dav/options-request.xml` is the deterministic HTTP discovery seed.
Live DAV responses require a configured `mod_dav_svn` and will be added by the
Phase 6 recorder. No credential, authorization header, private key, or session
cookie may be committed in a transcript.
