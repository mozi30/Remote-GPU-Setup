
# fast-remote-gpu




Rent a cheap NVIDIA GPU (Vast.ai primary, RunPod fallback) and get a ready-to-train
environment over **VS Code Remote-SSH** in a couple of minutes.

Your local machine (even an AMD card) is just the editor. All compute runs on the
rented box. One command up, one command down.

```bash
git clone <your-repo-url> fast-remote-gpu
cd fast-remote-gpu
cp env.example.txt .env       # add your API key(s)
./up.sh                       # rents cheapest matching GPU, waits for SSH, opens VS Code
./connect.sh                  # re-attach to a running box later (no new rental)
./down.sh                     # destroys the instance, stops billing
```

---

## How it works

```
 local (AMD box)                     rented NVIDIA box
 ┌───────────────────┐   up.sh      ┌──────────────────────────┐
 │ VS Code           │ ───────────▶ │ container image (GHCR)   │
 │  Remote-SSH ──────┼── ssh ─────▶ │  sshd + CUDA + PyTorch   │
 │  (edits code)     │              │  uv + /workspace (volume)│
 └───────────────────┘   down.sh    └──────────────────────────┘
```

- `up.sh` asks the backend (Vast or RunPod) for the **cheapest offer** matching your
  GPU allow-list + price cap + mode, starts your image, waits for SSH, and writes an
  entry to `~/.ssh/config` as host **`fast-remote-gpu`**.
- You connect via **VS Code Remote-SSH** (host `fast-remote-gpu`) or `./ssh.sh`.
- `/workspace` lives on a **persistent volume** so code, datasets, HF cache and
  checkpoints survive stop/start (and spot preemption).
- `down.sh` destroys the instance so billing stops.

---

## Prerequisites

- A **Vast.ai** account and/or **RunPod** account + API key.
- A **GitHub** account (for the free GHCR image registry).
- **bash** — use WSL or Git-Bash on Windows.
- `docker` locally only if you build the image yourself (otherwise GitHub Actions does it).
- VS Code + the **Remote - SSH** extension.

---

## 1. Set up the image (once)

The training image is built and pushed to **GHCR automatically** by GitHub Actions
on every push to `main` (`.github/workflows/build.yml`).

1. Push this repo to GitHub.
2. Actions → "Build & Push Image" → image appears at
   `ghcr.io/<your-github-username>/fast-remote-gpu:latest`.
3. Make it **public** (or set registry creds): GitHub → profile → Packages →
   `fast-remote-gpu` → Package settings → Change visibility → Public.

> Private image? set `GHCR_USER` + `GHCR_TOKEN` (token needs `read:packages`) in `.env`.

Manual build/push alternative:

```bash
echo "$GH_TOKEN" | docker login ghcr.io -u <your-github-username> --password-stdin
docker build -t ghcr.io/<your-github-username>/fast-remote-gpu:latest ./image
docker push ghcr.io/<your-github-username>/fast-remote-gpu:latest
```

The base is `pytorch/pytorch:*-cuda12.8*` so **Blackwell (RTX 50-series)** works.
Older cu12x builds will NOT run on 50-series cards.

---

## 2. Configure `.env`

```bash
cp .env.example .env
```

| Var | Meaning |
|---|---|
| `PROVIDER` | `vast` or `runpod` (Vast primary by default) |
| `VAST_API_KEY` / `RUNPOD_API_KEY` | provider API keys |
| `IMAGE` | `ghcr.io/<you>/fast-remote-gpu:latest` |
| `MODE` | `interruptible` (cheap, preemptible) or `on_demand` (stable) |
| `GPU_NAMES` | comma-separated allow-list, cheapest match wins |
| `MAX_PRICE` | max $/hr you'll pay |
| `DISK_GB` | workspace volume size |
| `SSH_KEY` | private key path (auto-generated on first run if missing) |
| `VAST_SSH_KEY` | `auto` = auto-register your key on your Vast account each run (default); `skip` = don't; or a key path |
| `USE_VENV` | `0` = use image's system python (default, shortest boot); `1` = also make `/workspace/.venv` (reuses image torch) |
| `OPEN_VSCODE` | `1` = auto-open VS Code on the remote workspace when up.sh finishes |

### GPU allow-list default

```
RTX 5060 Ti,RTX 5070,RTX 5070 Ti,RTX 5080,RTX 5090,RTX 4090,RTX 4080 SUPER,RTX 4080,RTX 3090
```

Cheapest available match wins. Cheap cards (5060 Ti/5070/5080) are mostly on
**Vast**, often **interruptible** — great for training if you checkpoint.

---

## 3. Daily use

```bash
./up.sh                      # rent + wait for SSH + auto-open VS Code on /workspace
./connect.sh                 # re-attach to a RUNNING box (no rental) + open VS Code
./ssh.sh                     # shell + forwards tensorboard/jupyter ports
./sync.sh ./my-project       # rsync local project -> /workspace/my-project
./down.sh                    # destroy instance (stops billing)
```

- `up.sh` rents, sets up, and **opens a new VS Code window** on `fast-remote-gpu:/workspace`
  (disable with `OPEN_VSCODE=0` in `.env`).
- `connect.sh` is for **resuming**: it reads `.instance`, (on Vast) checks the real
  status and can **start a stopped instance**, re-writes `~/.ssh/config`, and reopens
  VS Code — without renting anything new.
- Connect manually any time from VS Code: **Remote-SSH → fast-remote-gpu → /workspace**.

### Package management with `uv` (available on the remote)

The full stack is baked into the image, but `uv` is installed system-wide so you can
add things fast without rebuilding:

```bash
uv pip install <package>          # install into the active env
uv pip install -r requirements.txt
uv venv                           # create a fresh venv in a project
uv add <package>                  # manage a project's pyproject.toml
uvx <tool>                        # run a one-off tool in an ephemeral env
```

### Tensorboard / wandb

`ssh.sh` forwards local ports:
- `6006` → tensorboard (`tensorboard --logdir /workspace/runs`)
- `8888` → jupyter (`jupyter lab --ip=0.0.0.0`)

Then open http://localhost:6006 or http://localhost:8888 locally.

---

## Interruptible vs on-demand

`MODE=interruptible` is ~50% cheaper but the box can be taken back at any time.
Training is fine **if you checkpoint** to `/workspace` regularly and can resume.
Switch to `MODE=on_demand` for long interactive / debugging sessions.

After a preemption: `./down.sh` then `./up.sh` and resume from `/workspace/checkpoints`.

---

## Included libraries

Full ML / CV / visualization stack (edit `requirements.txt`):

- **Core ML:** torch, torchvision, transformers, datasets, accelerate, peft, trl,
  bitsandbytes, safetensors, tokenizers, huggingface_hub
- **CV:** opencv-python, timm, ultralytics, albumentations, pillow, scikit-image
- **Data/viz:** numpy, pandas, polars, scikit-learn, scipy, matplotlib, seaborn,
  plotly, tensorboard, wandb, tqdm
- **GenAI/optional:** diffusers
- **Tooling:** uv, git, tmux, htop, rsync, nano, jupyterlab

---

## Repo layout

```
fast-remote-gpu/
├─ env.example.txt          # config template (copy -> .env)
├─ up.sh  connect.sh  down.sh  ssh.sh
├─ backends/vast.sh  backends/runpod.sh
├─ image/Dockerfile  image/entrypoint.sh
├─ scripts/sync.sh  scripts/startup.sh  scripts/uv-venv.sh
├─ requirements.txt
├─ .github/workflows/build.yml
└─ README.md
```

> Note: on Vast the container's `ENTRYPOINT` is replaced by Vast's own `/.launch`
> bootstrap, so the image's entrypoint (venv/workspace setup) may not run. That is
> why `/workspace` is pre-created in the image itself and setup also happens in
> `up.sh` after SSH. Boot then uses the image's **system python** (driver-matched
> torch), with no install step.

---

## Troubleshooting

- **"no CUDA-capable device" / unsupported GPU** on 50-series → image must be cu12.8+.
  Rebuild from the provided Dockerfile.
- **No offers found** → loosen `GPU_NAMES` or raise `MAX_PRICE`; Vast stock fluctuates.
  Try `PROVIDER=runpod`.
- **SSH hangs** → instance may still be pulling the image on first run (minutes).
  Re-run `./ssh.sh`.
- **Preempted mid-run** → resume from `/workspace/checkpoints` after `./up.sh`.
