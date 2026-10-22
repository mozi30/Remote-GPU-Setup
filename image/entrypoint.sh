#!/usr/bin/env bash
set -euo pipefail

# Ensure workspace + cache dirs exist
mkdir -p /workspace/.cache/huggingface /workspace/.cache/torch \
         /workspace/.cache/pip /workspace/.cache/uv /workspace/checkpoints /workspace/runs

# Install the provided SSH public key (passed via env AUTHORIZED_KEY)
if [ -n "${AUTHORIZED_KEY:-}" ]; then
  echo "${AUTHORIZED_KEY}" > /root/.ssh/authorized_keys
  chmod 600 /root/.ssh/authorized_keys
  chmod 700 /root/.ssh
fi

# Start sshd in foreground style and keep container alive
mkdir -p /run/sshd
/usr/sbin/sshd -D -e &
SSHD_PID=$!

# Convenience banner
echo "fast-remote-gpu ready. workspace=/workspace  uv=$(uv --version 2>/dev/null || echo missing)"
nvidia-smi 2>/dev/null || echo "nvidia-smi not available"

# Wait on sshd so container stays up
wait $SSHD_PID
