#!/usr/bin/env bash
# Normalize all tracked text files to LF line endings (fix CRLF from Windows).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  list="$(git ls-files)"
else
  list="$(find . -type f -not -path './.git/*')"
fi
echo "$list" | while IFS= read -r f; do
  [ -f "$f" ] || continue
  sed -i 's/\r$//' "$f"
done
echo ">> converted line endings to LF"
