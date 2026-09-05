#!/bin/sh
set -eu

. "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/common.sh"

SVN=$(require_tool svn GOSVN_SVN)
SVNSERVE=$(require_tool svnserve GOSVN_SVNSERVE)
WORK_ROOT=${1:?usage: generate-transcripts.sh WORK_ROOT}
TRANSCRIPT_DIR="$WORK_ROOT/transcripts"
REPO="$WORK_ROOT/build-dumps/basic.repo"
mkdir -p "$TRANSCRIPT_DIR/rasvn" "$TRANSCRIPT_DIR/dav"

if [ ! -d "$REPO" ]; then
	echo "missing generated repository: $REPO" >&2
	exit 1
fi

"$PROJECT_ROOT/scripts/fixtures/record-rasvn.sh" \
	"$SVN" "$SVNSERVE" "$REPO" "$TRANSCRIPT_DIR/rasvn/info.transcript"

go run ./scripts/fixtures/rasvn-record \
	-svn "$SVN" \
	-svnserve "$SVNSERVE" \
	-repository "$REPO" \
	-operation read-matrix \
	-output "$TRANSCRIPT_DIR/rasvn/read-matrix.transcript"

cat >"$TRANSCRIPT_DIR/dav/options-request.xml" <<'EOF'
<?xml version="1.0" encoding="utf-8"?>
<D:options xmlns:D="DAV:"><D:activity-collection-set/></D:options>
EOF

cat >"$TRANSCRIPT_DIR/dav/README.txt" <<'EOF'
The committed seed is the deterministic OPTIONS discovery body. To record a
live DAV exchange, set GOSVN_DAV_URL to a mod_dav_svn repository URL and use
the Phase 6 HTTP recorder when it is implemented. Raw credentials and TLS
secrets must never be committed.
EOF
