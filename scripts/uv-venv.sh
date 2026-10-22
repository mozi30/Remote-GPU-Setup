#!/usr/bin/env bash
# Create/refresh a uv virtualenv on the persistent workspace and install deps.
# Reuses the base image's torch/torchvision via --system-site-packages.
# Fast no-op if the venv already exists (lives on /workspace = persistent).
set -euo pipefail

VENV=/workspace/.venv
export UV_CACHE_DIR=/workspace/.cache/uv
mkdir -p "${UV_CACHE_DIR%/*}"

if [ ! -x "$VENV/bin/python" ]; then
  echo ">> creating venv at $VENV"
  uv venv --system-site-packages "$VENV"
fi

# shellcheck disable=SC1091
. "$VENV/bin/activate"

echo ">> installing requirements into venv (uv)"
uv pip install -r /opt/fast-remote-gpu/requirements.txt || \
  echo "!! requirements.txt not found; skipped"

echo ">> venv ready: $VENV (python $(python --version 2>&1))"
