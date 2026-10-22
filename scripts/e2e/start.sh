#!/bin/bash
# E2E: sukompiliuoja mircBSD ir paleidžia 24 patikras.
# Artefaktai (home/, drive.log) → /tmp/mircbsd-e2e, repo lieka švarus.
set -e
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DIR=/tmp/mircbsd-e2e
mkdir -p "$DIR/bin"
echo ">> Kompiliuojam mircBSD → $DIR/bin/mircbsd"
go build -o "$DIR/bin/mircbsd" "$REPO_ROOT"
export MIRCBSD_E2E_DIR="$DIR"
export MIRCBSD_BIN="$DIR/bin/mircbsd"
exec python3 "$REPO_ROOT/scripts/e2e/check.py"