#!/usr/bin/env bash
# Confirm every (non-excluded) file in /workspace exists in R2 with the same
# size/hash. Used as the last gate before an instance is destroyed.
. /opt/backpack/common.sh
ensure_rclone
exec 9>"$BP/sync.lock"
flock 9
log "verifying /workspace against R2..."
$RCLONE check /workspace "$R2_DEST" "${FILTER[@]}" --one-way --fast-list --checkers 16
log "verify ok: everything on the box is in R2"
