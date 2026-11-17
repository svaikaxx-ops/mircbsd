#!/bin/bash
# E2E: sukompiliuoja mircBSD ir paleidžia 24 patikras.
# Artefaktai rašomi į naują laikiną katalogą.
set -eu
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DIR="$(mktemp -d "${TMPDIR:-/tmp}/mircbsd-e2e.XXXXXXXX")"
mkdir -p "$DIR/bin"
echo ">> Kompiliuojam mircBSD → $DIR/bin/mircbsd"
(cd "$REPO_ROOT" && cargo build --release --locked --quiet)
cp "$REPO_ROOT/target/release/mircbsd" "$DIR/bin/mircbsd"
export MIRCBSD_E2E_DIR="$DIR"
export MIRCBSD_BIN="$DIR/bin/mircbsd"
exec python3 "$REPO_ROOT/scripts/e2e/check.py"
