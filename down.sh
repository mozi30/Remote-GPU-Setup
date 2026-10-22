#!/usr/bin/env bash
# fast-remote-gpu — destroy the tracked instance (stops billing).
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; cd "$HERE"
set -a; . ./.env; set +a
STATE_FILE="${STATE_FILE:-.instance}"

if [ ! -s "$STATE_FILE" ]; then
  echo ">> no tracked instance ($STATE_FILE empty). nothing to do."
  exit 0
fi

BACKEND="backends/${PROVIDER}.sh"
echo ">> destroying instance via $PROVIDER"
"$BACKEND" destroy || echo "!! destroy reported an error; check provider dashboard."

: > "$STATE_FILE"
echo ">> done. billing should stop now."
