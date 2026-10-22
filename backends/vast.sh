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

PY=python
command -v $PY >/dev/null || PY=python3

# "RTX 5060 Ti" -> "RTX_5060_Ti" (vast uses underscores in gpu_name)
to_vast_name() {
  local s="$1"
  s="${s#"${s%%[![:space:]]*}"}"       # ltrim
  s="${s%"${s##*[![:space:]]}"}"       # rtrim
  echo "${s// /_}"
}

case "$1" in
  create)
    # ---- 0. reuse an already-running instance (avoid double billing) ----
    if [ "${REUSE:-1}" = "1" ]; then
      existing="$(vastai show instances --raw 2>/dev/null | $PY -c '
import sys,json
try: arr=json.load(sys.stdin)
except Exception: arr=[]
arr=[i for i in arr if (i.get("actual_status") or i.get("cur_state") or "").lower() in ("running","created")]
# prefer ones we recognize by our container name/label
mine=[i for i in arr if "fast-remote-gpu" in json.dumps(i).lower()]
pick=(mine or arr)
print(f"{len(pick)}")
if pick:
    i=pick[0]
    ports=i.get("ports",{}) or {}
    v=ports.get("22/tcp") or [{}]
    host=i.get("public_ipaddr","") or ""
    port=(v[0] or {}).get("HostPort","")
    print(i.get("id",""))
    print(host)
    print(port)
')"
      reusecount="$(echo "$existing" | head -n1)"
      if [ -n "$reusecount" ] && [ "$reusecount" != "0" ]; then
        r_id="$(echo "$existing" | sed -n 2p)"
        r_host="$(echo "$existing" | sed -n 3p)"
        r_port="$(echo "$existing" | sed -n 4p)"
        if [ -n "$r_id" ] && [ -n "$r_host" ] && [ -n "$r_port" ]; then
          echo ">> found running instance $r_id ($r_host:$r_port) — reusing it (set REUSE=0 to rent new)"
          echo "$r_id $r_host $r_port" > "$STATE_FILE"
          exit 0
        fi
      fi
    fi

    # ---- 1. gather offers (one search per GPU name, robust) ----
    tmp="$(mktemp)"; echo '[]' > "$tmp"
    IFS=',' read -ra NAMES <<< "${GPU_NAMES:-RTX 5080,RTX 4090}"
    for n in "${NAMES[@]}"; do
      [ -z "${n// /}" ] && continue
      vname="$(to_vast_name "$n")"
      echo ">> querying: $vname  (<= \$${MAX_PRICE}/hr, disk>=${DISK_GB}GB)"
      q="gpu_name=${vname} dph_total<=${MAX_PRICE} disk_space>=${DISK_GB} rentable=true"
      vastai search offers "$q" --order dph_total --limit 5 --raw 2>/dev/null \
        | $PY -c 'import sys,json
try: d=json.load(sys.stdin)
except Exception: d=[]
print(json.dumps(d if isinstance(d,list) else []))' 2>/dev/null > "$tmp.cur" || echo '[]' > "$tmp.cur"
      $PY - "$tmp" "$tmp.cur" <<'PY'
import json,sys
acc=json.load(open(sys.argv[1])); cur=json.load(open(sys.argv[2]))
seen={o.get("id") for o in acc}
for o in cur:
    if o.get("id") not in seen: acc.append(o)
json.dump(acc,open(sys.argv[1],"w"))
PY
    done
    rm -f "$tmp.cur"

    # ---- 2. present sorted table ----
    COUNT="$($PY -c 'import json,sys;print(len(json.load(open(sys.argv[1]))))' "$tmp")"
    if [ "$COUNT" = "0" ]; then
      echo "!! no matching offers."
      echo "   Fixes: raise MAX_PRICE (now \$${MAX_PRICE}), reduce DISK_GB (now ${DISK_GB}),"
      echo "          or loosen GPU_NAMES."
      exit 1
    fi

    echo ""
    echo "   #  GPU                 VRAM   \$/hr    dlpf  rel  CPUc  RAM   up/down(Mb)  geo                    offer"
    echo "  --- ------------------ ------ ------- ----- ---- ----- ----- ------------ ---------------------- ---------"
    $PY - "$tmp" <<'PY'
import json,sys
offers=json.load(open(sys.argv[1]))
offers.sort(key=lambda o:o.get("dph_total",1e9))
def num(v):
    try: return float(v)
    except Exception: return 0.0
for i,o in enumerate(offers[:20],1):
    name=o.get("gpu_name","?")
    vram=num(o.get("gpu_total_ram",0))
    vram_s=f"{vram/1024:.0f}G" if vram else "?"
    dph=num(o.get("dph_total",0))
    dlperf=num(o.get("dlperf",0))
    rel=num(o.get("reliability2") or o.get("reliability") or 0)
    cpu=num(o.get("cpu_cores_effective") or o.get("cpu_cores") or 0)
    ram=num(o.get("cpu_ram",0))
    up=num(o.get("inet_up",0))
    down=num(o.get("inet_down",0))
    geo=(o.get("geolocation") or "?")
    print(f"  {i:>2}  {name:<18} {vram_s:>6} {dph:>7.3f} {dlperf:>5.1f} {rel*100:>3.0f}% {int(cpu):>5} {int(ram):>4}G {int(up):>4}/{int(down):<6} {geo[:22]:<22} {o.get('id')}")
PY
    echo "  (VRAM GB | dlpf=DLPerf TFLOPS | rel=reliability | CPUc=effective cores | RAM GB | up/down Mbps | geo)"

    # ---- 3. choose ----
    IDX=1
    if [ "${AUTO:-0}" != "1" ]; then
      shown=$([ "$COUNT" -gt 20 ] && echo 20 || echo "$COUNT")
      printf "\n  Choose an offer [1-%s, Enter=cheapest, q=quit]: " "$shown"
      read -r choice
      if [ "${choice:-}" = "q" ]; then echo "aborted."; rm -f "$tmp"; exit 0; fi
      [ -n "${choice:-}" ] && IDX="$choice"
    fi
    OFFER_ID="$($PY -c 'import json,sys
o=sorted(json.load(open(sys.argv[1])),key=lambda x:x.get("dph_total",1e9))
i=int(sys.argv[2])-1
print(o[i]["id"] if 0<=i<len(o) else "")' "$tmp" "$IDX")"
    rm -f "$tmp"
    [ -n "$OFFER_ID" ] || { echo "!! invalid choice"; exit 1; }
    echo ">> renting offer $OFFER_ID"

    # ---- 4a. ensure our SSH key is registered on the Vast account ----
    # Vast sshd trusts ACCOUNT ssh keys (not the AUTHORIZED_KEY env). So register
    # our pubkey via the API before renting, unless VAST_SSH_KEY=skip.
    if [ "${VAST_SSH_KEY:-auto}" != "skip" ]; then
      # pubkey to register: explicit VAST_SSH_KEY path, else our generated key
      if [ -n "${VAST_SSH_KEY:-}" ] && [ "${VAST_SSH_KEY}" != "auto" ] && [ -f "${VAST_SSH_KEY/#\~/$HOME}" ]; then
        REG_PUB="$(cat "${VAST_SSH_KEY/#\~/$HOME}")"
        case "$REG_PUB" in *.pub) ;; *) REG_PUB="$(cat "${VAST_SSH_KEY/#\~/$HOME}.pub" 2>/dev/null || echo "$REG_PUB")";; esac
      else
        REG_PUB="$(cat "${SSH_KEY/#\~/$HOME}.pub")"
      fi
      REG_PUB="$(echo "$REG_PUB" | head -n1 | tr -d '\r')"
      echo ">> registering SSH key on Vast account (once; safe to repeat)..."
      # Vast API: POST /ssh/ with the pubkey. Tolerate "already exists".
      resp="$(curl -s -X POST "https://console.vast.ai/api/v0/ssh/" \
        -H "Authorization: Bearer ${VAST_API_KEY}" \
        -H "Content-Type: application/json" \
        --data "$($PY -c 'import json,sys;print(json.dumps({"ssh_key":sys.argv[1]}))' "$REG_PUB")" 2>/dev/null || true)"
      if echo "$resp" | grep -qiE 'created|success|already|exists'; then
        echo ">> key registered (or already present)"
      else
        echo ">> note: key registration response: $(echo "$resp" | head -c 200)"
        echo "   (continuing; if SSH later fails, add key manually at vast.ai → Account → SSH Keys)"
      fi
    fi

    # env for the container: install ssh public key (our image entrypoint also writes this)
    PUBKEY="$(cat "${SSH_KEY/#\~/$HOME}.pub")"
    ENV_JSON="$($PY -c 'import json,sys;print(json.dumps({"-e AUTHORIZED_KEY":sys.argv[1]}))' "$PUBKEY")"

    RAW="$(vastai create instance "$OFFER_ID" \
        --image "$IMAGE" \
        --disk "$DISK_GB" \
        --ssh --direct \
        --env "$ENV_JSON" --raw)"
    INST_ID="$(echo "$RAW" | $PY -c 'import sys,json
try: d=json.load(sys.stdin)
except Exception: d={}
print(d.get("new_contract") or d.get("id") or "")')"

    echo ">> instance $INST_ID created; waiting for start..."
    for i in $(seq 1 60); do
      INFO="$(vastai show instance "$INST_ID" --raw 2>/dev/null || echo '{}')"
      STATUS="$(echo "$INFO" | $PY -c 'import sys,json
try: d=json.load(sys.stdin)
except Exception: d={}
print(d.get("actual_status",""))' 2>/dev/null || echo "")"
      HOST="$(echo "$INFO" | $PY -c 'import sys,json
try: d=json.load(sys.stdin)
except Exception: d={}
print(d.get("public_ipaddr",""))' 2>/dev/null || echo "")"
      PORT="$(echo "$INFO" | $PY -c 'import sys,json
try: d=json.load(sys.stdin)
except Exception: d={}
p=d.get("ports",{}) or {}
v=p.get("22/tcp") or [{}]
print((v[0] or {}).get("HostPort",""))' 2>/dev/null || echo "")"
      if [ "$STATUS" = "running" ] && [ -n "$HOST" ] && [ -n "$PORT" ]; then
        echo "$INST_ID $HOST $PORT" > "$STATE_FILE"
        echo ">> running at $HOST:$PORT"
        exit 0
      fi
      # surface obvious failures early
      if [ "$STATUS" = "exited" ] || [ "$STATUS" = "error" ]; then
        echo "!! instance status=$STATUS (likely image pull failure). Logs:"
        vastai logs "$INST_ID" 2>/dev/null | tail -20 || true
        echo ">> destroying failed instance $INST_ID to stop billing"
        vastai destroy instance "$INST_ID" --raw >/dev/null 2>&1 || true
        : > "$STATE_FILE"
        exit 1
      fi
      sleep 10
    done
    echo "!! instance did not reach running state in time."
    echo ">> destroying $INST_ID to stop billing (set KEEP_ON_FAIL=1 to keep it)"
    if [ "${KEEP_ON_FAIL:-0}" != "1" ]; then
      vastai destroy instance "$INST_ID" --raw >/dev/null 2>&1 || true
      : > "$STATE_FILE"
    fi
    exit 1
    ;;
    destroy)
    read -r INST_ID _ < <(head -n1 "$STATE_FILE" 2>/dev/null) || true
    [ -n "${INST_ID:-}" ] && vastai destroy instance "$INST_ID" --raw || true
    ;;
  *) echo "usage: vast.sh create|destroy  (AUTO=1 to skip picker)"; exit 1;;
esac
