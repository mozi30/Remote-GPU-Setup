#!/usr/bin/env bash
# OPTIONAL: create a uv venv on /workspace that REUSES the image's torch.
#
# Goals:
#   - shortest boot      -> opt-in only (USE_VENV=1). Default: use system python.
#   - max compatibility  -> NEVER install torch/torchvision if they already exist.
#     They come from the image (driver-matched CUDA). Installing newer torch
#     (e.g. cu130 on a cu128 image) is the exact risk we avoid.
#     Only install torch/torchvision when they are genuinely missing.
set -euo pipefail

VENV=/workspace/.venv
export UV_CACHE_DIR=/workspace/.cache/uv
mkdir -p "${UV_CACHE_DIR%/*}"

REQS=/opt/fast-remote-gpu/requirements.txt

if [ ! -x "$VENV/bin/python" ]; then
  echo ">> creating venv at $VENV (system site-packages enabled to reuse image torch)"
  uv venv --system-site-packages "$VENV"
fi
PYBIN="$VENV/bin/python"

# If torch already exists (via system site-packages), never (re)install it.
have_torch=0
if "$PYBIN" -c 'import torch' >/dev/null 2>&1; then have_torch=1; fi

if [ "$have_torch" = "1" ]; then
  echo ">> torch already present — NOT installing torch/torchvision"
  if [ -f "$REQS" ]; then
    grep -viE '^\s*(torch|torchvision|torchaudio)\b' "$REQS" > /tmp/req-notorch.txt || true
    echo ">> installing remaining requirements"
    uv pip install --python "$PYBIN" -r /tmp/req-notorch.txt
  fi
else
  echo ">> torch NOT found — installing full requirements (including torch)"
  [ -f "$REQS" ] && uv pip install --python "$PYBIN" -r "$REQS"
fi

# Report which torch the venv resolves to (should be the image's cu12.8 build).
"$PYBIN" - <<'PY' || true
import torch
print(">> venv torch:", torch.__version__, "| cuda:", torch.version.cuda, "| available:", torch.cuda.is_available())
PY
echo ">> venv ready: $VENV"
