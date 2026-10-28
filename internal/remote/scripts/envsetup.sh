#!/usr/bin/env bash
# Rebuild Python envs (never synced to R2) for every project in /workspace:
#   uv.lock present        -> isolated `uv sync` (exact lockfile, installs its own torch)
#   requirements.txt only  -> .venv layered on the image's torch (fast, driver-matched)
#   pyproject.toml only    -> `uv sync` (creates uv.lock)
# A failing project is reported but does not stop the others.
. /opt/backpack/common.sh
ensure_uv
SYS_PY="$(command -v python3 || command -v python)"

mapfile -t dirs < <(
  find /workspace -maxdepth 3 \
    \( -name .venv -o -name .cache -o -name node_modules -o -name .git -o -name data \) -prune -o \
    \( -name pyproject.toml -o -name requirements.txt \) -print 2>/dev/null \
  | xargs -r -n1 dirname | sort -u
)

if [ "${#dirs[@]}" -eq 0 ]; then
  log "no Python projects found (no pyproject.toml / requirements.txt)"
  exit 0
fi

fail=0
for d in "${dirs[@]}"; do
  cd "$d"
  if [ -f uv.lock ]; then
    log "$d: uv.lock -> isolated uv sync"
    uv sync --frozen || uv sync || { warn "$d: uv sync failed"; fail=1; }
  elif [ -f requirements.txt ]; then
    log "$d: requirements.txt -> venv on top of image torch"
    [ -x .venv/bin/python ] || uv venv --system-site-packages --python "$SYS_PY" .venv \
      || { warn "$d: creating .venv failed"; fail=1; continue; }
    # torch comes from the image (CUDA-matched); never reinstall it here.
    grep -viE '^[[:space:]]*(torch|torchvision|torchaudio)([[:space:]<>=~!;\[]|$)' requirements.txt > /tmp/bp-req.txt || true
    uv pip install --python .venv/bin/python -r /tmp/bp-req.txt || { warn "$d: install failed"; fail=1; }
  else
    log "$d: pyproject.toml without uv.lock -> uv sync"
    uv sync || { warn "$d: uv sync failed"; fail=1; }
  fi
done
[ "$fail" = 0 ] && log "python envs ready" || warn "some python envs failed (see above)"
exit 0
