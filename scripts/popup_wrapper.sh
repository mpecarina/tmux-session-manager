#!/usr/bin/env bash
# Robust tmux popup wrapper for tmux-session-manager.
#
# Goals:
# - Preserve a real TTY for interactive TUIs inside tmux popups.
# - Avoid "blank" popups by showing actionable errors on failure.
# - Capture minimal metadata to a log file for post-mortem debugging.
# - Keep popup open on failure so errors are visible.
# - Be silent on success (so the TUI fully owns the screen).
#
# Usage:
#   popup_wrapper.sh --cmd "<command string>"
# or:
#   popup_wrapper.sh "<command string>"
#
# Environment:
#   TMUX_SESSION_MANAGER_LOG               Override metadata log path
#                                         (default: ~/.config/tmux-session-manager/popup.log)
#   TMUX_SESSION_MANAGER_TITLE             Optional title shown in header (only when not silent)
#   TMUX_SESSION_MANAGER_SILENT_ON_SUCCESS If 1 (default), do not print wrapper header
#   TMUX_SESSION_MANAGER_WRAPPER_TRACE     If "0", disables set -x tracing (default: enabled)
#
# Notes:
# - Do NOT pipe to `tee` for full-screen TUIs. Pipes can make stdout/stderr non-TTY,
#   which can break rendering or cause hangs. This wrapper uses /dev/tty.
# - This wrapper may log the command string. Do not include secrets in the command line.

set -euo pipefail

if [[ "${TMUX_SESSION_MANAGER_WRAPPER_TRACE-1}" != "0" ]]; then
  set -x
fi

# ---------- args ----------
cmd_str=""
if [[ "${1-}" == "--cmd" ]]; then
  shift
  cmd_str="${1-}"
else
  cmd_str="${1-}"
fi

if [[ -z "${cmd_str}" ]]; then
  echo "tmux-session-manager popup wrapper: missing command string"
  echo "Usage: popup_wrapper.sh --cmd \"<command>\""
  exit 2
fi

# ---------- config ----------
silent_on_success="${TMUX_SESSION_MANAGER_SILENT_ON_SUCCESS-1}"
title="${TMUX_SESSION_MANAGER_TITLE-tmux-session-manager}"
log_path="${TMUX_SESSION_MANAGER_LOG-}"

# Resolve default metadata log path under ~/.config
if [[ -z "${log_path}" ]]; then
  home="${HOME-}"
  if [[ -z "${home}" ]]; then
    home="/tmp"
  fi
  log_dir="${home}/.config/tmux-session-manager"
  log_path="${log_dir}/popup.log"
else
  log_dir="$(dirname "${log_path}")"
fi

mkdir -p "${log_dir}" 2>/dev/null || true
: >> "${log_path}" 2>/dev/null || true

# ---------- helpers ----------
ts() { date +"%Y-%m-%dT%H:%M:%S%z"; }

hr() {
  printf '%*s\n' "${1:-80}" '' | tr ' ' '-'
}

print_header() {
  local cols="${COLUMNS:-80}"
  hr "${cols}"
  echo "${title} (popup)"
  echo "Time : $(ts)"
  echo "TMUX : ${TMUX-}"
  echo "TERM : ${TERM-}"
  echo "PWD  : $(pwd)"
  echo "SHELL: ${SHELL-}"
  echo "PATH : ${PATH-}"
  echo "Log  : ${log_path}"
  echo "Cmd  : ${cmd_str}"
  hr "${cols}"
}

keep_open_prompt() {
  echo
  echo "Press Enter to close this popup."
  if [[ -r /dev/tty ]]; then
    # shellcheck disable=SC2162
    read _ < /dev/tty || true
  else
    # shellcheck disable=SC2162
    read _ || true
  fi
}

# ---------- main ----------
# In silent mode, clear before launching so wrapper text doesn't linger.
if [[ "${silent_on_success}" != "0" ]]; then
  printf "\033[2J\033[H"
else
  print_header
fi

# Log minimal metadata (do not use pipe/tee)
{
  echo
  echo "===== $(ts) ====="
  echo "cmd=${cmd_str}"
  echo "pwd=$(pwd)"
  echo "term=${TERM-}"
  echo "tmux=${TMUX-}"
  echo "path=${PATH-}"
} >> "${log_path}" 2>/dev/null || true

# IMPORTANT: keep stdio as a TTY for TUIs.
tty_in="/dev/tty"
tty_out="/dev/tty"

set +e
if [[ -r "${tty_in}" && -w "${tty_out}" ]]; then
  bash -lc -- "${cmd_str}" <"${tty_in}" >"${tty_out}" 2>&1
  exit_code=$?
else
  # Fallback: inherited stdio (may be less reliable if not a TTY).
  bash -lc -- "${cmd_str}"
  exit_code=$?
fi
set -e

if [[ "${exit_code}" -ne 0 ]]; then
  echo
  echo "Command exited with code ${exit_code}."
  echo "See log: ${log_path}"
  keep_open_prompt
  exit "${exit_code}"
fi

exit 0
