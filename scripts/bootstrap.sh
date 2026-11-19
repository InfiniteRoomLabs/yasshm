#!/usr/bin/env bash
# Install the pinned toolchain on a machine that has only bash, curl and git:
# a fresh cloud sandbox, a new laptop, a CI runner without mise-action.
# Idempotent: run it at the start of every session, it is a no-op when current.
#
# Plain bash on purpose. The other scripts use the `usage` shebang, and `usage`
# is itself installed by mise, so this one cannot depend on it.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# The mise installer puts the binary here and does not edit shell profiles.
export PATH="$HOME/.local/bin:$PATH"

if ! command -v mise >/dev/null 2>&1; then
  echo "bootstrap: mise not found, installing to $HOME/.local/bin"
  curl -fsSL https://mise.run | sh
fi

cd "$repo_root"
mise trust --quiet
mise install

echo "bootstrap: toolchain ready ($(mise --version | head -1))"
mise ls --current

# The real-sshd test lane needs an sshd binary; its absence is reported, never fatal.
if [ -n "$(command -v sshd || ls /usr/sbin/sshd 2>/dev/null)" ]; then
  echo "bootstrap: sshd found (the real-sshd lane can run locally)"
else
  echo "bootstrap: sshd not found; the real-sshd lane runs in CI only"
fi

if [ "$(command -v mise)" = "$HOME/.local/bin/mise" ]; then
  echo "bootstrap: mise is at $HOME/.local/bin/mise -- call it by that path if your shell does not have that directory on PATH"
fi
