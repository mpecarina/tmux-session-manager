# tmux-session-manager

A tmux plugin for creating/switching **project development sessions** with a consistent TUI and reproducible, repo-local session specs.

## Install (TPM)

Add to `~/.tmux.conf`:

```tmux
set -g @plugin '~/.tmux/plugins/tmux-session-manager'
# Optional (defaults shown)
set -g @tmux_session_manager_key 'S'
set -g @tmux_session_manager_bin '~/.tmux/plugins/tmux-session-manager/bin/tmux-session-manager'
set -g @tmux_session_manager_launch_mode 'window'   # window | popup

run '~/.tmux/plugins/tpm/tpm'
```

Reload tmux, then install via TPM: `prefix` + `I`.

## Build

From the plugin directory:

```sh
go build -o bin/tmux-session-manager ./cmd/tmux-session-manager
```

## Launch

- Default keybind: `prefix` + `S`
- Launcher opens the TUI in a tmux **window** by default (popup is optional and requires tmux popup support).

## Workflow

The TUI has two primary flows:

### 1) Sessions
- Lists existing tmux sessions.
- `Enter` switches to the selected session.

### 2) Projects
- Scans project roots for directories.
- `Enter` creates/bootstraps a session for the selected project, using:
  1) a project-local session spec (preferred), otherwise
  2) a built-in template (auto-detected)

## Project-local session spec (recommended)

If a project contains one of these at its root, it will be used to define the session:

- `./.tmux-session.yaml`
- `./.tmux-session.yml`
- `./.tmux-session.json`

## TUI keybindings

Vim-like defaults:

- `j` / `k`: down / up
- `gg` / `G`: top / bottom
- `Ctrl-d` / `Ctrl-u`: page down / page up
- `Enter`: switch/apply
- `/`: search
- `Esc`: clear/blur search
- `Tab`: toggle sessions/projects
- `p`: toggle preview
- `?` or `h`: help
- `q`: quit

## Configuration (tmux options)

Set these in `~/.tmux.conf`:

```tmux
# Plugin launcher
set -g @tmux_session_manager_key 'S'
set -g @tmux_session_manager_bin '~/.tmux/plugins/tmux-session-manager/bin/tmux-session-manager'
set -g @tmux_session_manager_launch_mode 'window'   # window | popup

# Project discovery
set -g @tmux_session_manager_roots '~/code,~/src,~/projects'
set -g @tmux_session_manager_project_depth '2'
set -g @tmux_session_manager_ignore_dirs '.git,node_modules,vendor,dist,build,target,.venv,__pycache__'

# Spec/template behavior
set -g @tmux_session_manager_prefer_project_spec 'on'
set -g @tmux_session_manager_project_spec_names '.tmux-session.yaml,.tmux-session.yml,.tmux-session.json'
set -g @tmux_session_manager_default_template 'auto'  # auto|empty|node|python|go

# Safety (defaults are off)
set -g @tmux_session_manager_allow_shell 'off'
set -g @tmux_session_manager_allow_tmux_passthrough 'off'
```

## Interoperability: tmux-ssh-manager dashboards → tmux-session-manager specs

`tmux-ssh-manager` can export a resolved dashboard (multi-pane SSH view) into a tmux-session-manager spec file (`.tmux-session.yaml` / `.json`) and optionally ask tmux-session-manager to apply it.

### How apply works

tmux-session-manager supports applying an arbitrary spec by path:

```sh
tmux-session-manager --spec /path/to/dashboard.tmux-session.yaml
```

This is the “integration mode” used by tmux-ssh-manager when it exports dashboards. It does not require the spec to live inside a project directory.

### What the exported spec generally contains

Exported dashboard specs are intended to stay in the “safe” subset:

- a session (name derived from the dashboard name)
- one window (commonly named `dashboard`)
- panes laid out deterministically when possible (`pane_plan`)
- pane actions that connect to a host and then run the dashboard’s effective commands (host/group on-connect + per-pane commands)

If you use password-backed SSH flows, the recommended pattern is for specs to use structured SSH actions that delegate credential/prompt handling to `tmux-ssh-manager` rather than typing secrets via `send-keys`.

### Common spec action types you may see/use

- `ssh_manager_connect`: structured SSH connect (delegates automation/credential handling to tmux-ssh-manager)
- `wait_for_prompt`: readiness gate before sending commands (helps with banners/MOTD)
- `watch`: safe repeat helper
- `send_keys`: literal commands/keys

### Initiation path from tmux-ssh-manager

When tmux-ssh-manager is configured to apply after export, it will run tmux-session-manager in a new tmux window:

- `tmux-session-manager --spec "<exported path>"`

The exact enabling knobs are owned by tmux-ssh-manager (env or launcher options). tmux-session-manager only needs:
- an accessible `tmux-session-manager` binary
- a valid spec file at the path

## CLI usage (optional)

The binary can be run directly (the plugin/launcher typically runs it for you):

- Open the TUI:
  - `tmux-session-manager`

- Apply a project by name (resolves under roots):
  - `tmux-session-manager --project <name>`

- Apply a spec by path:
  - `tmux-session-manager --spec /path/to/.tmux-session.yaml`

- If running outside tmux and you want it to start/attach tmux (opt-in):
  - `tmux-session-manager --bootstrap --project <name>`
  - or set `TMUX_SESSION_MANAGER_BOOTSTRAP=1`

## Troubleshooting

- Keybinding does nothing:
  - Ensure TPM loaded the plugin and you ran `prefix` + `I`.
  - Ensure `@tmux_session_manager_bin` points to an executable and you built it.

- Popup doesn’t work:
  - Set `@tmux_session_manager_launch_mode 'window'`
  - Ensure your tmux version supports popups.

License: MIT