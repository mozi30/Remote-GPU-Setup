# Sourced by every gpu-backpack script on the GPU box.
set -euo pipefail
BP=/opt/backpack
# shellcheck disable=SC1091
. "$BP/backpack.env"
export RCLONE_CONFIG="$BP/rclone.conf"
# ssh sessions on Vast do not inherit the image's ENV, so set what we rely on.
export PATH="/opt/conda/bin:/usr/local/bin:$PATH"
export UV_CACHE_DIR=/workspace/.cache/uv UV_LINK_MODE=copy
export HF_HOME=/workspace/.cache/huggingface TORCH_HOME=/workspace/.cache/torch
FILTER=(--exclude-from "$BP/excludes.txt")
RCLONE_FLAGS=(--transfers 16 --checkers 16 --fast-list --stats 15s --stats-one-line --stats-log-level NOTICE)

log()  { echo ">> $*"; }
warn() { echo "!! $*" >&2; }

ensure_rclone() {
  if command -v rclone >/dev/null 2>&1; then RCLONE=rclone; return; fi
  if [ -x "$BP/bin/rclone" ]; then RCLONE="$BP/bin/rclone"; return; fi
  log "installing rclone (not in image)"
  local tmp; tmp="$(mktemp -d)"
  curl -fsSL -o "$tmp/rclone.zip" https://downloads.rclone.org/rclone-current-linux-amd64.zip
  if command -v unzip >/dev/null 2>&1; then unzip -q "$tmp/rclone.zip" -d "$tmp"
  else python3 -m zipfile -e "$tmp/rclone.zip" "$tmp"; fi
  mkdir -p "$BP/bin"
  cp "$tmp"/rclone-*-linux-amd64/rclone "$BP/bin/rclone"
  chmod +x "$BP/bin/rclone"
  rm -rf "$tmp"
  RCLONE="$BP/bin/rclone"
}

ensure_uv() {
  command -v uv >/dev/null 2>&1 && return
  log "installing uv (not in image)"
  curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR=/usr/local/bin sh
}
