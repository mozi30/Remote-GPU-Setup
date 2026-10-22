# fast-remote-gpu — training image
# Base chosen for Blackwell (RTX 50-series) support: CUDA 12.8.
# Adjust the patch tag if needed; keep cu12.8.
FROM pytorch/pytorch:2.7.1-cuda12.8-cudnn9-runtime

ENV DEBIAN_FRONTEND=noninteractive \
    PYTHONUNBUFFERED=1 \
    HF_HOME=/workspace/.cache/huggingface \
    TORCH_HOME=/workspace/.cache/torch \
    PIP_CACHE_DIR=/workspace/.cache/pip \
    UV_CACHE_DIR=/workspace/.cache/uv

# System deps + sshd + tooling
RUN apt-get update && apt-get install -y --no-install-recommends \
        openssh-server git tmux htop rsync nano curl ca-certificates \
        build-essential libgl1 libglib2.0-0 && \
    rm -rf /var/lib/apt/lists/*

# uv (system-wide so it's on PATH for ssh + VS Code terminals)
RUN curl -LsSf https://astral.sh/uv/install.sh | sh && \
    mv /root/.local/bin/uv /usr/local/bin/uv && \
    mv /root/.local/bin/uvx /usr/local/bin/uvx 2>/dev/null || true && \
    uv --version

# Python libs (ML + CV + viz). Use uv for speed.
COPY requirements.txt /tmp/requirements.txt
RUN uv pip install --system --no-cache -r /tmp/requirements.txt

# Ship requirements + uv-venv helper + auto-activate hook (venv lives on /workspace)
RUN mkdir -p /opt/fast-remote-gpu && cp /tmp/requirements.txt /opt/fast-remote-gpu/requirements.txt
COPY scripts/uv-venv.sh /opt/fast-remote-gpu/uv-venv.sh
COPY image/bashrc-uv /opt/fast-remote-gpu/bashrc-uv
RUN chmod +x /opt/fast-remote-gpu/uv-venv.sh && \
    echo '. /opt/fast-remote-gpu/bashrc-uv' >> /root/.bashrc

# SSH: key-only root login
RUN mkdir -p /run/sshd /root/.ssh && chmod 700 /root/.ssh && \
    sed -i 's/#PermitRootLogin.*/PermitRootLogin prohibit-password/' /etc/ssh/sshd_config && \
    sed -i 's/#PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config

COPY image/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

WORKDIR /workspace
EXPOSE 22

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

