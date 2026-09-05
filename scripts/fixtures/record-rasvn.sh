#!/bin/sh
set -eu

if [ "$#" -ne 4 ]; then
	echo "usage: record-rasvn.sh SVN SVNSERVE REPOSITORY OUTPUT" >&2
	exit 2
fi

SVN=$1
SVNSERVE=$2
REPOSITORY=$3
OUTPUT=$4

exec go run ./scripts/fixtures/rasvn-record \
	-svn "$SVN" \
	-svnserve "$SVNSERVE" \
	-repository "$REPOSITORY" \
	-output "$OUTPUT"
