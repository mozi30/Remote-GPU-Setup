#!/usr/bin/env bash
# fast-remote-gpu — connect to an already-running instance and open VS Code.
# Does NOT rent anything. If no instance is tracked/running, tells you to run ./up.sh.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; cd "$HERE"

# ---- load config ----
if [ ! -f .env ]; then
  echo "!! .env not found (needed for SSH_KEY / STATE_FILE / alias)."
  exit 1
fi
set -a; . ./.env; set +a

STATE_FILE="${STATE_FILE:-.instance}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/fast_remote_gpu}"
SSH_HOST_ALIAS="${SSH_HOST_ALIAS:-fast-remote-gpu}"
OPEN_VSCODE="${OPEN_VSCODE:-1}"
SSH_KEY="${SSH_KEY/#\~/$HOME}"

# ---- is an instance tracked? ----
if [ ! -s "$STATE_FILE" ]; then
  echo ">> no instance tracked in $STATE_FILE."
  echo "   start one with: ./up.sh"
  exit 1
fi
read -r INST_ID HOST PORT < <(head -n1 "$STATE_FILE")
if [ -z "${INST_ID:-}" ] || [ -z "${HOST:-}" ] || [ -z "${PORT:-}" ]; then
  echo "!! $STATE_FILE is malformed (expected: ID HOST PORT). run ./down.sh then ./up.sh."
  exit 1
fi

echo ">> tracked instance: $INST_ID ($HOST:$PORT)"

# ---- if using vast, check the real instance status (running/stopped/gone) ----
if [ "${PROVIDER:-}" = "vast" ] && command -v vastai >/dev/null 2>&1; then
  st_info="$(vastai show instance "$INST_ID" --raw 2>/dev/null || echo '{}')"
  st="$(echo "$st_info" | python -c 'import sys,json
try: d=json.load(sys.stdin)
except Exception: d={}
print((d.get("actual_status") or d.get("cur_state") or "").lower())' 2>/dev/null || echo "")"
  case "$st" in
    running) echo ">> vast reports: running" ;;
    stopped|exited|"") 
      if [ "$st" = "stopped" ] || [ "$st" = "exited" ]; then
        echo ">> vast reports: $st — attempting to START instance $INST_ID"
        vastai start instance "$INST_ID" --raw >/dev/null 2>&1 || true
        echo ">> waiting for it to come back..."
        for i in $(seq 1 30); do
          sleep 5
          s2="$(vastai show instance "$INST_ID" --raw 2>/dev/null | python -c 'import sys,json
try: d=json.load(sys.stdin)
except Exception: d={}
print((d.get("actual_status") or "").lower())' 2>/dev/null || echo "")"
          [ "$s2" = "running" ] && { echo ">> running"; break; }
        done
      fi
      ;;
    *) echo ">> vast reports: ${st:-unknown}" ;;
  esac
fi

# ---- pick the key that actually authenticates (our key, else default/agent) ----
KEY_OPT=()
probe() {
  ssh "$@" -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o BatchMode=yes -o ConnectTimeout=6 root@"$HOST" true 2>/dev/null
}
if [ -f "$SSH_KEY" ] && probe -i "$SSH_KEY"; then
  KEY_OPT=(-i "$SSH_KEY"); echo ">> auth ok with $SSH_KEY"
elif probe; then
  echo ">> auth ok with default key/agent (not $SSH_KEY)"
else
  echo "!! cannot authenticate to $HOST:$PORT."
  echo "   - is the instance still running? check the provider dashboard / ./up.sh"
  echo "   - debug: ssh -i $SSH_KEY -p $PORT root@$HOST -v"
  exit 1
fi

# ---- ensure ~/.ssh/config host entry matches current instance ----
CONF="$HOME/.ssh/config"
touch "$CONF"; chmod 600 "$CONF"
awk -v host="$SSH_HOST_ALIAS" '
  $1=="Host" && $2==host {skip=1; next}
  /^Host / {skip=0}
  !skip {print}
' "$CONF" > "${CONF}.tmp" && mv "${CONF}.tmp" "$CONF"
cat >> "$CONF" <<EOF

Host $SSH_HOST_ALIAS
    HostName $HOST
    Port $PORT
    User root
    IdentityFile $SSH_KEY
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
    ServerAliveInterval 30
EOF
echo ">> ~/.ssh/config updated for host '$SSH_HOST_ALIAS'"

# ---- ensure /workspace exists (in case it was a pre-.keep image) ----
ssh "${KEY_OPT[@]}" -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o BatchMode=yes root@"$HOST" "mkdir -p /workspace" 2>/dev/null || true

# ---- open VS Code (or print how) ----
echo ">> you can also: ssh $SSH_HOST_ALIAS   |   ./ssh.sh (ports)"
if [ "$OPEN_VSCODE" = "1" ]; then
  if command -v code >/dev/null 2>&1; then
    echo ">> opening VS Code on ${SSH_HOST_ALIAS}:/workspace ..."
    code --remote "ssh-remote+${SSH_HOST_ALIAS}" /workspace >/dev/null 2>&1 || \
      echo "   (code CLI failed; run: code --remote ssh-remote+${SSH_HOST_ALIAS} /workspace)"
  else
    echo ">> 'code' CLI not on PATH; open manually: code --remote ssh-remote+${SSH_HOST_ALIAS} /workspace"
  fi
fi
