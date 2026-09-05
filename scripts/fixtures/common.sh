#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
PROJECT_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
OUTPUT_ROOT=${GOSVN_FIXTURE_OUTPUT:-"$PROJECT_ROOT/testdata"}

require_tool() {
	name=$1
	env_name=$2
	eval "override=\${$env_name:-}"
	if [ -n "$override" ]; then
		if [ ! -x "$override" ]; then
			echo "$env_name does not name an executable: $override" >&2
			exit 1
		fi
		printf '%s\n' "$override"
		return
	fi
	if ! command -v "$name" >/dev/null 2>&1; then
		echo "$name is required; install Subversion or set $env_name" >&2
		exit 1
	fi
	command -v "$name"
}

replace_dir() {
	source_dir=$1
	target_dir=$2
	rm -rf "$target_dir"
	mkdir -p "$(dirname -- "$target_dir")"
	mv "$source_dir" "$target_dir"
}
