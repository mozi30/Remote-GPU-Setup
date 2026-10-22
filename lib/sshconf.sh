#!/usr/bin/env bash
# lib/sshconf.sh — helpers for writing ~/.ssh/config entries that work when
# VS Code (Windows OpenSSH) consumes a config written from Git-Bash / WSL.
#
# WHY THIS EXISTS
#   The scripts may run under Git-Bash (HOME=/c/Users/you) or WSL
#   (HOME=/home/you, Windows drive at /mnt/c). VS Code on Windows uses
#   C:\Windows\System32\OpenSSH\ssh.exe, which CANNOT read POSIX paths like
#   "/c/Users/you/.ssh/key". If we write those verbatim, VS Code fails with
#   "no such identity: /c/Users/..." -> Permission denied (publickey) ->
#   the remote folder appears not to exist.
#
#   So: resolve a Windows-style path (C:/Users/you/.ssh/key) for the config
#   that Windows ssh.exe reads, and keep a POSIX path for our own ssh calls.

# Print the Windows config path that VS Code reads.
# Prefers $WIN_SSH_CONFIG from .env, else derives it from USERPROFILE or the
# WSL mount. Returns empty when this looks like a plain Linux/macOS machine.
sshconf_win_config_path() {
  if [ -n "${WIN_SSH_CONFIG:-}" ]; then
    printf '%s\n' "$WIN_SSH_CONFIG"; return 0
  fi

  # Git-Bash / MSYS / PowerShell host: USERPROFILE is set.
  if [ -n "${USERPROFILE:-}" ]; then
    printf '%s\n' "$(winpath "$USERPROFILE")/.ssh/config"; return 0
  fi

  # WSL: the Windows profiles live under /mnt/c/Users. The WSL $USER often
  # does NOT match the Windows account name, so pick the profile that has .ssh.
  if [ -d /mnt/c/Users ]; then
    local d name
    # exact match first
    if [ -n "${WIN_USER:-}" ] && [ -d "/mnt/c/Users/${WIN_USER}" ]; then
      printf 'C:/Users/%s/.ssh/config\n' "$WIN_USER"; return 0
    fi
    if [ -n "${USER:-}" ] && [ -d "/mnt/c/Users/${USER}" ]; then
      printf 'C:/Users/%s/.ssh/config\n' "$USER"; return 0
    fi
    # else: first profile that already has an .ssh directory with a config
    for d in /mnt/c/Users/*; do
      [ -d "$d" ] || continue
      name="$(basename "$d")"
      case "$name" in
        Public|Default|"All Users"|Default\ User|defaultuser0) continue ;;
      esac
      if [ -f "$d/.ssh/config" ]; then
        printf 'C:/Users/%s/.ssh/config\n' "$name"; return 0
      fi
    done
  fi

  # plain Linux / macOS: the normal unix config is what ssh reads
  printf '%s\n' "$HOME/.ssh/config"
}

# Convert a POSIX path to a Windows path (C:/a/b). Pass through if already Windows-ish.
winpath() {
  local p="$1"
  case "$p" in
    [A-Za-z]:[\\/]*) printf '%s\n' "$(printf '%s' "$p" | sed 's|\\|/|g')"; return ;;
  esac
  # Git-Bash style: /c/Users/... -> C:/Users/...
  case "$p" in
    /?/*) printf '%s\n' "$(printf '%s' "$p" | sed -E 's|^/([A-Za-z])/|\U\1:/|')"; return ;;
  esac
  # WSL style: /mnt/c/Users/... -> C:/Users/...
  case "$p" in
    /mnt/?/*) printf '%s\n' "$(printf '%s' "$p" | sed -E 's|^/mnt/([A-Za-z])/|\U\1:/|')"; return ;;
  esac
  printf '%s\n' "$p"
}

# True if the Windows config file is the one we should write (i.e. not $HOME/.ssh/config).
sshconf_is_windows() {
  local cfg; cfg="$(sshconf_win_config_path)"
  [ -n "$cfg" ] && [ "$cfg" != "$HOME/.ssh/config" ]
}

# sshconf_write <config_path> <alias> <host> <port> <identityfile-windows-or-posix>
# Replaces any existing block for <alias>, appends a fresh one.
sshconf_write() {
  local conf="$1" alias="$2" host="$3" port="$4" identity="$5"
  local dir; dir="$(dirname "$conf")"
  mkdir -p "$dir"

  if [ -f "$conf" ]; then
    # drop old block for this alias
    awk -v host="$alias" '
      $1=="Host" && $2==host {skip=1; next}
      /^Host[ \t]/ {skip=0}
      !skip {print}
    ' "$conf" > "${conf}.tmp.$$" && mv "${conf}.tmp.$$" "$conf"
  fi

  # POSIX ssh: /dev/null. Windows ssh.exe has no /dev/null, and "NUL" is wrong
  # too: Git-Bash's ssh treats it as a relative FILENAME, so every ssh call
  # from a project directory leaves a junk file named "NUL" behind. Point at a
  # real known_hosts next to the identity file instead — both Windows ssh.exe
  # and Git-Bash ssh understand "C:/Users/you/.ssh/known_hosts".
  local knownhosts="/dev/null"
  case "$identity" in
    [A-Za-z]:[\\/]*)
      # normalize C:\a\b -> C:/a/b so the known_hosts path stays consistent
      local idir; idir="$(dirname "$identity")"; idir="${idir//\\//}"
      [ "$idir" = "." ] || knownhosts="${idir}/known_hosts"
      ;;
  esac

  {
    printf '\nHost %s\n' "$alias"
    printf '    HostName %s\n' "$host"
    printf '    Port %s\n' "$port"
    printf '    User root\n'
    printf '    IdentityFile %s\n' "$identity"
    printf '    IdentitiesOnly yes\n'
    printf '    StrictHostKeyChecking no\n'
    printf '    UserKnownHostsFile %s\n' "$knownhosts"
    printf '    ServerAliveInterval 30\n'
  } >> "$conf"
}
