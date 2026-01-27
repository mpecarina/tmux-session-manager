#!/usr/bin/env bash
set -euo pipefail

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="${CURRENT_DIR}"

KEY_BIND="$(tmux show -gqv @tmux_session_manager_key || true)"
if [[ -z "${KEY_BIND}" ]]; then
  KEY_BIND="S"
fi

LAUNCHER="${REPO_ROOT}/scripts/tmux_session_manager.tmux"
if [[ ! -f "${LAUNCHER}" ]]; then
  tmux display-message -d 5000 "tmux-session-manager: launcher script not found at ${LAUNCHER}"
  exit 0
fi

tmux bind-key "${KEY_BIND}" run-shell "\"${LAUNCHER}\""
tmux display-message -d 2000 "tmux-session-manager: bound prefix + ${KEY_BIND} to tmux-session-manager"
