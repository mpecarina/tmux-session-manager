#!/usr/bin/env bash
# tmux_session_manager.tmux
#
# Launcher for the tmux-session-manager binary.
#
# Patterns (mirrors tmux-ssh-manager):
# - Reads tmux options for binary path, config path, and launch mode
# - Launches in a new tmux window by default (native mode)
# - Optionally launches in a tmux popup (tmux >= 3.2)
# - Writes a launcher log to ~/.config/tmux-session-manager/launcher.log
#
# tmux options (set in ~/.tmux.conf):
#   set -g @tmux_session_manager_bin '~/.tmux/plugins/tmux-session-manager/bin/tmux-session-manager'
#   set -g @tmux_session_manager_config '~/.config/tmux-session-manager/config.yaml'
#   set -g @tmux_session_manager_launch_mode 'window'   # window | popup
#   set -g @tmux_session_manager_window_name 'sessions' # default: session-manager
#   set -g @tmux_session_manager_popup_w '90%'          # default: 90%
#   set -g @tmux_session_manager_popup_h '80%'          # default: 80%
#
# NOTE (2025-12): fzf picker support has been removed.
# This launcher always runs the Bubble Tea TUI for consistent behavior across terminals and tmux modes.
#
# Additional tmux options passed through to the binary as environment variables
# (to mirror our existing plugin patterns and keep the Go binary tmux-agnostic):
#   set -g @tmux_session_manager_roots '~/code,~/src,~/projects'
#   set -g @tmux_session_manager_project_depth '2'
#   set -g @tmux_session_manager_ignore_dirs '.git,node_modules,vendor,dist,build,target,.venv,__pycache__'
#   set -g @tmux_session_manager_prefer_project_spec 'on'          # on|off
#   set -g @tmux_session_manager_project_spec_names '.tmux-session.yaml,.tmux-session.yml,.tmux-session.json'
#   set -g @tmux_session_manager_default_template 'auto'           # auto|empty|node|python|go
#   set -g @tmux_session_manager_allow_shell 'off'                 # on|off (unsafe)
#   set -g @tmux_session_manager_allow_tmux_passthrough 'off'      # on|off (advanced)
#   set -g @tmux_session_manager_allowed_tmux_commands '<csv>'      # optional
#   set -g @tmux_session_manager_denied_tmux_commands '<csv>'       # optional
#   set -g @tmux_session_manager_allowed_shell_prefixes '<csv>'     # optional
#   set -g @tmux_session_manager_debug 'off'                       # on|off
#
# Environment (passed for downstream behavior):
#   TMUX_SESSION_MANAGER_IN_POPUP=1 (popup only)
#   TMUX_SESSION_MANAGER_BIN=<resolved binary path>
#
# Notes:
# - This launcher intentionally does not assume a config must exist.
# - If invoked outside tmux (or tmux server not reachable), it will execute the binary directly.

set -euo pipefail
set -x

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${CURRENT_DIR}/.." && pwd)"

LOG_DIR="${HOME}/.config/tmux-session-manager"
LOG_FILE="${LOG_DIR}/launcher.log"
mkdir -p "${LOG_DIR}" 2>/dev/null || true
: >> "${LOG_FILE}" 2>/dev/null || true

ts() { date +"%Y-%m-%dT%H:%M:%S%z"; }
log() { printf "[%s] %s\n" "$(ts)" "$*" >> "${LOG_FILE}" 2>/dev/null || true; }

log "launcher: start; cwd=$(pwd); current_dir=${CURRENT_DIR}; repo_root=${REPO_ROOT}"
log "launcher: tmux=$(tmux -V 2>/dev/null || echo 'unknown'); TMUX_env=${TMUX-}; TERM=${TERM-}; SHELL=${SHELL-}; PATH=${PATH-}"

BIN_PATH="$(tmux show -gqv @tmux_session_manager_bin || true)"
CONFIG_PATH="$(tmux show -gqv @tmux_session_manager_config || true)"
LAUNCH_MODE="$(tmux show -gqv @tmux_session_manager_launch_mode || true)"
WINDOW_NAME="$(tmux show -gqv @tmux_session_manager_window_name || true)"
POPUP_W="$(tmux show -gqv @tmux_session_manager_popup_w || true)"
POPUP_H="$(tmux show -gqv @tmux_session_manager_popup_h || true)"

# (fzf support removed)

# Pass-through options (mapped to env vars consumed by the Go binary)
ROOTS_OPT="$(tmux show -gqv @tmux_session_manager_roots || true)"
DEPTH_OPT="$(tmux show -gqv @tmux_session_manager_project_depth || true)"
IGNORE_DIRS_OPT="$(tmux show -gqv @tmux_session_manager_ignore_dirs || true)"
PREFER_SPEC_OPT="$(tmux show -gqv @tmux_session_manager_prefer_project_spec || true)"
SPEC_NAMES_OPT="$(tmux show -gqv @tmux_session_manager_project_spec_names || true)"
DEFAULT_TEMPLATE_OPT="$(tmux show -gqv @tmux_session_manager_default_template || true)"

ALLOW_SHELL_OPT="$(tmux show -gqv @tmux_session_manager_allow_shell || true)"
ALLOW_TMUX_PASSTHROUGH_OPT="$(tmux show -gqv @tmux_session_manager_allow_tmux_passthrough || true)"
ALLOWED_TMUX_COMMANDS_OPT="$(tmux show -gqv @tmux_session_manager_allowed_tmux_commands || true)"
DENIED_TMUX_COMMANDS_OPT="$(tmux show -gqv @tmux_session_manager_denied_tmux_commands || true)"
ALLOWED_SHELL_PREFIXES_OPT="$(tmux show -gqv @tmux_session_manager_allowed_shell_prefixes || true)"
DEBUG_OPT="$(tmux show -gqv @tmux_session_manager_debug || true)"

log "launcher: opt @tmux_session_manager_bin=${BIN_PATH}"
log "launcher: opt @tmux_session_manager_config=${CONFIG_PATH}"
log "launcher: opt @tmux_session_manager_launch_mode=${LAUNCH_MODE}"
log "launcher: opt @tmux_session_manager_window_name=${WINDOW_NAME}"
log "launcher: opt @tmux_session_manager_popup_w=${POPUP_W}"
log "launcher: opt @tmux_session_manager_popup_h=${POPUP_H}"

# (fzf support removed)

log "launcher: opt @tmux_session_manager_roots=${ROOTS_OPT}"
log "launcher: opt @tmux_session_manager_project_depth=${DEPTH_OPT}"
log "launcher: opt @tmux_session_manager_ignore_dirs=${IGNORE_DIRS_OPT}"
log "launcher: opt @tmux_session_manager_prefer_project_spec=${PREFER_SPEC_OPT}"
log "launcher: opt @tmux_session_manager_project_spec_names=${SPEC_NAMES_OPT}"
log "launcher: opt @tmux_session_manager_default_template=${DEFAULT_TEMPLATE_OPT}"
log "launcher: opt @tmux_session_manager_allow_shell=${ALLOW_SHELL_OPT}"
log "launcher: opt @tmux_session_manager_allow_tmux_passthrough=${ALLOW_TMUX_PASSTHROUGH_OPT}"
log "launcher: opt @tmux_session_manager_allowed_tmux_commands=${ALLOWED_TMUX_COMMANDS_OPT}"
log "launcher: opt @tmux_session_manager_denied_tmux_commands=${DENIED_TMUX_COMMANDS_OPT}"
log "launcher: opt @tmux_session_manager_allowed_shell_prefixes=${ALLOWED_SHELL_PREFIXES_OPT}"
log "launcher: opt @tmux_session_manager_debug=${DEBUG_OPT}"

# Defaults
if [[ -z "${BIN_PATH}" ]]; then
  BIN_PATH="${REPO_ROOT}/bin/tmux-session-manager"
fi
if [[ -z "${CONFIG_PATH}" ]]; then
  CONFIG_PATH="${HOME}/.config/tmux-session-manager/config.yaml"
fi
if [[ -z "${LAUNCH_MODE}" ]]; then
  LAUNCH_MODE="window"
fi
if [[ -z "${WINDOW_NAME}" ]]; then
  WINDOW_NAME="session-manager"
fi
if [[ -z "${POPUP_W}" ]]; then
  POPUP_W="90%"
fi
if [[ -z "${POPUP_H}" ]]; then
  POPUP_H="80%"
fi

# (fzf support removed)

# Expand leading "~/"
if [[ "${BIN_PATH}" == "~/"* ]]; then
  BIN_PATH="${HOME}/${BIN_PATH:2}"
fi
if [[ "${CONFIG_PATH}" == "~/"* ]]; then
  CONFIG_PATH="${HOME}/${CONFIG_PATH:2}"
fi

# Normalize launch mode
if [[ "${LAUNCH_MODE}" != "popup" ]]; then
  LAUNCH_MODE="window"
fi

# Normalize defaults for pass-through options
if [[ -z "${PREFER_SPEC_OPT}" ]]; then
  PREFER_SPEC_OPT="on"
fi
if [[ -z "${SPEC_NAMES_OPT}" ]]; then
  SPEC_NAMES_OPT=".tmux-session.yaml,.tmux-session.yml,.tmux-session.json"
fi
if [[ -z "${DEFAULT_TEMPLATE_OPT}" ]]; then
  DEFAULT_TEMPLATE_OPT="auto"
fi
if [[ -z "${DEPTH_OPT}" ]]; then
  DEPTH_OPT="2"
fi
if [[ -z "${ALLOW_SHELL_OPT}" ]]; then
  ALLOW_SHELL_OPT="off"
fi
if [[ -z "${ALLOW_TMUX_PASSTHROUGH_OPT}" ]]; then
  ALLOW_TMUX_PASSTHROUGH_OPT="off"
fi
if [[ -z "${DEBUG_OPT}" ]]; then
  DEBUG_OPT="off"
fi

log "launcher: BIN_PATH(resolved)=${BIN_PATH}"
log "launcher: CONFIG_PATH(resolved)=${CONFIG_PATH}"
log "launcher: LAUNCH_MODE(effective)=${LAUNCH_MODE}"
log "launcher: WINDOW_NAME(effective)=${WINDOW_NAME}"
log "launcher: POPUP_W/H=${POPUP_W}/${POPUP_H}"
# (fzf support removed)

if [[ ! -x "${BIN_PATH}" ]]; then
  log "launcher: ERROR binary not found or not executable: ${BIN_PATH}"
  tmux display-message -d 5000 "tmux-session-manager: binary not found or not executable at: ${BIN_PATH}" || true
  tmux display-message -d 5000 "Build it with: (cd ${REPO_ROOT} && go build -o bin/tmux-session-manager ./cmd/tmux-session-manager)" || true
  tmux display-message -d 5000 "Or set: set -g @tmux_session_manager_bin '/path/to/tmux-session-manager'" || true
  exit 1
fi

# Build command string.
# We pass config only if it exists; otherwise let the binary decide defaults.
CMD_STR="exec \"${BIN_PATH}\""
if [[ -f "${CONFIG_PATH}" ]]; then
  CMD_STR+=" --config \"${CONFIG_PATH}\""
fi
log "launcher: CMD_STR=${CMD_STR}"

# Build environment passthrough string.
# These env vars are consumed by the Go binary (so it doesn't need to query tmux options itself).
ENV_STR=""
if [[ -n "${LAUNCH_MODE}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_LAUNCH_MODE=$(printf %q "${LAUNCH_MODE}")"
fi
if [[ -n "${ROOTS_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_ROOTS=$(printf %q "${ROOTS_OPT}")"
fi
if [[ -n "${DEPTH_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_PROJECT_DEPTH=$(printf %q "${DEPTH_OPT}")"
fi
if [[ -n "${IGNORE_DIRS_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_IGNORE_DIRS=$(printf %q "${IGNORE_DIRS_OPT}")"
fi
if [[ -n "${PREFER_SPEC_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_PREFER_PROJECT_SPEC=$(printf %q "${PREFER_SPEC_OPT}")"
fi
if [[ -n "${SPEC_NAMES_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_SPEC_NAMES=$(printf %q "${SPEC_NAMES_OPT}")"
fi
if [[ -n "${DEFAULT_TEMPLATE_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_DEFAULT_TEMPLATE=$(printf %q "${DEFAULT_TEMPLATE_OPT}")"
fi
if [[ -n "${ALLOW_SHELL_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_ALLOW_SHELL=$(printf %q "${ALLOW_SHELL_OPT}")"
fi
if [[ -n "${ALLOW_TMUX_PASSTHROUGH_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_ALLOW_TMUX_PASSTHROUGH=$(printf %q "${ALLOW_TMUX_PASSTHROUGH_OPT}")"
fi
if [[ -n "${ALLOWED_TMUX_COMMANDS_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_ALLOWED_TMUX_COMMANDS=$(printf %q "${ALLOWED_TMUX_COMMANDS_OPT}")"
fi
if [[ -n "${DENIED_TMUX_COMMANDS_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_DENIED_TMUX_COMMANDS=$(printf %q "${DENIED_TMUX_COMMANDS_OPT}")"
fi
if [[ -n "${ALLOWED_SHELL_PREFIXES_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_ALLOWED_SHELL_PREFIXES=$(printf %q "${ALLOWED_SHELL_PREFIXES_OPT}")"
fi
if [[ -n "${DEBUG_OPT}" ]]; then
  ENV_STR+=" TMUX_SESSION_MANAGER_DEBUG=$(printf %q "${DEBUG_OPT}")"
fi

# ---------- fzf picker removed ----------
# This launcher always runs the Bubble Tea TUI to avoid terminal/keybinding incompatibilities.

log "launcher: ENV_STR=${ENV_STR}"

# If tmux server isn't reachable, run directly in this shell.
if ! tmux display-message -d 1 "tmux-session-manager: starting" >/dev/null 2>&1; then
  log "launcher: tmux server NOT reachable; running directly"
  echo "tmux-session-manager: executing: ${CMD_STR}"
  eval "${CMD_STR}"
  exit_code=$?
  if [[ ${exit_code} -ne 0 ]]; then
    echo "tmux-session-manager: command failed with exit code ${exit_code}"
  fi
  exit "${exit_code}"
fi
log "launcher: tmux server reachable"

# Determine popup support (tmux >= 3.2)
version_raw="$(tmux -V 2>/dev/null | awk '{print $2}')"
ver_major="${version_raw%%.*}"
ver_minor_patch="${version_raw#*.}"
ver_minor="${ver_minor_patch%%[^0-9]*}"

supports_popup=false
if [[ -n "${ver_major}" && -n "${ver_minor}" ]]; then
  if (( ver_major > 3 )) || (( ver_major == 3 && ver_minor >= 2 )); then
    supports_popup=true
  fi
fi

POPUP_WRAPPER="${REPO_ROOT}/scripts/popup_wrapper.sh"
log "launcher: supports_popup=${supports_popup}; popup_wrapper=${POPUP_WRAPPER}"

# Default behavior: new tmux window (native).
if [[ "${LAUNCH_MODE}" == "window" ]]; then
  log "launcher: launching in new tmux window; name=${WINDOW_NAME}"
  tmux display-message -d 1500 "tmux-session-manager: launching window" || true

  # Keep TERM stable for Bubble Tea. Also export TMUX_SESSION_MANAGER_BIN for any helpers.
  if ! tmux new-window -n "${WINDOW_NAME}" -c "#{pane_current_path}" -- \
    bash -lc "TERM=xterm-256color TMUX_SESSION_MANAGER_BIN=$(printf %q "${BIN_PATH}")${ENV_STR} ${CMD_STR}"
  then
    log "launcher: ERROR tmux new-window failed"
    tmux display-message -d 10000 "tmux-session-manager: failed to open window. See ${LOG_FILE}" || true
    exit 1
  fi

  log "launcher: window launched successfully"
  exit 0
fi

# Popup mode
if [[ "${supports_popup}" != true ]]; then
  log "launcher: ERROR popup unsupported; tmux_version=${version_raw}"
  tmux display-message -d 10000 "tmux-session-manager: tmux popup unsupported (need tmux >= 3.2). Detected tmux=${version_raw}" || true
  exit 1
fi

if [[ ! -x "${POPUP_WRAPPER}" ]]; then
  log "launcher: ERROR popup wrapper not executable: ${POPUP_WRAPPER}"
  tmux display-message -d 8000 "tmux-session-manager: popup wrapper not executable: ${POPUP_WRAPPER} (chmod +x it)" || true
  exit 1
fi

log "launcher: launching popup; w=${POPUP_W}; h=${POPUP_H}"
tmux display-message -d 1500 "tmux-session-manager: launching popup" || true

# -E: close popup when command exits successfully
if ! tmux display-popup -E -w "${POPUP_W}" -h "${POPUP_H}" -- \
  bash -lc "TERM=xterm-256color TMUX_SESSION_MANAGER_IN_POPUP=1 TMUX_SESSION_MANAGER_TITLE='tmux-session-manager' TMUX_SESSION_MANAGER_BIN=$(printf %q "${BIN_PATH}")${ENV_STR} \"${POPUP_WRAPPER}\" --cmd $(printf %q "${CMD_STR}")"
then
  log "launcher: ERROR tmux display-popup failed"
  tmux display-message -d 10000 "tmux-session-manager: popup failed. See ${LOG_FILE} and popup.log under ${LOG_DIR}/" || true
  exit 1
fi

log "launcher: popup launched successfully"
exit 0
