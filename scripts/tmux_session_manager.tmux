#!/usr/bin/env bash
set -euo pipefail

CURRENT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${CURRENT_DIR}/.." && pwd)"

mkdir -p "${HOME}/.config/tmux-session-manager" 2>/dev/null || true

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

if [[ ! -x "${BIN_PATH}" ]]; then
  tmux display-message -d 5000 "tmux-session-manager: binary not found or not executable at: ${BIN_PATH}" || true
  tmux display-message -d 5000 "Build it with: (cd ${REPO_ROOT} && go build -o bin/tmux-session-manager ./cmd/tmux-session-manager)" || true
  tmux display-message -d 5000 "Or set: set -g @tmux_session_manager_bin '/path/to/tmux-session-manager'" || true
  exit 1
fi

CMD_STR="exec \"${BIN_PATH}\""
if [[ -f "${CONFIG_PATH}" ]]; then
  CMD_STR+=" --config \"${CONFIG_PATH}\""
fi

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

if ! tmux display-message -d 1 "tmux-session-manager: starting" >/dev/null 2>&1; then
  echo "tmux-session-manager: executing: ${CMD_STR}"
  eval "${CMD_STR}"
  exit_code=$?
  if [[ ${exit_code} -ne 0 ]]; then
    echo "tmux-session-manager: command failed with exit code ${exit_code}"
  fi
  exit "${exit_code}"
fi

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

if [[ "${LAUNCH_MODE}" == "window" ]]; then
  tmux display-message -d 1500 "tmux-session-manager: launching window" || true

  if ! tmux new-window -n "${WINDOW_NAME}" -c "#{pane_current_path}" -- \
    bash -lc "TERM=xterm-256color TMUX_SESSION_MANAGER_BIN=$(printf %q "${BIN_PATH}")${ENV_STR} ${CMD_STR}"
  then
    tmux display-message -d 10000 "tmux-session-manager: failed to open window" || true
    exit 1
  fi

  exit 0
fi

if [[ "${supports_popup}" != true ]]; then
  tmux display-message -d 10000 "tmux-session-manager: tmux popup unsupported (need tmux >= 3.2). Detected tmux=${version_raw}" || true
  exit 1
fi

if [[ ! -x "${POPUP_WRAPPER}" ]]; then
  tmux display-message -d 8000 "tmux-session-manager: popup wrapper not executable: ${POPUP_WRAPPER} (chmod +x it)" || true
  exit 1
fi

tmux display-message -d 1500 "tmux-session-manager: launching popup" || true

# -E: close popup when command exits successfully
if ! tmux display-popup -E -w "${POPUP_W}" -h "${POPUP_H}" -- \
  bash -lc "TERM=xterm-256color TMUX_SESSION_MANAGER_IN_POPUP=1 TMUX_SESSION_MANAGER_TITLE='tmux-session-manager' TMUX_SESSION_MANAGER_BIN=$(printf %q "${BIN_PATH}")${ENV_STR} \"${POPUP_WRAPPER}\" --cmd $(printf %q "${CMD_STR}")"
then
  tmux display-message -d 10000 "tmux-session-manager: popup failed" || true
  exit 1
fi

exit 0
