#!/usr/bin/env sh
# Run the px0 Electron app with hot reload.
#   ./start-ide-dev.sh [px0 args, e.g. a repo path]
# Edits to web/src/** rebuild the bundle and reload the window; edits to *.go
# or electron/*.js rebuild and restart the app.
set -eu
HERE=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
START=$(pwd)
cd "$HERE"
[ -d electron/node_modules/electron ] || (cd electron && npm install)
[ $# -gt 0 ] || set -- "$START"
exec node scripts/dev.js --electron "$@"
