# tmux-session-manager

A tmux plugin for creating, switching, and bootstrapping **project development sessions** with a consistent TUI across our tmux modules.

## SSH dashboards: safe `ssh_manager_connect` (delegates to tmux-ssh-manager) + `wait_for_prompt` + `watch`

For NOC-style dashboards (SSH panes + repeating commands), tmux-session-manager supports a **safe, structured** SSH connect action that **delegates password handling and interactive prompt automation to tmux-ssh-manager**.

Why delegation:
- tmux-ssh-manager already owns the macOS Keychain service (`tmux-ssh-manager`) and its storage conventions.
- It already implements a PTY-based "expect-like" connector (`__connect`) that can satisfy password prompts without typing secrets into tmux panes.
- It preserves the desired UX: `ssh` remains the interactive foreground program in the pane, so breaking a `watch` command returns you to the remote shell prompt.

This is intended to pair with:
- `wait_for_prompt` (best-effort readiness gate for banners/MOTD)
- `watch` (safe repeat helper compiled to `watch -n <interval> -t -- <cmd>` via send-keys)

This plugin is a simple wrapper around tmux sessions to fit the following workflow:
- select project directory
- create or bootstrap a session
- vim motions used to navigate layout

---

## Prerequisites

- tmux >= 3.2 recommended (popup support; window mode works broadly)
- TPM (Tmux Plugin Manager)
- Go toolchain (to build the binary)

---

## Install (local dev)

Build the binary:

```sh
cd ~/.tmux/plugins/tmux-ssh-manager/tmux-session-manager
go build -o bin/tmux-session-manager ./cmd/tmux-session-manager
```

Then add to your `~/.tmux.conf`:

```tmux
# Load the plugin run file (local path)
set -g @plugin '~/.tmux/plugins/tmux-ssh-manager/tmux-session-manager'

# Where the built binary lives
set -g @tmux_session_manager_bin '~/.tmux/plugins/tmux-ssh-manager/tmux-session-manager/bin/tmux-session-manager'

# Launch mode: window (default) | popup
set -g @tmux_session_manager_launch_mode 'window'

# Bind key (default in the run file is prefix + S; override here if you want)
set -g @tmux_session_manager_key 'o'

# TPM init (keep at bottom)
run '~/.tmux/plugins/tpm/tpm'
```

Reload tmux and install via TPM:
- `prefix` + `I`

---

## Operational workflow (end-to-end)

There are two "entry" paths that matter operationally:

### 1) Sessions mode (existing tmux sessions)
1. Launch the plugin (keybind → launcher script).
2. The UI lists `tmux list-sessions`.
3. `Enter` switches to the selected session (`tmux switch-client -t <name>`).
4. Optional session ops:
   - rename session
   - kill session (with confirm)

### 2) Projects mode (create/switch from a project directory)
1. Launch the plugin (keybind → launcher script).
2. Switch to "projects" mode (Tab) and select a project directory.
3. Determine the "session plan":
   - pick a session name derived from the project dir name (sanitized)
   - choose a template:
     - **project-local spec** (if present) takes precedence
     - else fallback to built-in template selection (auto/node/python/go/basic)
4. Create session if missing:
   - `tmux new-session -d -s <name> -c <projectDir>`
5. Apply the plan:
   - windows/panes/layout created using tmux primitives (`new-window`, `split-window`, `send-keys`, `select-layout`)
6. Switch to the session (`tmux switch-client -t <name>`).

---

## Project-local session spec (`.tmux-session.yaml` / `.tmux-session.json`)

If a project contains one of these files at its root, tmux-session-manager will use it to define the dev session:

- `./.tmux-session.yaml`
- `./.tmux-session.yml`
- `./.tmux-session.json`

### Why this exists
- "Auto" templates are good defaults, but projects often need consistent windows/panes/commands.
- Putting the spec in the repo makes the session reproducible for the whole team.

### Safety model: 3 tiers

**Tier 1 (default, recommended): safe + declarative**
- Use `windows[].pane_plan` (recommended) or `windows[].panes` to describe deterministic window/pane layouts.
- Use `actions` / `panes[].actions` for safe, structured operations:
  - `run` (program + args)
  - `send_keys`
  - `ssh_manager_connect` (safe structured SSH connect; delegates to tmux-ssh-manager for Keychain/PTTY automation)
  - `wait_for_prompt` (best-effort readiness gate for interactive targets like SSH; safe)
  - `watch` (repeat helper; compiled to `watch -n <interval> -t -- <cmd>` via send-keys)
  - safe tmux-building primitives (internally executed without raw passthrough)
- This keeps specs reproducible and avoids "accidental scripting".

**Tier 2 (opt-in): shell actions**
- The spec may include `shell` actions (arbitrary shell strings), but only if you opt in:
  - via tmux option: `set -g @tmux_session_manager_allow_shell 'on'`
  - or env: `TMUX_SESSION_MANAGER_ALLOW_SHELL=1`
- When enabled, the UI preview will clearly indicate that a spec contains arbitrary shell execution.

**Tier 3 (opt-in, advanced): validated tmux passthrough (`actions[].type: tmux`)**
- Allow raw tmux subcommands only when explicitly enabled:
  - `TMUX_SESSION_MANAGER_ALLOW_TMUX_PASSTHROUGH=1`
- Even then, passthrough is validated against an allowlist (and a denylist).
- Use this only when the safe declarative interface cannot express what you need.

> Recommendation: prefer `pane_plan` for deterministic splits (safe), and only reach for `actions.tmux` when necessary.

---

## Deterministic splits with `pane_plan` (recommended)

`windows[].panes` alone does **not** encode split direction/size, which can lead to non-deterministic layouts.

Use `windows[].pane_plan` to express split geometry declaratively (tmuxifier-like) without enabling tmux passthrough:

```yaml
version: 1
session:
  name: "${PROJECT_NAME}"
  root: "${PROJECT_PATH}"
  attach: true

env:
  EDITOR: "nvim"

windows:
  - name: editor
    root: "${PROJECT_PATH}"
    layout: main-vertical

    pane_plan:
      - pane:
          name: nvim
          focus: true
          actions:
            - type: run
              run:
                program: bash
                args: ["-lc", "${EDITOR:-nvim} ."]

      - split:
          direction: h
          size: "50%"

      - pane:
          name: shell
          actions:
            - type: run
              run:
                program: bash
                args: ["-lc", "${SHELL}"]

      - split:
          direction: v
          size: "30%"

      - pane:
          name: tests
          actions:
            - type: run
              run:
                program: bash
                args: ["-lc", "npm test"]
```

Notes:
- `direction: h` = side-by-side (`tmux split-window -h`)
- `direction: v` = stacked (`tmux split-window -v`)
- `size: "NN%"` is treated as a percent split when supported; `size: "NN"` is treated as an absolute size.
- `pane.command` is supported as shorthand, but using `actions` is recommended.

### Safe builtin watch helper (`type: watch`)

If you want a pane to run a command repeatedly (common for dashboards/NOC walls) without embedding an opaque shell string, you can use the SAFE `watch` action.

Schema:

- `type: watch`
- `watch.interval_s` (optional int; defaults to 2 when omitted/<=0)
- `watch.command` (required string)

Executor semantics (compiled safely as `send-keys` + Enter):

- `watch -n <interval_s> -t -- <command>`

Recommended: use `wait_for_prompt` immediately before `watch` after connecting so variable-length banners/MOTD don’t race with the first `watch` command.

Example:

```yaml
version: 1
session:
  name: "${PROJECT_NAME}"
  root: "${PROJECT_PATH}"

windows:
  - name: noc
    root: "${PROJECT_PATH}"
    pane_plan:
      - pane:
          name: rtr1
          actions:
            - type: run
              run:
                program: bash
                args: ["-lc", "ssh rtr1.example.com"]
                enter: true

            - type: wait_for_prompt
              wait_for_prompt:
                timeout_ms: 15000
                min_quiet_ms: 500
                settle_ms: 250
                # prompt_regex is optional; if omitted, executor uses a conservative default
                # prompt_regex: "(?m)(^.*[#>$] ?$)"
                max_lines: 200

            - type: watch
              watch:
                interval_s: 2
                command: "show clock"

      - split: { direction: h, size: "50%" }

      - pane:
          name: rtr2
          actions:
            - type: run
              run:
                program: bash
                args: ["-lc", "ssh rtr2.example.com"]
                enter: true

            - type: wait_for_prompt
              wait_for_prompt:
                timeout_ms: 15000
                min_quiet_ms: 500
                settle_ms: 250
                max_lines: 200

            - type: watch
              watch:
                interval_s: 5
                command: "show ip interface brief"
```

Notes / constraints:
- `watch` is "best-effort": it assumes the pane is at a prompt that will accept the command.
- `wait_for_prompt` is a SAFE approximation of "expect":
  - It polls recent pane output until it appears prompt-ready AND has been quiet for `min_quiet_ms`,
  - then waits `settle_ms` before allowing subsequent actions.
- If you use password auth, prefer `ssh_manager_connect` with `login_mode: askpass` so the connect step can proceed without blocking on a password prompt.

### Safe SSH connect helper (`type: ssh_manager_connect`)

If your SSH targets require password auth, you can use a SAFE structured action that connects in a pane and **delegates password automation to tmux-ssh-manager** (Keychain + PTY prompt handling) without ever typing secrets via tmux `send-keys`.

Delegation contract:

- tmux-session-manager runs (in the target pane):
  - `tmux-ssh-manager __connect --host <host> [--user <user>]`
- tmux-ssh-manager performs:
  - Keychain lookup using its existing service name: `tmux-ssh-manager`
  - PTY-based password prompt detection and password injection
  - interactive SSH session remains the foreground program in the pane (so breaking `watch` returns to the remote shell)

Schema:

- `type: ssh_manager_connect`
- `ssh_manager_connect.host` (required)
- `ssh_manager_connect.user` (optional; recommended when credentials were stored with a username)
- `ssh_manager_connect.login_mode` (optional: `askpass` | `manual` | `key`; default `askpass`)
- `ssh_manager_connect.port` (optional; note: tmux-ssh-manager `__connect` currently resolves port via ssh config/alias)

Recommended sequence for password-auth SSH dashboards:

1) `ssh_manager_connect` (connect)
2) `wait_for_prompt` (handle banners/MOTD)
3) `watch` (start repeating command)

Example:

```yaml
version: 1
session:
  name: "gateway"
  root: "${PROJECT_PATH}"

windows:
  - name: noc
    pane_plan:
      - pane:
          name: gateway
          actions:
            - type: ssh_manager_connect
              ssh_manager_connect:
                host: "192.168.0.1"
                user: "admin"
                login_mode: "askpass"

            - type: wait_for_prompt
              wait_for_prompt:
                timeout_ms: 30000
                min_quiet_ms: 500
                settle_ms: 250
                max_lines: 200

            - type: watch
              watch:
                interval_s: 2
                command: "show clock"
```

---

## Using `actions.tmux` (advanced / opt-in)

If you need something the safe interface cannot express, you can use `actions` with `type: tmux` (validated/allowlisted), but only when passthrough is enabled:

```yaml
version: 1
session:
  name: "${PROJECT_NAME}"
  root: "${PROJECT_PATH}"

actions:
  - type: tmux
    tmux:
      name: set-window-option
      args: ["-g", "aggressive-resize", "on"]
```

This requires:
- `TMUX_SESSION_MANAGER_ALLOW_TMUX_PASSTHROUGH=1` (or tmux option enabling passthrough)
- and the subcommand must be in the allowlist and not in the denylist

---

## Built-in templates (fallback)

If there is no project-local spec, tmux-session-manager falls back to built-ins.

### `auto`
Infers a template from project contents:
- `package.json` → `node`
- `go.mod` → `go`
- `pyproject.toml` / `requirements.txt` → `python`
- fallback → `basic`

### `basic`
- minimal windows/panes in project root

### `node`, `python`, `go`
- opinionated but small "starter" layouts, intended to be overridable via the project-local spec

---

## Configuration

### tmux options (recommended)

Set these in `~/.tmux.conf`:

```tmux
set -g @tmux_session_manager_key 'o'
set -g @tmux_session_manager_bin '~/.tmux/plugins/tmux-ssh-manager/tmux-session-manager/bin/tmux-session-manager'
set -g @tmux_session_manager_launch_mode 'window'

# Safety toggles (default off)
set -g @tmux_session_manager_allow_shell 'off'
# (advanced) allow validated tmux passthrough
set -g @tmux_session_manager_allow_tmux_passthrough 'off'

# Roots for project discovery (comma-separated)
set -g @tmux_session_manager_roots '~/code,~/src,~/projects'
```

# Optional: customize the project-local spec filenames (comma-separated)
# (defaults: .tmux-session.yaml,.tmux-session.yml,.tmux-session.json)
set -g @tmux_session_manager_project_spec_names '.tmux-session.yaml,.tmux-session.json'
```

### environment variables (runtime)

- `TMUX_SESSION_MANAGER_LAUNCH_MODE` → `window|popup`
- `TMUX_SESSION_MANAGER_IN_POPUP` → set by launcher in popup mode
- `TMUX_SESSION_MANAGER_ALLOW_SHELL=1` → enable shell actions
- `TMUX_SESSION_MANAGER_ALLOW_TMUX_PASSTHROUGH=1` → enable validated raw tmux actions

---

## Keybindings (inside the TUI)

We aim for consistent, vim-like ergonomics across modules. Expected defaults:

### Navigation
- `j` / `k`: move down/up
- `gg`: top
- `G`: bottom
- `Ctrl-d` / `Ctrl-u`: page down/up

### Actions
- `Enter`: switch session / create from project
- `/`: search/filter
- `Esc`: blur search
- `Tab`: toggle sessions/projects
- `p`: toggle preview
- `?` or `h`: help
- `q`: quit

### Session ops
- `r`: rename session
- `n`: new session
- `d`: kill session (confirm)

---

## Popup vs window behavior

- **window mode** (default): opens a new tmux window running the TUI (most reliable)
- **popup mode**: opens the TUI in a tmux popup (tmux >= 3.2). Launcher enforces a stable `TERM`.

---

## Build

From this plugin directory:

```sh
go build -o bin/tmux-session-manager ./cmd/tmux-session-manager
```

---

## Troubleshooting

- **Keybinding does nothing**
  - Confirm the plugin is loaded via TPM and `prefix + I` has been run.
  - Confirm `@tmux_session_manager_bin` points to an executable file.
  - Rebuild the binary after pulling changes.

- **Popup opens blank / rendering issues**
  - Switch to window mode: `set -g @tmux_session_manager_launch_mode 'window'`
  - Ensure tmux >= 3.2
  - Ensure `TERM` inside tmux is sane (`xterm-256color` is commonly reliable)

- **Session spec rejected**
  - If your spec uses `shell`, enable it explicitly:
    - `set -g @tmux_session_manager_allow_shell 'on'`
    - or `TMUX_SESSION_MANAGER_ALLOW_SHELL=1`

---

## License

MIT

---
