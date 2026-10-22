#!/usr/bin/env bash
# fast-remote-gpu — connect + forward tensorboard/jupyter ports.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; cd "$HERE"
set -a; . ./.env; set +a
TB="${TENSORBOARD_PORT:-6006}"
JUP="${JUPYTER_PORT:-8888}"

exec ssh -L "${TB}:localhost:${TB}" -L "${JUP}:localhost:${JUP}" \
  fast-remote-gpu "$@"
