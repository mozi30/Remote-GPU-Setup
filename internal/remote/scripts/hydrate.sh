#!/usr/bin/env bash
# Pull the workspace from R2 into /workspace. Writes $BP/hydrated on success;
# backup.sh refuses to push until that marker exists, so a failed or partial
# pull can never overwrite the copy in R2.
. /opt/backpack/common.sh
ensure_rclone
mkdir -p /workspace

if [ -f "$BP/hydrated" ]; then
  log "workspace already pulled on this box ($(cat "$BP/hydrated")) - skipping"
  exit 0
fi

log "checking $R2_DEST"
if ! listing="$($RCLONE lsf --max-depth 1 "$R2_DEST" 2>&1)"; then
  # S3-style stores report a missing prefix as "directory not found" = first run.
  if printf '%s' "$listing" | grep -qi "directory not found"; then
    listing=""
  else
    warn "cannot list R2: $listing"
    exit 1
  fi
fi

if [ -z "$listing" ]; then
  log "R2 workspace is empty (first run) - nothing to pull"
else
  log "pulling workspace from R2..."
  start=$(date +%s)
  $RCLONE copy "$R2_DEST" /workspace "${FILTER[@]}" "${RCLONE_FLAGS[@]}"
  log "pull done in $(( $(date +%s) - start ))s ($(du -sh /workspace 2>/dev/null | cut -f1))"
fi

mkdir -p /workspace/checkpoints /workspace/runs /workspace/.cache
date -u +%FT%TZ > "$BP/hydrated"
