#!/bin/sh
set -eu

. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/common.sh"

SVNADMIN=$(require_tool svnadmin GOSVN_SVNADMIN)
WORK_ROOT=${1:?usage: generate-fsfs.sh WORK_ROOT}
DUMP_FILE="$WORK_ROOT/repos/basic.dump"
ARCHIVE_DIR="$WORK_ROOT/fsfs"
BUILD_DIR="$WORK_ROOT/build-fsfs"
mkdir -p "$ARCHIVE_DIR" "$BUILD_DIR"

if [ ! -f "$DUMP_FILE" ]; then
	echo "missing dump fixture: $DUMP_FILE" >&2
	exit 1
fi

create_fixture() {
	format=$1
	compatible=$2
	repo="$BUILD_DIR/format$format"
	rm -rf "$repo"
	"$SVNADMIN" create --fs-type fsfs --compatible-version "$compatible" "$repo"
	actual=$(sed -n '1p' "$repo/db/format" 2>/dev/null || printf '1')
	if [ "$actual" != "$format" ]; then
		echo "Subversion $($SVNADMIN --version --quiet) created FSFS format $actual for compatibility $compatible, expected $format" >&2
		exit 1
	fi
	if [ "$format" -ge 3 ]; then
		chmod u+w "$repo/db/format"
		awk 'NR == 2 && $1 == "layout" { print "layout sharded 2"; next } { print }' "$repo/db/format" >"$repo/db/format.new"
		mv "$repo/db/format.new" "$repo/db/format"
		chmod 0444 "$repo/db/format"
	fi
	"$SVNADMIN" load -q --ignore-uuid "$repo" <"$DUMP_FILE"
	repository_uuid="10000000-0000-0000-0000-00000000000$format"
	"$SVNADMIN" setuuid "$repo" "$repository_uuid"
	if [ "$format" -ge 7 ]; then
		chmod u+w "$repo/db/uuid"
		printf '%s\n%s\n' "$repository_uuid" "20000000-0000-0000-0000-00000000000$format" >"$repo/db/uuid"
		chmod 0444 "$repo/db/uuid"
	fi
	if [ "$format" -ge 4 ]; then
		"$SVNADMIN" pack -q "$repo"
	fi
	"$SVNADMIN" verify -q "$repo"
	(
		cd "$PROJECT_ROOT"
		go run ./scripts/fixtures/tar-repo -source "$repo" -output "$ARCHIVE_DIR/format$format.tar.gz"
	)
}

create_fixture 1 1.1
create_fixture 2 1.4
create_fixture 3 1.5
create_fixture 4 1.6
create_fixture 6 1.8
create_fixture 7 1.9
create_fixture 8 1.10

cat >"$ARCHIVE_DIR/format5.unreleased" <<'EOF'
FSFS format 5 was understood only by Subversion 1.7 development builds and was
never released. No valid format-5 repository is generated. See
subversion/libsvn_fs_fs/structure, section "Filesystem formats".
EOF

(
	cd "$ARCHIVE_DIR"
	for file in format*.tar.gz format5.unreleased; do
		shasum -a 256 "$file"
	done
) >"$ARCHIVE_DIR/SHA256SUMS"
