#!/usr/bin/env bash
# RunPod backend for fast-remote-gpu (uses the RunPod REST API via curl).
# Subcommands: create | destroy
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"; cd "$HERE/.."
set -a; . ./.env; set +a
STATE_FILE="${STATE_FILE:-.instance}"

: "${RUNPOD_API_KEY:?set RUNPOD_API_KEY in .env}"
API="https://rest.runpod.io/v1"
AUTH="Authorization: Bearer ${RUNPOD_API_KEY}"

py() { python -c "$1"; }

# RunPod GPU type ids: map human names -> runpod ids (extend as needed).
rp_gpu_id() {
  case "$1" in
    "RTX 5090") echo "NVIDIA GeForce RTX 5090";;
    "RTX 5080") echo "NVIDIA GeForce RTX 5080";;
    "RTX 4090") echo "NVIDIA GeForce RTX 4090";;
    "RTX 4080 SUPER"|"RTX 4080") echo "NVIDIA GeForce RTX 4080 SUPER";;
    "RTX 3090") echo "NVIDIA GeForce RTX 3090";;
    *) return 1;;
  esac
}

case "$1" in
  create)
    # Build candidate gpuTypeIds
    ids=""
    IFS=',' read -ra NAMES <<< "${GPU_NAMES:-RTX 5090,RTX 4090}"
    for n in "${NAMES[@]}"; do
      n="${n#"${n%%[![:space:]]*}"}"; n="${n%"${n##*[![:space:]]}"}"
      if id="$(rp_gpu_id "$n")"; then ids+="${ids:+,}\"$id\""; fi
    done
    ids="[${ids}]"
    echo ">> RunPod candidate gpu types: $ids"

    PUBKEY="$(cat "${SSH_KEY/#\~/$HOME}.pub")"
    BODY="$(python - "$ids" "$IMAGE" "$DISK_GB" "$PUBKEY" <<'PY'
import json,sys
ids=json.loads(sys.argv[1]); image=sys.argv[2]; disk=int(sys.argv[3]); pub=sys.argv[4]
print(json.dumps({
  "name":"fast-remote-gpu",
  "imageName":image,
  "gpuTypeIds":ids,
  "gpuCount":1,
  "containerDiskInGb":disk,
  "volumeInGb":disk,
  "volumeMountPath":"/workspace",
  "ports":"22/tcp",
  "env":{"AUTHORIZED_KEY":pub},
  "dockerEntrypoint":["/usr/local/bin/entrypoint.sh"],
}))
PY
)"
    RAW="$(curl -s -X POST "$API/pods" -H "$AUTH" -H "Content-Type: application/json" -d "$BODY")"
    POD_ID="$(echo "$RAW" | py 'import sys,json;d=json.load(sys.stdin);print(d.get("id",""))')"
    [ -n "$POD_ID" ] || { echo "!! create failed: $RAW"; exit 1; }
    echo ">> pod $POD_ID created; waiting for public IP + SSH port..."

    for i in $(seq 1 60); do
      INFO="$(curl -s "$API/pods/$POD_ID" -H "$AUTH")"
      HOST="$(echo "$INFO" | py 'import sys,json;d=json.load(sys.stdin);p=d.get("publicIp","");print(p if p and p!="0.0.0.0" else "")')"
      PORT="$(echo "$INFO" | py 'import sys,json;d=json.load(sys.stdin);m=(d.get("portMappings") or {});print(m.get("22") or m.get("22/tcp") or "")')"
      if [ -n "$HOST" ] && [ -n "$PORT" ]; then
        echo "$POD_ID $HOST $PORT" > "$STATE_FILE"
        echo ">> running at $HOST:$PORT"
        exit 0
      fi
      sleep 10
    done
    echo "!! pod did not expose SSH in time."
    exit 1
    ;;
  destroy)
    read -r POD_ID _ < <(head -n1 "$STATE_FILE")
    [ -n "$POD_ID" ] && curl -s -X DELETE "$API/pods/$POD_ID" -H "$AUTH" || true
    ;;
  *) echo "usage: runpod.sh create|destroy"; exit 1;;
esac
