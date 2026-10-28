#!/usr/bin/env bash
# autosave.sh start|stop|status
# The loop runs ON the GPU box, so backups continue even if the PC sleeps or
# the tool is closed.
. /opt/backpack/common.sh
PIDF="$BP/autosave.pid"
LOGF="$BP/autosave.log"

running() { [ -f "$PIDF" ] && kill -0 "$(cat "$PIDF")" 2>/dev/null; }

case "${1:-}" in
  loop)
    echo $$ > "$PIDF"
    while true; do
      sleep "$AUTOSAVE_SECONDS"
      bash "$BP/backup.sh" >>"$LOGF" 2>&1 || echo "$(date -u +%FT%TZ) autosave run failed" >>"$LOGF"
      # keep the log small
      tail -n 400 "$LOGF" > "$LOGF.tmp" 2>/dev/null && mv "$LOGF.tmp" "$LOGF"
    done
    ;;
  start)
    if running; then kill "$(cat "$PIDF")" 2>/dev/null || true; fi
    nohup setsid bash "$BP/autosave.sh" loop >/dev/null 2>&1 </dev/null &
    sleep 1
    running && log "autosave running every $((AUTOSAVE_SECONDS / 60)) min" || { warn "autosave failed to start"; exit 1; }
    ;;
  stop)
    # Kill only the loop; an in-flight backup.sh keeps running and the next
    # backup waits for it via the sync.lock.
    if running; then kill "$(cat "$PIDF")" 2>/dev/null || true; fi
    rm -f "$PIDF"
    log "autosave stopped"
    ;;
  status)
    running && echo "autosave=running" || echo "autosave=stopped"
    [ -f "$BP/last_backup_ok" ] && echo "last_ok=$(cat "$BP/last_backup_ok")"
    [ -f "$BP/last_backup_failed" ] && echo "last_failed=$(cat "$BP/last_backup_failed")"
    [ -f "$BP/hydrated" ] && echo "hydrated=$(cat "$BP/hydrated")"
    exit 0
    ;;
  *) echo "usage: autosave.sh start|stop|status"; exit 1 ;;
esac
