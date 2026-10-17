#!/usr/bin/env sh
# Run px0 in the browser with hot reload.
#   ./start-dev.sh [px0 args, e.g. a repo path or --port 8080]
# Edits to web/src/** rebuild the bundle and reload the page; edits to *.go
# rebuild and restart the server. Defaults to the directory you run it from.
set -eu
HERE=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
START=$(pwd)
cd "$HERE"
[ $# -gt 0 ] || set -- "$START"
exec node scripts/dev.js "$@"
