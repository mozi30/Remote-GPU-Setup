#!/usr/bin/env bash
# Vast.ai backend for fast-remote-gpu.
# Requires the `vastai` CLI:  pip install vastai   (or uv tool install vastai)
# Subcommands: create | destroy
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; cd "$HERE/.."
set -a; . ./.env; set +a
STATE_FILE="${STATE_FILE:-.instance}"

: "${VAST_API_KEY:?set VAST_API_KEY in .env}"
command -v vastai >/dev/null || { echo "!! vastai CLI not found (pip install vastai)"; exit 1; }

# vastai reads key from ~/.vast_api_key or --api-key
export VAST_API_KEY

# Build a query: datacenter/price-ordered offers matching GPU allow-list.
# vast.ai gpu names use underscores, e.g. "RTX_5080", "RTX_4090".
gpu_query=""
IFS=',' read -ra NAMES <<< "${GPU_NAMES:-RTX 5080,RTX 4090}"
for n in "${NAMES[@]}"; do
  g="# ${n#"${n%%[![:space:]]*}"}"      # trim leading space
  g="${g// /_}"
  gpu_query+="${gpu_query:+ }${g}"
done

case "$1" in
  create)
    echo ">> searching Vast offers: gpus=[$gpu_query] max=\$${MAX_PRICE}/hr disk=${DISK_GB}GB"
    # --order dph_total : cheapest first. --raw for parsing.
    OFFERS_JSON="$(vastai search offers \
        "gpu_name in [${gpu_query// /,}] dph_total<=${MAX_PRICE} disk_space>=${DISK_GB} rentable=true" \
        --order dph_total --limit 5 --raw)"
    OFFER_ID="$(echo "$OFFERS_JSON" | python -c 'import sys,json;d=json.load(sys.stdin);print(d[0]["id"] if d else "")')"
    if [ -z "$OFFER_ID" ]; then
      echo "!! no matching offers. loosen GPU_NAMES or raise MAX_PRICE."
      exit 1
    fi
    echo ">> renting offer $OFFER_ID"

    # env for the container: install ssh public key
    PUBKEY="$(cat "${SSH_KEY/#\~/$HOME}.pub")"
    ENV_JSON="$(python -c 'import json,sys;print(json.dumps({"-e AUTHORIZED_KEY":sys.argv[1]}))' "$PUBKEY")"

    RAW="$(vastai create instance "$OFFER_ID" \
        --image "$IMAGE" \
        --disk "$DISK_GB" \
        --ssh --direct \
        --env "$ENV_JSON" --raw)"
    INST_ID="$(echo "$RAW" | python -c 'import sys,json;d=json.load(sys.stdin);print(d.get("new_contract"))')"

    echo ">> instance $INST_ID created; waiting for start..."
    for i in $(seq 1 60); do
      INFO="$(vastai show instance "$INST_ID" --raw 2>/dev/null || echo '{}')"
      STATUS="$(echo "$INFO" | python -c 'import sys,json;d=json.load(sys.stdin);print(d.get("actual_status",""))' 2>/dev/null || echo "")"
      HOST="$(echo "$INFO" | python -c 'import sys,json;d=json.load(sys.stdin);print(d.get("public_ipaddr",""))' 2>/dev/null || echo "")"
      PORT="$(echo "$INFO" | python -c 'import sys,json;d=json.load(sys.stdin);d=d.get("ports",{}) or {};v=d.get("22/tcp") or [{}];print((v[0] or {}).get("HostPort",""))' 2>/dev/null || echo "")"
      if [ "$STATUS" = "running" ] && [ -n "$HOST" ] && [ -n "$PORT" ]; then
        echo "$INST_ID $HOST $PORT" > "$STATE_FILE"
        echo ">> running at $HOST:$PORT"
        exit 0
      fi
      sleep 10
    done
    echo "!! instance did not reach running state in time."
    exit 1
    ;;
  destroy)
    read -r INST_ID _ < <(head -n1 "$STATE_FILE")
    [ -n "$INST_ID" ] && vastai destroy instance "$INST_ID" --raw || true
    ;;
  *) echo "usage: vast.sh create|destroy"; exit 1;;
esac
