#!/usr/bin/env bash
# fast-remote-gpu — rent a GPU, wait for SSH, register ~/.ssh/config entry.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$HERE"

# shellcheck source=lib/sshconf.sh
. "$HERE/lib/sshconf.sh"

# ---- load config ----
if [ ! -f .env ]; then
  echo "!! .env not found. Copy the template from README and fill in your keys:"
  echo "   (see .env.example in the README) -> .env"
  exit 1
fi
set -a; . ./.env; set +a

: "${PROVIDER:?set PROVIDER=vast|runpod in .env}"
: "${IMAGE:?set IMAGE in .env}"
MODE="${MODE:-interruptible}"
MAX_PRICE="${MAX_PRICE:-0.60}"
DISK_GB="${DISK_GB:-100}"
STATE_FILE="${STATE_FILE:-.instance}"
SSH_KEY="${SSH_KEY:-$HOME/.ssh/fast_remote_gpu}"
SSH_HOST_ALIAS="fast-remote-gpu"

# ---- ensure ssh key ----
SSH_KEY="${SSH_KEY/#\~/$HOME}"
if [ ! -f "$SSH_KEY" ]; then
  echo ">> generating SSH key at $SSH_KEY"
  mkdir -p "$(dirname "$SSH_KEY")"
  ssh-keygen -t ed25519 -N "" -f "$SSH_KEY" -C "fast-remote-gpu" >/dev/null
fi
PUBKEY="$(cat "${SSH_KEY}.pub")"

# ---- already running? ----
if [ -f "$STATE_FILE" ] && [ -s "$STATE_FILE" ]; then
  echo ">> an instance is already tracked in $STATE_FILE:"
  cat "$STATE_FILE"
  echo "   run ./down.sh first if you want a fresh one."
  exit 0
fi

# ---- dispatch to backend ----
BACKEND="backends/${PROVIDER}.sh"
[ -x "$BACKEND" ] || { echo "!! unknown PROVIDER=$PROVIDER (no $BACKEND)"; exit 1; }

echo ">> provisioning via $PROVIDER (mode=$MODE, max=\$$MAX_PRICE/hr)"
"$BACKEND" create

# backend is expected to write: ID HOST PORT  into $STATE_FILE
read -r INST_ID HOST PORT < <(head -n1 "$STATE_FILE")

echo ">> waiting for SSH on $HOST:$PORT ..."
SSH_OK=0
for i in $(seq 1 60); do
  # Verify we get real output back, not just a zero exit code: some providers
  # accept TCP but drop auth, which used to make this loop print "SSH is up".
  out="$(ssh -i "$SSH_KEY" -p "$PORT" -o StrictHostKeyChecking=no \
              -o UserKnownHostsFile=/dev/null -o BatchMode=yes -o ConnectTimeout=5 \
              root@"$HOST" 'echo READY' 2>/dev/null || true)"
  if [ "$out" = "READY" ]; then
    SSH_OK=1; echo ">> SSH is up"; break
  fi
  sleep 5
done
if [ "$SSH_OK" != "1" ]; then
  echo "!! timed out waiting for SSH on $HOST:$PORT"
  echo "   debug: ssh -i $SSH_KEY -p $PORT root@$HOST -v"
  echo "   instance kept running: $INST_ID  (destroy with ./down.sh)"
  exit 1
fi

# ---- ensure /workspace on the box; optionally build a venv (USE_VENV=1) ----
# Default (USE_VENV unset/0): use the image's SYSTEM python — shortest boot,
# driver-matched torch already installed. Only build a venv when asked.
USE_VENV="${USE_VENV:-0}"
echo ">> ensuring /workspace on remote (venv=${USE_VENV})..."
# Hard requirement: VS Code opens /workspace, so fail loudly instead of
# continuing with a directory that does not exist.
ws_out="$(ssh -i "$SSH_KEY" -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
    -o BatchMode=yes root@"$HOST" 'mkdir -p /workspace && ls -ld /workspace' 2>&1 || true)"
case "$ws_out" in
  *" /workspace"*) echo ">> /workspace ready: $(printf '%s' "$ws_out" | tail -n1)" ;;
  *)
    echo "!! could not create /workspace on $HOST:$PORT"
    echo "   output: $ws_out"
    echo "   instance kept running: $INST_ID  (debug with ./ssh.sh, destroy with ./down.sh)"
    exit 1
    ;;
esac

if [ "$USE_VENV" = "1" ]; then
  echo ">> building /workspace/.venv (reuses image torch; never reinstalls it if present)"
  ssh -i "$SSH_KEY" -p "$PORT" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      -o BatchMode=yes root@"$HOST" "bash /opt/fast-remote-gpu/uv-venv.sh" 2>/dev/null || \
      echo ">> note: venv setup failed (continuing; use system python)"
fi

# ---- write ssh config entry ----
# VS Code on Windows reads C:\Users\<you>\.ssh\config with Windows OpenSSH,
# which cannot parse POSIX paths like /c/Users/... — so the IdentityFile there
# must be Windows-style. We always write the POSIX config for this shell, and
# additionally the Windows config when it is a different file.
POSIX_CONF="$HOME/.ssh/config"
sshconf_write "$POSIX_CONF" "$SSH_HOST_ALIAS" "$HOST" "$PORT" "$SSH_KEY"
echo ">> wrote $POSIX_CONF  IdentityFile=$SSH_KEY"

if sshconf_is_windows; then
  WCONF="$(sshconf_win_config_path)"
  WINKEY="$(winpath "$SSH_KEY")"
  sshconf_write "$WCONF" "$SSH_HOST_ALIAS" "$HOST" "$PORT" "$WINKEY"
  echo ">> wrote $WCONF (VS Code)  IdentityFile=$WINKEY"
fi

echo ""
echo ">> done. connect with:"
echo "   ssh $SSH_HOST_ALIAS"
echo "   code --remote ssh-remote+$SSH_HOST_ALIAS /workspace"
echo "   ./ssh.sh   (also forwards tensorboard/jupyter ports)"

# ---- auto-open VS Code on the remote workspace (OPEN_VSCODE=1, default on) ----
OPEN_VSCODE="${OPEN_VSCODE:-1}"
if [ "$OPEN_VSCODE" = "1" ]; then
  if command -v code >/dev/null 2>&1; then
    echo ">> opening VS Code on ${SSH_HOST_ALIAS}:/workspace ..."
    code --remote "ssh-remote+${SSH_HOST_ALIAS}" /workspace >/dev/null 2>&1 || \
      echo "   (code CLI failed; open manually: code --remote ssh-remote+${SSH_HOST_ALIAS} /workspace)"
  else
    echo ">> 'code' CLI not on PATH; skipping auto-open."
    echo "   In VS Code: Ctrl+Shift+P → 'Shell Command: Install code command in PATH', then rerun."
  fi
fi
