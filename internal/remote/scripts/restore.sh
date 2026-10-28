#!/usr/bin/env bash
# restore.sh <snapshot> [relative/path]
# Copy files from an R2 trash snapshot back into /workspace on the running box
# (restoring only in R2 would be undone by the next autosave sync).
. /opt/backpack/common.sh
ensure_rclone
snap="${1:?snapshot}"; rel="${2:-}"
case "$snap/$rel" in *..*) warn "invalid path"; exit 1 ;; esac
if [ -z "$rel" ]; then
  log "restoring whole snapshot $snap"
  $RCLONE copy "$R2_TRASH/$snap" /workspace "${RCLONE_FLAGS[@]}"
else
  log "restoring $rel from $snap"
  $RCLONE copyto "$R2_TRASH/$snap/$rel" "/workspace/$rel"
fi
