#!/usr/bin/env bash
# Fallback: install the stack on a provider BASE image (no custom GHCR image).
# Run this on a plain pytorch/cuda box if you don't want to use the prebuilt image.
set -euo pipefail

export HF_HOME=/workspace/.cache/huggingface
export TORCH_HOME=/workspace/.cache/torch
export PIP_CACHE_DIR=/workspace/.cache/pip
export UV_CACHE_DIR=/workspace/.cache/uv
mkdir -p "$HF_HOME" "$TORCH_HOME" "$PIP_CACHE_DIR" "$UV_CACHE_DIR" \
         /workspace/checkpoints /workspace/runs

# install uv if missing
command -v uv >/dev/null || curl -LsSf https://astral.sh/uv/install.sh | sh

echo ">> installing requirements via uv"
uv pip install --system -r requirements.txt || uv pip install --system \
  transformers datasets accelerate peft trl bitsandbytes safetensors tokenizers \
  huggingface_hub opencv-python-headless timm ultralytics albumentations pillow \
  scikit-image numpy pandas polars scikit-learn scipy matplotlib seaborn plotly \
  tensorboard wandb tqdm diffusers jupyterlab

echo ">> startup.sh done. python: $(python --version) | uv: $(uv --version)"
