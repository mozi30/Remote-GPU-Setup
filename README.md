# gpu-backpack

Rent a cheap NVIDIA GPU on **Vast.ai** (RunPod fallback), work on it from **VS Code
Remote-SSH**, and keep your workspace in a **Cloudflare R2** bucket so destroying the
instance never loses your files.

One Windows executable, no Git-Bash / Python / `vastai` CLI needed:

```
1  Start GPU & Pull Workspace       (or "Reconnect" when a GPU is running)
2  Force Backup to R2 Now
3  Backup & Destroy GPU
4  R2 Files (upload / download / restore from trash)
5  Settings (API keys, GPU wishlist, min internet speed)
```

---

## How it works

```
 PC (gpu-backpack.exe)                  rented GPU box                    Cloudflare R2
 ┌──────────────────────┐  Vast API    ┌───────────────────────────┐    ┌──────────────────┐
 │ menu, settings, keys │ ───────────▶ │ image: CUDA 12.8 torch, uv│    │ <prefix>/        │
 │ state.json (crash    │  ssh.exe     │ /opt/backpack/*.sh        │◀──▶│   workspace/     │
 │   recovery)          │ ───────────▶ │ /workspace  ── autosave ──┼───▶│   trash/<time>/  │
 │ VS Code Remote-SSH   │ ───────────▶ │   (every 12 min, on box)  │    │   lock.json      │
 └──────────────────────┘              └───────────────────────────┘    └──────────────────┘
```

**Start**
1. Searches Vast for the cheapest offer in your GPU wishlist that also meets the
   minimums: download ≥ 500 Mbps, upload ≥ 500 Mbps, reliability ≥ 0.98,
   ≥ 1 direct port (direct SSH, no proxy), driver ≥ CUDA 12.8. All configurable.
2. Rents it, labels it `gpu-backpack`, and **saves the instance ID locally right away**.
3. Waits for SSH, installs the sync scripts to `/opt/backpack` (outside `/workspace`).
4. **Pulls** `R2:<prefix>/workspace` → `/workspace` with rclone.
5. Rebuilds Python environments with `uv` (envs are never synced, see below).
6. Starts the **autosave loop on the box** — it keeps running if your PC sleeps or the
   tool is closed.
7. Opens VS Code on `gpu-backpack:/workspace`.

**Backup & Destroy** — stops autosave → final push → `rclone check` verifies every file
is in R2 → only then destroys the instance. If anything fails the GPU is **not**
destroyed; you choose: retry, download `/workspace` to your PC over SSH, keep it
running, or destroy anyway (requires typing `DESTROY`).

**Crash recovery** — reopening the exe finds the tracked instance and offers
Reconnect / Backup & Destroy. A stopped (outbid) interruptible instance can be
restarted first. Labelled instances the PC lost track of are offered for adoption.

### Safety rules built in

| Risk | Protection |
|---|---|
| Pull from R2 fails, empty `/workspace` gets pushed and wipes R2 | Pushing is refused on a box until a pull completed there (`/opt/backpack/hydrated`) |
| Sync deletes or overwrites files in R2 | Old versions move to `trash/<timestamp>/` (restorable from menu 4); kept 14 days |
| Accidental mass deletion | Max 500 deletions per sync; larger needs your confirmation |
| Final backup fails | Instance is kept; alert + choices; never auto-destroyed |
| Two GPUs writing the same workspace | `lock.json` in R2; warns before a second session starts |
| Tool closed / PC rebooted | `state.json` written at rent time; recovery on next launch |

---

## Setup

### 1. Cloudflare R2
1. Cloudflare dashboard → R2 → **Create bucket** (e.g. `gpu-workspace`).
2. R2 → **Manage API tokens** → Create token → *Object Read & Write*,
   **limited to that one bucket**. Note the Access Key ID, Secret, and your Account ID.
   (The token is copied to the rented box for rclone, so keep it bucket-scoped.)

### 2. The image (once)
GitHub Actions builds `image/` to `ghcr.io/<you>/fast-remote-gpu:latest` on push
(`.github/workflows/build.yml`). Make the package **public** (GitHub → Packages →
Package settings). The image now also contains rclone.

### 3. Get the exe
- Download `gpu-backpack.exe` from the Actions run / GitHub release
  (`.github/workflows/exe.yml`), or build it:
  ```bash
  go build -trimpath -ldflags "-s -w" -o dist/gpu-backpack.exe ./cmd/backpack
  ```
- Run it. On first launch it imports an old `.env` from the current folder if present,
  then shows the settings form (Vast key, R2 details, wishlist, minimum speeds…).
- Settings → *Test connections* shows how many Vast offers match your filters.

Requires Windows OpenSSH (built into Windows 10/11) and VS Code with the
**Remote - SSH** extension and the `code` command on PATH.

---

## What gets synced

Everything in `/workspace` (code, `checkpoints/`, `runs/`, …) **except** these default
excludes (rclone patterns, editable in Settings):

```
.venv/**   .cache/**   __pycache__/**   node_modules/**   .ipynb_checkpoints/**   /data/**
```

`x/**` = a folder named x anywhere; `/x/**` = only the top-level `/workspace/x`.
Put datasets in `/workspace/data` and re-download them per session.

## Python environments

Rebuilt on every new box by `envsetup.sh`, per project folder (up to 3 levels deep):

| Project has | What happens |
|---|---|
| `uv.lock` | `uv sync` — isolated env, exact lockfile, installs its own torch (needs the cu128 index in `pyproject.toml` for RTX 50-series) |
| only `requirements.txt` | `.venv` with `--system-site-packages` on top of the image's CUDA 12.8 torch; torch lines are skipped. Seconds. |
| only `pyproject.toml` | `uv sync` (creates `uv.lock`) |

## VS Code extensions

The tool sets `remote.SSH.defaultExtensions` in your local VS Code settings
(backup: `settings.json.gpu-backpack.bak`), so VS Code installs your list on every new
GPU box automatically. Edit the list in Settings.

## R2 Files menu

- **Upload PC folder** → straight into R2 when no GPU runs; into the box's
  `/workspace` (then backed up) when one does — otherwise the next autosave would
  move R2-only files to trash.
- **Download** the whole workspace or one folder to your PC.
- **Restore from trash** — pick a snapshot, restore all or selected files. The
  version being replaced goes to trash itself, so restores are undoable.

## Files on your PC

| | |
|---|---|
| `%APPDATA%\gpu-backpack\config.toml` | settings |
| `%APPDATA%\gpu-backpack\state.json` | active instance (crash recovery) |
| `%APPDATA%\gpu-backpack\backpack.log` | full log incl. remote output |
| Windows Credential Manager `gpu-backpack` | Vast / RunPod keys, R2 secret |
| `~\.ssh\config` | managed `Host gpu-backpack` block |

## Repo layout

```
cmd/backpack/            exe entry point
internal/app/            menu, start/reconnect, backup, teardown, files, settings
internal/provider/       Vast REST API (primary), RunPod REST API (fallback)
internal/remote/         ssh.exe wrapper, ssh config, scripts/ (run on the box)
internal/r2/             R2 from the PC: lock, upload/download, trash
internal/filter/         exclude matching on the PC side
internal/vscode/         settings.json patch, launch
image/                   GPU image (CUDA 12.8 torch, uv, rclone, sshd)
up.sh, connect.sh, …     legacy bash workflow (no R2 sync), superseded by the exe
```

## Notes & limits

- **RunPod** has no offer search, so speed/reliability minimums are not applied there.
- **Interruptible mode** on Vast bids 15% above the current minimum bid, capped at your
  max price. When outbid the instance stops; autosave limits loss to the last interval.
- Single-file R2 uploads from the PC larger than ~16 MB are multipart; rclone `check`
  then compares sizes only for those files.

## Troubleshooting

- **No offers** → Settings: raise max price, lower min speeds, add GPUs. *Test
  connections* shows the match count.
- **SSH never comes up** → first boot may still be pulling the image. `ssh -v gpu-backpack`.
- **"refusing to push"** → the box never finished pulling from R2; use Reconnect.
- **Backup fails with the delete cap** → you deleted many files; confirm the one-time
  override (deleted files remain in trash).
- Details of every remote command are in `backpack.log`.
