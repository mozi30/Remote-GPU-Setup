#!/usr/bin/env bash
# Push /workspace to R2. Files deleted or overwritten in R2 are moved into
# trash/<timestamp>/ (--backup-dir) instead of being lost, and --max-delete
# caps how much a single run may remove.
. /opt/backpack/common.sh

if [ ! -f "$BP/hydrated" ]; then
  warn "refusing to push: the workspace was never pulled from R2 on this box,"
  warn "so pushing could overwrite your R2 copy with an empty/partial one."
  exit 3
fi
ensure_rclone
# one-off override from the PC after the user confirmed a large deletion
[ -n "${BP_MAX_DELETE:-}" ] && MAX_DELETE="$BP_MAX_DELETE"

# One sync at a time (autosave vs. manual/final backup).
exec 9>"$BP/sync.lock"
flock 9

ts="$(date -u +%Y%m%dT%H%M%SZ)"
log "backup -> $R2_DEST"
start=$(date +%s)
if $RCLONE sync /workspace "$R2_DEST" "${FILTER[@]}" "${RCLONE_FLAGS[@]}" \
     --backup-dir "$R2_TRASH/$ts" --max-delete "$MAX_DELETE"; then
  date -u +%FT%TZ > "$BP/last_backup_ok"
  rm -f "$BP/last_backup_failed"
  log "backup ok in $(( $(date +%s) - start ))s"
else
  rc=$?
  echo "$(date -u +%FT%TZ) rc=$rc" > "$BP/last_backup_failed"
  warn "backup FAILED (rclone exit $rc)"
  exit "$rc"
fi
