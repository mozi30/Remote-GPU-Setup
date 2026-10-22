#!/usr/bin/env bash
# Sync a local directory to the remote /workspace.
# Usage: ./scripts/sync.sh <local-path> [remote-subdir]
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"; cd "$HERE"
set -a; . ./.env; set +a

SRC="${1:?usage: sync.sh <local-path> [remote-subdir]}"
DEST="${2:-$(basename "$SRC")}"

echo ">> rsync $SRC -> fast-remote-gpu:/workspace/$DEST"
rsync -az --info=progress2 \
  -e "ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null" \
  --exclude '.git' --exclude '__pycache__' --exclude '.venv' \
  "$SRC" "root@fast-remote-gpu:/workspace/${DEST}"
echo ">> done"
