#!/bin/sh
set -eu

. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/common.sh"

work_root=$(mktemp -d "${TMPDIR:-/tmp}/go-svn-fixtures.XXXXXX")
trap 'rm -rf "$work_root"' EXIT HUP INT TERM

"$SCRIPT_DIR/generate-dumps.sh" "$work_root"
"$SCRIPT_DIR/generate-fsfs.sh" "$work_root"
"$SCRIPT_DIR/generate-transcripts.sh" "$work_root"

replace_dir "$work_root/repos" "$OUTPUT_ROOT/repos"
replace_dir "$work_root/fsfs" "$OUTPUT_ROOT/fsfs"
replace_dir "$work_root/transcripts" "$OUTPUT_ROOT/transcripts"

printf 'fixtures generated in %s\n' "$OUTPUT_ROOT"
