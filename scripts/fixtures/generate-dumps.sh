#!/bin/sh
set -eu

. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/common.sh"

SVN=$(require_tool svn GOSVN_SVN)
SVNADMIN=$(require_tool svnadmin GOSVN_SVNADMIN)
SVNLOOK=$(require_tool svnlook GOSVN_SVNLOOK)
WORK_ROOT=${1:?usage: generate-dumps.sh WORK_ROOT}
DUMP_DIR="$WORK_ROOT/repos"
BUILD_DIR="$WORK_ROOT/build-dumps"
mkdir -p "$DUMP_DIR" "$BUILD_DIR"

commit_wc() {
	wc=$1
	message=$2
	"$SVN" commit -q --non-interactive --no-auth-cache --username fixture -m "$message" "$wc"
}

normalize_revprops() {
	repo=$1
	youngest=$($SVNLOOK youngest "$repo")
	rev=0
	while [ "$rev" -le "$youngest" ]; do
		date_file="$BUILD_DIR/rev-date"
		printf '2020-01-%02dT00:00:00.000000Z\n' "$((rev + 1))" >"$date_file"
		"$SVNADMIN" setrevprop "$repo" -r "$rev" svn:date "$date_file"
		if [ "$rev" -gt 0 ]; then
			author_file="$BUILD_DIR/rev-author"
			printf 'fixture\n' >"$author_file"
			"$SVNADMIN" setrevprop "$repo" -r "$rev" svn:author "$author_file"
		fi
		rev=$((rev + 1))
	done
}

new_fixture() {
	name=$1
	uuid_suffix=$2
	repo="$BUILD_DIR/$name.repo"
	wc="$BUILD_DIR/$name.wc"
	rm -rf "$repo" "$wc"
	"$SVNADMIN" create --fs-type fsfs --compatible-version 1.10 "$repo"
	"$SVNADMIN" setuuid "$repo" "00000000-0000-0000-0000-0000000000$uuid_suffix"
	"$SVN" checkout -q "file://$repo" "$wc"
	mkdir -p "$wc/trunk" "$wc/branches" "$wc/tags"
	"$SVN" add -q "$wc/trunk" "$wc/branches" "$wc/tags"
	commit_wc "$wc" "create standard project layout"
	printf '%s\n' "$repo|$wc"
}

finish_fixture() {
	name=$1
	repo=$2
	normalize_revprops "$repo"
	"$SVNADMIN" verify -q "$repo"
	"$SVNADMIN" dump -q "$repo" >"$DUMP_DIR/$name.dump"
}

fixture_basic() {
	pair=$(new_fixture basic 01); repo=${pair%%|*}; wc=${pair#*|}
	printf 'go-svn fixture\n' >"$wc/trunk/README.txt"
	printf '#!/bin/sh\nprintf "revision: $Revision$\\n"\n' >"$wc/trunk/run.sh"
	chmod +x "$wc/trunk/run.sh"
	"$SVN" add -q "$wc/trunk/README.txt" "$wc/trunk/run.sh"
	"$SVN" propset -q svn:eol-style LF "$wc/trunk/README.txt"
	"$SVN" propset -q svn:keywords Revision "$wc/trunk/run.sh"
	"$SVN" propset -q svn:executable '*' "$wc/trunk/run.sh"
	commit_wc "$wc" "add text and executable files"
	printf 'go-svn fixture\nsecond line\n' >"$wc/trunk/README.txt"
	commit_wc "$wc" "modify text"
	"$SVN" delete -q "$wc/trunk/run.sh"
	commit_wc "$wc" "delete executable"
	finish_fixture basic "$repo"
}

fixture_copies() {
	pair=$(new_fixture copies 02); repo=${pair%%|*}; wc=${pair#*|}
	printf 'copy source\n' >"$wc/trunk/source.txt"
	"$SVN" add -q "$wc/trunk/source.txt"
	commit_wc "$wc" "add copy source"
	"$SVN" copy -q "$wc/trunk" "$wc/branches/feature"
	commit_wc "$wc" "copy trunk to branch"
	"$SVN" move -q "$wc/branches/feature/source.txt" "$wc/branches/feature/moved.txt"
	commit_wc "$wc" "move copied file"
	finish_fixture copies "$repo"
}

fixture_props() {
	pair=$(new_fixture props 03); repo=${pair%%|*}; wc=${pair#*|}
	printf 'line one\nline two\n' >"$wc/trunk/props.txt"
	"$SVN" add -q "$wc/trunk/props.txt"
	"$SVN" propset -q custom:text 'custom value' "$wc/trunk/props.txt"
	"$SVN" propset -q svn:eol-style LF "$wc/trunk/props.txt"
	"$SVN" propset -q svn:ignore '*.tmp' "$wc/trunk"
	"$SVN" propset -q svn:global-ignores '*.cache' "$wc/trunk"
	commit_wc "$wc" "add node properties"
	"$SVN" propdel -q custom:text "$wc/trunk/props.txt"
	commit_wc "$wc" "delete custom property"
	finish_fixture props "$repo"
}

fixture_binary() {
	pair=$(new_fixture binary 04); repo=${pair%%|*}; wc=${pair#*|}
	dd if=/dev/zero of="$wc/trunk/data.bin" bs=1024 count=64 2>/dev/null
	printf '\001\002\003SVN\000' | dd of="$wc/trunk/data.bin" conv=notrunc 2>/dev/null
	"$SVN" add -q "$wc/trunk/data.bin"
	"$SVN" propset -q svn:mime-type application/octet-stream "$wc/trunk/data.bin"
	commit_wc "$wc" "add binary data"
	printf 'changed' | dd of="$wc/trunk/data.bin" bs=1 seek=32768 conv=notrunc 2>/dev/null
	commit_wc "$wc" "modify binary data"
	: >"$wc/trunk/empty.bin"
	"$SVN" add -q "$wc/trunk/empty.bin"
	commit_wc "$wc" "add empty file"
	finish_fixture binary "$repo"
}

fixture_mergeinfo() {
	pair=$(new_fixture mergeinfo 05); repo=${pair%%|*}; wc=${pair#*|}
	printf 'trunk\n' >"$wc/trunk/file.txt"
	"$SVN" add -q "$wc/trunk/file.txt"
	commit_wc "$wc" "add merge source"
	"$SVN" copy -q "$wc/trunk" "$wc/branches/feature"
	commit_wc "$wc" "create feature branch"
	"$SVN" propset -q svn:mergeinfo '/trunk:2-3' "$wc/branches/feature"
	commit_wc "$wc" "record mergeinfo"
	finish_fixture mergeinfo "$repo"
}

fixture_locks() {
	pair=$(new_fixture locks 06); repo=${pair%%|*}; wc=${pair#*|}
	printf 'lock me\n' >"$wc/trunk/locked.txt"
	"$SVN" add -q "$wc/trunk/locked.txt"
	"$SVN" propset -q svn:needs-lock '*' "$wc/trunk/locked.txt"
	commit_wc "$wc" "add lockable file"
	finish_fixture locks "$repo"
}

fixture_unicode() {
	pair=$(new_fixture unicode 07); repo=${pair%%|*}; wc=${pair#*|}
	printf 'accent\n' >"$wc/trunk/café.txt"
	printf 'space\n' >"$wc/trunk/space name.txt"
	printf 'reserved\n' >"$wc/trunk/hash#percent%.txt"
	"$SVN" add -q "$wc/trunk/café.txt" "$wc/trunk/space name.txt" "$wc/trunk/hash#percent%.txt"
	commit_wc "$wc" "add unicode and escaped paths"
	finish_fixture unicode "$repo"
}

fixture_symlinks() {
	pair=$(new_fixture symlinks 08); repo=${pair%%|*}; wc=${pair#*|}
	printf 'target\n' >"$wc/trunk/target.txt"
	ln -s target.txt "$wc/trunk/link.txt"
	"$SVN" add -q "$wc/trunk/target.txt" "$wc/trunk/link.txt"
	commit_wc "$wc" "add symbolic link"
	"$SVN" delete -q "$wc/trunk/link.txt"
	printf 'ordinary file\n' >"$wc/trunk/link.txt"
	"$SVN" add -q "$wc/trunk/link.txt"
	commit_wc "$wc" "replace symbolic link with file"
	finish_fixture symlinks "$repo"
}

fixture_externals() {
	pair=$(new_fixture externals 09); repo=${pair%%|*}; wc=${pair#*|}
	cat >"$BUILD_DIR/externals.txt" <<'EOF'
^/trunk ext-trunk
-r 1 ^/tags ext-tag
../branches/feature ext-relative
EOF
	"$SVN" propset -q -F "$BUILD_DIR/externals.txt" svn:externals "$wc/trunk"
	commit_wc "$wc" "add external definitions"
	finish_fixture externals "$repo"
}

fixture_basic
fixture_copies
fixture_props
fixture_binary
fixture_mergeinfo
fixture_locks
fixture_unicode
fixture_symlinks
fixture_externals
