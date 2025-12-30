#!/usr/bin/env bash
# tmux-session-manager.tmux
#
# TPM plugin run file:
# - Binds a default key to launch the tmux-session-manager UI from within tmux.
# - Reads optional tmux options to customize the key and binary/config paths.
#
# Options (set in your ~/.tmux.conf if desired):
#   set -g @tmux_session_manager_key 'S'                      # default: 'S' (prefix + S)
#   set -g @tmux_session_manager_bin '~/.tmux/plugins/tmux-session-manager/bin/tmux-session-manager'
#   set -g @tmux_session_manager_config '~/.config/tmux-session-manager/config.yaml'
#   set -g @tmux_session_manager_launch_mode 'window'         # window | popup
#
# This file is executed by TPM when the plugin is sourced.
# It binds the chosen key to run the launcher script:
#   scripts/tmux_session_manager.tmux

set -euo pipefail

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="${CURRENT_DIR}"

# Read key binding from tmux option; default to 'S' (prefix + S)
KEY_BIND="$(tmux show -gqv @tmux_session_manager_key || true)"
if [[ -z "${KEY_BIND}" ]]; then
  KEY_BIND="S"
fi

LAUNCHER="${REPO_ROOT}/scripts/tmux_session_manager.tmux"

# Friendly guidance if launcher is missing
if [[ ! -f "${LAUNCHER}" ]]; then
  tmux display-message -d 5000 "tmux-session-manager: launcher script not found at ${LAUNCHER}"
  tmux display-message -d 5000 "Ensure plugin files are installed correctly."
  exit 0
fi

# Bind the key in the default (prefix) table:
# Users will press: prefix + ${KEY_BIND}
tmux bind-key "${KEY_BIND}" run-shell "\"${LAUNCHER}\""

# Optional: brief status message (non-intrusive)
tmux display-message -d 2000 "tmux-session-manager: bound prefix + ${KEY_BIND} to session manager"
