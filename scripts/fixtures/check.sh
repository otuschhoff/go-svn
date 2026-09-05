#!/bin/sh
set -eu

. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/common.sh"

work_root=$(mktemp -d "${TMPDIR:-/tmp}/go-svn-fixtures-check.XXXXXX")
trap 'rm -rf "$work_root"' EXIT HUP INT TERM

GOSVN_FIXTURE_OUTPUT="$work_root/testdata" "$SCRIPT_DIR/generate.sh"

for directory in repos fsfs transcripts; do
	if [ ! -d "$OUTPUT_ROOT/$directory" ]; then
		echo "committed fixture directory is missing: $OUTPUT_ROOT/$directory" >&2
		exit 1
	fi
	diff -ru "$OUTPUT_ROOT/$directory" "$work_root/testdata/$directory"
done

size_kib=$(du -sk "$OUTPUT_ROOT/repos" "$OUTPUT_ROOT/fsfs" "$OUTPUT_ROOT/transcripts" | awk '{ total += $1 } END { print total }')
if [ "$size_kib" -gt 5120 ]; then
	echo "fixture payload is ${size_kib} KiB; limit is 5120 KiB" >&2
	exit 1
fi

printf 'fixtures are reproducible and use %s KiB\n' "$size_kib"
