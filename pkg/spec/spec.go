package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Package spec defines the project-local session specification format used by tmux-session-manager.
//
// Files (project-local):
//   - .tmux-session.yaml / .tmux-session.yml
//   - .tmux-session.json
//
// Goals:
//   - Provide a safe, portable, declarative interface for creating tmux sessions/windows/panes.
//   - Support a controlled "escape hatch" for power users: shell passthrough (disabled by default).
//   - Make it easy to preview and validate what will be executed.
//
// Non-goals (for MVP):
//   - Full tmuxifier/tmuxinator parity (hooks, conditionals, ERB, etc.).
//   - A fully general scripting language. Keep it a schema + executor.
//
// Security model:
//   - By default, only whitelisted actions are allowed (no arbitrary shell).
//   - If AllowShell is enabled in runtime policy, Shell actions may run.
//   - Even with AllowShell, implementers should consider "confirm on first use" UX.

const (
	// CurrentVersion is the current schema version for project-local specs.
	CurrentVersion = 1
)

// Spec is the root document.
type Spec struct {
	Version int `json:"version" yaml:"version"`

	// Name is optional display name; session name defaults to derived project name unless Session.Name is set.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Description is optional.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Session settings.
	Session Session `json:"session,omitempty" yaml:"session,omitempty"`

	// Env are environment variables applied to all shell commands or actions that run processes.
	// NOTE: env is only meaningful if the executor supports it. Whitelisted tmux actions typically don't need it.
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`

	// Windows list.
	Windows []Window `json:"windows,omitempty" yaml:"windows,omitempty"`

	// Actions is an alternative "script-like" representation; either Windows or Actions may be used.
	// If Actions is provided and non-empty, executors may choose it as the primary plan.
	Actions []Action `json:"actions,omitempty" yaml:"actions,omitempty"`

	// Meta provides non-functional info.
	Meta map[string]string `json:"meta,omitempty" yaml:"meta,omitempty"`
}

// Session describes how to create/attach/switch.
type Session struct {
	// Name overrides derived session name (e.g. project basename).
	// If empty, executor derives from project path.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Prefix can be used to namespace sessions (e.g. "dev").
	Prefix string `json:"prefix,omitempty" yaml:"prefix,omitempty"`

	// Root is the working directory for the session. If empty, executor should use project root.
	Root string `json:"root,omitempty" yaml:"root,omitempty"`

	// Attach controls whether to switch/attach automatically after creation. Default true.
	Attach *bool `json:"attach,omitempty" yaml:"attach,omitempty"`

	// SwitchClient controls whether to use `tmux switch-client` when already inside tmux.
	// Default true.
	SwitchClient *bool `json:"switch_client,omitempty" yaml:"switch_client,omitempty"`

	// BaseIndex (window base index) and PaneBaseIndex can be set for compatibility with user prefs.
	BaseIndex     *int `json:"base_index,omitempty" yaml:"base_index,omitempty"`
	PaneBaseIndex *int `json:"pane_base_index,omitempty" yaml:"pane_base_index,omitempty"`

	// FocusWindow, when set, requests selecting a specific window *after* all windows are created.
	//
	// Supported forms:
	//   - "active": no-op (leave whatever window is active at the end of creation)
	//   - numeric string: window index (e.g. "1", "2") relative to tmux base-index
	//   - window name: e.g. "files"
	//
	// NOTE:
	// This is a declarative hint for executors; it does not require raw tmux passthrough.
	// Executors may interpret this as best-effort.
	FocusWindow string `json:"focus_window,omitempty" yaml:"focus_window,omitempty"`
}

// Window describes a tmux window.
type Window struct {
	Name string `json:"name" yaml:"name"`

	// Root sets working directory for panes created in this window. If empty, uses Session.Root / project root.
	Root string `json:"root,omitempty" yaml:"root,omitempty"`

	// Layout is a tmux layout name (e.g. "even-horizontal", "main-vertical", etc.).
	Layout string `json:"layout,omitempty" yaml:"layout,omitempty"`

	// Focus indicates this window should be selected after creation.
	Focus bool `json:"focus,omitempty" yaml:"focus,omitempty"`

	// FocusPane, when set, requests focusing a specific pane *after* the window's panes are created.
	//
	// Supported forms:
	//   - "active": no-op (leave whatever pane is active at the end of creation)
	//   - numeric string: pane index (e.g. "1", "2") relative to tmux pane-base-index
	//
	// NOTE:
	// This does not execute raw tmux passthrough; it is a declarative hint for executors.
	// If your executor cannot deterministically map indices (because pane-base-index is unknown),
	// it may ignore this field or fall back to focusing the window.
	FocusPane string `json:"focus_pane,omitempty" yaml:"focus_pane,omitempty"`

	// Panes contains panes and their actions/commands.
	//
	// If PanePlan is provided (non-empty), executors should prefer PanePlan over Panes
	// for pane creation because PanePlan encodes split geometry declaratively.
	Panes []Pane `json:"panes,omitempty" yaml:"panes,omitempty"`

	// PanePlan is an optional declarative split plan for building panes with geometry.
	//
	// Motivation:
	// - Panes[] alone is insufficient to express deterministic split direction/size (tmuxifier-like layouts).
	// - PanePlan allows repo-local reproducible layouts without requiring raw tmux passthrough.
	//
	// Semantics (MVP, intentionally simple):
	// - The plan is interpreted left-to-right.
	// - The first step should be a "pane" step (creates/initializes the first pane).
	// - Subsequent "split" steps declare how to split from the *currently active pane*.
	// - A "pane" step following a "split" step describes what to run in the newly created pane.
	//
	// Example:
	//   pane_plan:
	//     - pane: { name: editor, actions: [ ... ] }
	//     - split: { direction: h, size: "50%" }
	//     - pane: { name: shell, actions: [ ... ] }
	//     - split: { direction: v, size: "30%" }
	//     - pane: { name: tests, actions: [ ... ] }
	//
	// Notes:
	// - This stays within the safe, declarative interface (no arbitrary tmux commands required).
	// - For advanced cases, you can still use Spec.Actions with validated tmux passthrough (opt-in).
	PanePlan []PanePlanStep `json:"pane_plan,omitempty" yaml:"pane_plan,omitempty"`

	// Actions optionally provides window-scoped actions (for advanced usage).
	Actions []Action `json:"actions,omitempty" yaml:"actions,omitempty"`
}

// PanePlanStep is a tagged union: exactly one of Pane or Split must be set.
type PanePlanStep struct {
	Pane  *PanePlanPane  `json:"pane,omitempty" yaml:"pane,omitempty"`
	Split *PanePlanSplit `json:"split,omitempty" yaml:"split,omitempty"`
}

// PanePlanPane describes the pane created/selected after a split. It reuses the same
// conceptual fields as Pane, but is purpose-built for the pane plan.
type PanePlanPane struct {
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	Root string `json:"root,omitempty" yaml:"root,omitempty"`

	// Focus indicates this pane should be focused.
	//
	// IMPORTANT:
	// This is a legacy/compat flag that many executors interpret as "focus the window".
	// For deterministic post-plan focusing (e.g. focus the first/top pane after creating a dev pane),
	// prefer Window.FocusPane.
	Focus bool `json:"focus,omitempty" yaml:"focus,omitempty"`

	Actions []Action `json:"actions,omitempty" yaml:"actions,omitempty"`
	Command string   `json:"command,omitempty" yaml:"command,omitempty"`
}

// PanePlanSplit describes how to split from the currently active pane.
type PanePlanSplit struct {
	// Direction: "h" (side-by-side) or "v" (stacked)
	Direction string `json:"direction" yaml:"direction"`
	// Size: optional, e.g. "30%" or "20"
	Size string `json:"size,omitempty" yaml:"size,omitempty"`
}

// Pane describes a tmux pane within a window.
type Pane struct {
	// Name is optional metadata; tmux pane titles may be set by executor.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`

	// Root sets working directory for this pane. If empty, uses Window.Root / Session.Root / project root.
	Root string `json:"root,omitempty" yaml:"root,omitempty"`

	// Focus indicates this pane should be selected after creation.
	Focus bool `json:"focus,omitempty" yaml:"focus,omitempty"`

	// Actions describes what to do in the pane.
	// Typical: a single Run or Shell action.
	Actions []Action `json:"actions,omitempty" yaml:"actions,omitempty"`

	// Command is shorthand for a single action:
	//   - if it looks like a program + args array is desired, prefer Actions with Run.
	//   - if it is a shell snippet, it maps to Shell action (subject to policy).
	Command string `json:"command,omitempty" yaml:"command,omitempty"`
}

// Action is a safe, whitelisted set of operations.
// Executors should validate actions against runtime Policy before executing.
type Action struct {
	// Type identifies the action.
	// Supported types (recommended):
	//   - "tmux": a whitelisted tmux command (structured)
	//   - "run": run a program with args (structured; implemented by sending keys or direct exec)
	//   - "send_keys": send literal keys/strings to a pane (structured)
	//   - "shell": run arbitrary shell snippet (unsafe; requires AllowShell)
	//   - "sleep": pause for milliseconds (useful for timing)
	//   - "watch": SAFE builtin repeat helper (compiled to send-keys of a watch command)
	//   - "wait_for_prompt": SAFE best-effort "expect-like" readiness gate (polls pane output until prompt/quiet)
	//   - "ssh_manager_connect": SAFE structured SSH connect action (optional askpass using Keychain)
	Type string `json:"type" yaml:"type"`

	// Target describes the tmux target this action applies to.
	// Targets are resolved by the executor (session/window/pane context).
	Target Target `json:"target,omitempty" yaml:"target,omitempty"`

	// For "tmux" action: Name is the tmux subcommand, Args are arguments (no shell).
	Tmux *TmuxAction `json:"tmux,omitempty" yaml:"tmux,omitempty"`

	// For "run" action: Program + Args represent an argv; Executor decides how to execute.
	Run *RunAction `json:"run,omitempty" yaml:"run,omitempty"`

	// For "send_keys" action: Keys is a list of strings; Enter indicates to send Enter after.
	SendKeys *SendKeysAction `json:"send_keys,omitempty" yaml:"send_keys,omitempty"`

	// For "shell" action: Cmd is shell snippet to execute.
	Shell *ShellAction `json:"shell,omitempty" yaml:"shell,omitempty"`

	// For "sleep" action.
	Sleep *SleepAction `json:"sleep,omitempty" yaml:"sleep,omitempty"`

	// For "watch" action: declarative repeat helper (safe).
	Watch *WatchAction `json:"watch,omitempty" yaml:"watch,omitempty"`

	// For "wait_for_prompt" action: safe best-effort readiness gate (safe).
	WaitForPrompt *WaitForPromptAction `json:"wait_for_prompt,omitempty" yaml:"wait_for_prompt,omitempty"`

	// For "ssh_manager_connect" action: safe structured SSH connection (askpass can reuse tmux-ssh-manager Keychain service).
	SshManagerConnect *SshManagerConnectAction `json:"ssh_manager_connect,omitempty" yaml:"ssh_manager_connect,omitempty"`

	// If true, failure should not abort the whole plan (best-effort).
	IgnoreError bool `json:"ignore_error,omitempty" yaml:"ignore_error,omitempty"`

	// If set, executor may show this in UI preview.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`
}

// Target describes where an action should apply.
// Fields are optional; executor may apply defaults based on current creation context.
type Target struct {
	Session string `json:"session,omitempty" yaml:"session,omitempty"`
	Window  string `json:"window,omitempty" yaml:"window,omitempty"`
	Pane    string `json:"pane,omitempty" yaml:"pane,omitempty"`
}

// TmuxAction is a structured tmux subcommand invocation.
type TmuxAction struct {
	// Name is the tmux subcommand (e.g. "new-window", "split-window", "select-layout").
	Name string   `json:"name" yaml:"name"`
	Args []string `json:"args,omitempty" yaml:"args,omitempty"`
}

// RunAction describes a program execution in a pane.
type RunAction struct {
	Program string   `json:"program" yaml:"program"`
	Args    []string `json:"args,omitempty" yaml:"args,omitempty"`
	// Enter indicates whether executor should send Enter after the command when using send-keys model.
	Enter *bool `json:"enter,omitempty" yaml:"enter,omitempty"`
}

// SendKeysAction describes sending keystrokes/text to a pane.
type SendKeysAction struct {
	Keys  []string `json:"keys" yaml:"keys"`
	Enter bool     `json:"enter,omitempty" yaml:"enter,omitempty"`
}

// ShellAction is an escape hatch for arbitrary shell. Requires AllowShell policy.
type ShellAction struct {
	Cmd string `json:"cmd" yaml:"cmd"`
	// Shell overrides the default shell (e.g. "bash", "zsh"). Optional.
	Shell string `json:"shell,omitempty" yaml:"shell,omitempty"`
}

// SleepAction pauses execution for timing.
type SleepAction struct {
	Milliseconds int `json:"ms" yaml:"ms"`
}

// WatchAction is a SAFE, declarative helper that expresses "repeat this command" without requiring shell passthrough.
//
// Executors should compile this into a send-keys of:
//
//	watch -n <interval_s> -t -- <command>
//
// Notes:
// - interval_s is optional; if unset/<=0, treat as 2 seconds.
// - command must be non-empty.
type WatchAction struct {
	IntervalSeconds int    `json:"interval_s,omitempty" yaml:"interval_s,omitempty"`
	Command         string `json:"command" yaml:"command"`
}

// WaitForPromptAction is a SAFE, best-effort readiness gate intended for interactive targets (e.g. SSH).
//
// It approximates an "expect" sequence without requiring shell passthrough by allowing executors to
// poll pane output (e.g. `tmux capture-pane -p`) until:
//
//  1. the output matches a prompt-like regex OR some executor-defined "ready" heuristic, AND
//  2. the output has remained unchanged for at least MinQuietMS (to allow MOTD/banner output to settle), AND
//  3. an optional SettleMS elapses to ensure no trailing output arrives before subsequent actions.
//
// Defaulting guidance for executors (when fields are zero/unset):
// - TimeoutMS: 15000
// - MinQuietMS: 500
// - SettleMS: 250
// - PromptRegex: executor default (suggestion: (?m)(^.*[#>$] ?$))
// - MaxLines: 200
type WaitForPromptAction struct {
	// TimeoutMS bounds total wait time. If <=0, treat as 15000.
	TimeoutMS int `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`

	// MinQuietMS requires the captured pane output to be unchanged for this long before considering it ready.
	// If <=0, treat as 500.
	MinQuietMS int `json:"min_quiet_ms,omitempty" yaml:"min_quiet_ms,omitempty"`

	// SettleMS is an extra delay after readiness is detected, before allowing subsequent actions to proceed.
	// If <=0, treat as 250.
	SettleMS int `json:"settle_ms,omitempty" yaml:"settle_ms,omitempty"`

	// PromptRegex is an optional regex used to detect a prompt-like last line.
	// If empty, executor should use its own conservative default.
	PromptRegex string `json:"prompt_regex,omitempty" yaml:"prompt_regex,omitempty"`

	// MaxLines controls how many lines of pane output to consider (e.g. last N lines).
	// If <=0, executor should choose a safe default (e.g. 200).
	MaxLines int `json:"max_lines,omitempty" yaml:"max_lines,omitempty"`
}

// SshManagerConnectAction is a SAFE, structured SSH connect action intended for specs that
// want tmux-session-manager to handle password auth similarly to tmux-ssh-manager, without
// using shell passthrough.
//
// The executor is expected to:
//   - start an interactive ssh session in the target pane
//   - if LoginMode == "askpass", provide SSH_ASKPASS that retrieves a password from macOS Keychain
//     using the existing tmux-ssh-manager service name ("tmux-ssh-manager").
//   - optionally honor ConnectTimeoutMS as a best-effort bound for readiness before proceeding.
//
// Notes:
// - This action is safe/structured: it does not execute arbitrary shell from the spec.
// - Passwords must never be written to tmux buffers/logs; only provided to ssh via askpass.
type SshManagerConnectAction struct {
	Host string `json:"host" yaml:"host"`

	// Optional; if empty, ssh default user resolution applies.
	User string `json:"user,omitempty" yaml:"user,omitempty"`

	// Optional; if <=0, ssh default applies.
	Port int `json:"port,omitempty" yaml:"port,omitempty"`

	// LoginMode controls auth strategy:
	// - "askpass" (default): use SSH_ASKPASS with Keychain lookup (service "tmux-ssh-manager")
	// - "manual": allow ssh to prompt in-pane
	// - "key": rely on agent/keys (no askpass)
	LoginMode string `json:"login_mode,omitempty" yaml:"login_mode,omitempty"`

	// ConnectTimeoutMS is a best-effort bound for the connect attempt. If <=0, executor default.
	ConnectTimeoutMS int `json:"connect_timeout_ms,omitempty" yaml:"connect_timeout_ms,omitempty"`
}

// Policy defines runtime execution allowances. This is NOT serialized in the spec.
// It is provided by the executor based on user configuration (tmux options/env).
type Policy struct {
	// AllowShell enables "shell" actions and Pane.Command shorthands treated as shell.
	AllowShell bool

	// AllowTmuxPassthrough enables "tmux" actions for commands beyond the safe allowlist.
	// Recommended default: false.
	AllowTmuxPassthrough bool

	// AllowedTmuxCommands is the allowlist for "tmux" action names when AllowTmuxPassthrough=false.
	AllowedTmuxCommands map[string]bool
}

// DefaultPolicy returns a conservative allowlist.
func DefaultPolicy() Policy {
	allowed := map[string]bool{
		// Creation / navigation
		"new-session":    true,
		"new-window":     true,
		"split-window":   true,
		"rename-window":  true,
		"rename-session": true,
		"select-layout":  true,
		"select-window":  true,
		"select-pane":    true,
		"kill-window":    true,
		"kill-pane":      true,
		"kill-session":   true,

		// Input / config
		"send-keys":         true,
		"set-option":        true,
		"set-window-option": true,

		// Introspection (useful for preview/dry-run)
		"list-windows":    true,
		"list-panes":      true,
		"display-message": true,
	}
	return Policy{
		AllowShell:           false,
		AllowTmuxPassthrough: false,
		AllowedTmuxCommands:  allowed,
	}
}

// Validate performs structural validation of the spec. It does NOT apply security policy.
// Call ValidatePolicy separately to enforce AllowShell / tmux allowlist rules.
func (s *Spec) Validate() error {
	if s.Version == 0 {
		// Version is optional; default to CurrentVersion.
		s.Version = CurrentVersion
	}
	if s.Version != CurrentVersion {
		return fmt.Errorf("unsupported spec version %d (expected %d)", s.Version, CurrentVersion)
	}

	// If Actions are present, Windows may be empty.
	if len(s.Actions) == 0 && len(s.Windows) == 0 {
		return errors.New("spec must define either windows[] or actions[]")
	}

	for i := range s.Windows {
		w := &s.Windows[i]
		if strings.TrimSpace(w.Name) == "" {
			return fmt.Errorf("windows[%d].name is required", i)
		}

		// Validate focus_pane (optional)
		w.FocusPane = strings.TrimSpace(strings.ToLower(w.FocusPane))
		if w.FocusPane != "" && w.FocusPane != "active" {
			// must be an integer string >= 0 (actual meaning is executor-defined relative to pane-base-index)
			for _, r := range w.FocusPane {
				if r < '0' || r > '9' {
					return fmt.Errorf("windows[%d](%s).focus_pane must be \"active\" or a numeric string (got %q)", i, w.Name, w.FocusPane)
				}
			}
		}

		// pane_plan validation (preferred when present)
		if len(w.PanePlan) > 0 {
			if err := validatePanePlan(w.PanePlan); err != nil {
				return fmt.Errorf("windows[%d](%s).pane_plan: %w", i, w.Name, err)
			}

			for si := range w.PanePlan {
				step := &w.PanePlan[si]
				if step.Pane == nil {
					continue
				}

				// Normalize shorthand command -> shell action.
				if step.Pane.Command != "" && len(step.Pane.Actions) == 0 {
					step.Pane.Actions = []Action{
						{
							Type:  "shell",
							Shell: &ShellAction{Cmd: step.Pane.Command},
						},
					}
				}

				for ak := range step.Pane.Actions {
					if err := validateAction(&step.Pane.Actions[ak]); err != nil {
						return fmt.Errorf("windows[%d](%s).pane_plan[%d].pane.actions[%d]: %w", i, w.Name, si, ak, err)
					}
				}
			}
		}

		// panes[] validation (legacy / simpler form)
		for j := range w.Panes {
			p := &w.Panes[j]
			// Normalize shorthand command.
			if p.Command != "" && len(p.Actions) == 0 {
				p.Actions = []Action{
					{
						Type:  "shell",
						Shell: &ShellAction{Cmd: p.Command},
					},
				}
			}
			for k := range p.Actions {
				if err := validateAction(&p.Actions[k]); err != nil {
					return fmt.Errorf("windows[%d](%s).panes[%d].actions[%d]: %w", i, w.Name, j, k, err)
				}
			}
		}

		for k := range w.Actions {
			if err := validateAction(&w.Actions[k]); err != nil {
				return fmt.Errorf("windows[%d](%s).actions[%d]: %w", i, w.Name, k, err)
			}
		}
	}

	for i := range s.Actions {
		if err := validateAction(&s.Actions[i]); err != nil {
			return fmt.Errorf("actions[%d]: %w", i, err)
		}
	}

	// Session name constraints are validated later by executor (it may derive).
	if s.Session.Name != "" {
		if err := ValidateTmuxName(s.Session.Name); err != nil {
			return fmt.Errorf("session.name: %w", err)
		}
	}

	// Validate session.focus_window (optional)
	s.Session.FocusWindow = strings.TrimSpace(s.Session.FocusWindow)
	if s.Session.FocusWindow != "" {
		lw := strings.ToLower(s.Session.FocusWindow)
		if lw == "active" {
			s.Session.FocusWindow = "active"
		} else {
			// Either numeric (window index) or a non-empty name.
			isNum := true
			for _, r := range s.Session.FocusWindow {
				if r < '0' || r > '9' {
					isNum = false
					break
				}
			}
			if !isNum {
				if err := ValidateTmuxName(s.Session.FocusWindow); err != nil {
					return fmt.Errorf("session.focus_window: %w", err)
				}
			}
		}
	}

	return nil
}

func validateAction(a *Action) error {
	a.Type = strings.TrimSpace(strings.ToLower(a.Type))
	if a.Type == "" {
		return errors.New("missing type")
	}
	switch a.Type {
	case "tmux":
		if a.Tmux == nil {
			return errors.New("tmux action missing tmux{}")
		}
		a.Tmux.Name = strings.TrimSpace(a.Tmux.Name)
		if a.Tmux.Name == "" {
			return errors.New("tmux.name is required")
		}
	case "run":
		if a.Run == nil {
			return errors.New("run action missing run{}")
		}
		a.Run.Program = strings.TrimSpace(a.Run.Program)
		if a.Run.Program == "" {
			return errors.New("run.program is required")
		}
	case "send_keys":
		if a.SendKeys == nil {
			return errors.New("send_keys action missing send_keys{}")
		}
		if len(a.SendKeys.Keys) == 0 {
			return errors.New("send_keys.keys is required")
		}
	case "shell":
		if a.Shell == nil {
			return errors.New("shell action missing shell{}")
		}
		a.Shell.Cmd = strings.TrimSpace(a.Shell.Cmd)
		if a.Shell.Cmd == "" {
			return errors.New("shell.cmd is required")
		}
	case "sleep":
		if a.Sleep == nil {
			return errors.New("sleep action missing sleep{}")
		}
		if a.Sleep.Milliseconds < 0 {
			return errors.New("sleep.ms must be >= 0")
		}
	case "watch":
		if a.Watch == nil {
			return errors.New("watch action missing watch{}")
		}
		if a.Watch.IntervalSeconds < 0 {
			return errors.New("watch.interval_s must be >= 0")
		}
		a.Watch.Command = strings.TrimSpace(a.Watch.Command)
		if a.Watch.Command == "" {
			return errors.New("watch.command is required")
		}
	case "wait_for_prompt":
		if a.WaitForPrompt == nil {
			return errors.New("wait_for_prompt action missing wait_for_prompt{}")
		}
		if a.WaitForPrompt.TimeoutMS < 0 {
			return errors.New("wait_for_prompt.timeout_ms must be >= 0")
		}
		if a.WaitForPrompt.MinQuietMS < 0 {
			return errors.New("wait_for_prompt.min_quiet_ms must be >= 0")
		}
		if a.WaitForPrompt.SettleMS < 0 {
			return errors.New("wait_for_prompt.settle_ms must be >= 0")
		}
		if a.WaitForPrompt.MaxLines < 0 {
			return errors.New("wait_for_prompt.max_lines must be >= 0")
		}
		a.WaitForPrompt.PromptRegex = strings.TrimSpace(a.WaitForPrompt.PromptRegex)

	case "ssh_manager_connect":
		if a.SshManagerConnect == nil {
			return errors.New("ssh_manager_connect action missing ssh_manager_connect{}")
		}
		a.SshManagerConnect.Host = strings.TrimSpace(a.SshManagerConnect.Host)
		if a.SshManagerConnect.Host == "" {
			return errors.New("ssh_manager_connect.host is required")
		}
		a.SshManagerConnect.User = strings.TrimSpace(a.SshManagerConnect.User)
		if a.SshManagerConnect.Port < 0 {
			return errors.New("ssh_manager_connect.port must be >= 0")
		}
		a.SshManagerConnect.LoginMode = strings.TrimSpace(strings.ToLower(a.SshManagerConnect.LoginMode))
		if a.SshManagerConnect.LoginMode == "" {
			a.SshManagerConnect.LoginMode = "askpass"
		}
		switch a.SshManagerConnect.LoginMode {
		case "askpass", "manual", "key":
			// ok
		default:
			return fmt.Errorf("ssh_manager_connect.login_mode must be askpass|manual|key (got %q)", a.SshManagerConnect.LoginMode)
		}
		if a.SshManagerConnect.ConnectTimeoutMS < 0 {
			return errors.New("ssh_manager_connect.connect_timeout_ms must be >= 0")
		}

	default:
		return fmt.Errorf("unknown action type %q", a.Type)
	}
	return nil
}

func validatePanePlan(steps []PanePlanStep) error {
	if len(steps) == 0 {
		return nil
	}

	// First step should be a pane.
	if steps[0].Pane == nil || steps[0].Split != nil {
		return errors.New("first step must be pane")
	}

	for i := range steps {
		step := &steps[i]

		hasPane := step.Pane != nil
		hasSplit := step.Split != nil

		if hasPane == hasSplit {
			return fmt.Errorf("step[%d] must have exactly one of pane or split", i)
		}

		if hasSplit {
			dir := strings.ToLower(strings.TrimSpace(step.Split.Direction))
			if dir != "h" && dir != "v" {
				return fmt.Errorf("step[%d].split.direction must be 'h' or 'v'", i)
			}
			// Size is optional; no further validation here (executor decides -p vs -l).
			continue
		}

		// Pane step: normalize in Validate() and validate actions there.
		// Nothing to do here besides a minimal sanity check.
		if step.Pane != nil {
			_ = strings.TrimSpace(step.Pane.Name)
		}
	}

	// Ensure pane/split alternation isn't strictly required, but split must be followed by a pane
	// for a meaningful plan. Enforce "no trailing split".
	if steps[len(steps)-1].Split != nil {
		return errors.New("last step must be pane (cannot end with split)")
	}

	return nil
}

// ValidatePolicy enforces execution policy rules (shell allow, tmux allowlist).
func (s *Spec) ValidatePolicy(pol Policy) error {
	// Normalize allowlist presence.
	if pol.AllowedTmuxCommands == nil {
		p := DefaultPolicy()
		pol.AllowedTmuxCommands = p.AllowedTmuxCommands
	}

	check := func(a Action) error {
		switch a.Type {
		case "shell":
			if !pol.AllowShell {
				return errors.New("shell actions are disabled by policy")
			}
		case "tmux":
			if a.Tmux == nil {
				return errors.New("tmux action missing tmux{}")
			}
			cmd := strings.TrimSpace(a.Tmux.Name)
			if cmd == "" {
				return errors.New("tmux.name is required")
			}
			if !pol.AllowTmuxPassthrough && !pol.AllowedTmuxCommands[cmd] {
				return fmt.Errorf("tmux command %q not allowed by policy", cmd)
			}
		}
		return nil
	}

	for _, a := range s.Actions {
		if err := check(a); err != nil {
			return err
		}
	}
	for _, w := range s.Windows {
		for _, a := range w.Actions {
			if err := check(a); err != nil {
				return fmt.Errorf("window %q: %w", w.Name, err)
			}
		}
		for _, p := range w.Panes {
			for _, a := range p.Actions {
				if err := check(a); err != nil {
					return fmt.Errorf("window %q pane %q: %w", w.Name, p.Name, err)
				}
			}
		}
	}
	return nil
}

// LoadProjectLocal attempts to load a project-local spec file from a directory.
// It checks (in order):
//   - .tmux-session.yaml
//   - .tmux-session.yml
//   - .tmux-session.json
//
// Returns (spec, pathUsed, ok, err).
func LoadProjectLocal(projectDir string) (*Spec, string, bool, error) {
	return LoadProjectLocalWithNames(projectDir, []string{
		".tmux-session.yaml",
		".tmux-session.yml",
		".tmux-session.json",
	})
}

// LoadProjectLocalWithNames attempts to load a project-local spec file from a directory,
// using a customizable list of candidate filenames.
//
// - names are treated as basenames relative to projectDir (not absolute paths).
// - names are tried in order.
// - if names is empty, this falls back to the same defaults as LoadProjectLocal.
//
// Returns (spec, pathUsed, ok, err).
func LoadProjectLocalWithNames(projectDir string, names []string) (*Spec, string, bool, error) {
	projectDir = strings.TrimSpace(projectDir)
	if projectDir == "" {
		return nil, "", false, errors.New("projectDir is empty")
	}

	if len(names) == 0 {
		names = []string{
			".tmux-session.yaml",
			".tmux-session.yml",
			".tmux-session.json",
		}
	}

	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		// Treat as project-local basename, not an absolute path.
		p := filepath.Join(projectDir, name)

		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue
		}

		s, err := LoadFile(p)
		if err != nil {
			return nil, p, true, err
		}
		return s, p, true, nil
	}

	return nil, "", false, nil
}

// LoadFile loads a spec from a YAML or JSON file path.
func LoadFile(path string) (*Spec, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("empty path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	ext := strings.ToLower(filepath.Ext(path))
	var s Spec
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(b, &s); err != nil {
			return nil, err
		}
	case ".json":
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
	default:
		// Heuristic: try YAML then JSON.
		if err := yaml.Unmarshal(b, &s); err != nil {
			if jerr := json.Unmarshal(b, &s); jerr != nil {
				return nil, fmt.Errorf("unknown spec file type %q; yaml err: %v; json err: %v", ext, err, jerr)
			}
		}
	}

	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// ValidateTmuxName validates a tmux session/window name (best-effort).
// tmux is permissive, but names with ':' and '.' cause frequent tool friction.
// We enforce a conservative subset by default.
func ValidateTmuxName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("empty name")
	}
	// Disallow characters that often break tooling or tmux targeting conventions.
	// Allowed: alnum, underscore, dash.
	re := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
	if !re.MatchString(name) {
		return fmt.Errorf("invalid name %q (allowed: [a-zA-Z0-9_-])", name)
	}
	return nil
}

// DeriveSessionName returns a tmux-safe derived name from:
//   - optional prefix
//   - project basename
//
// This does not check for collisions (executor should handle that).
func DeriveSessionName(prefix, projectPath string) string {
	base := filepath.Base(strings.TrimRight(projectPath, string(filepath.Separator)))
	base = sanitizeName(base)
	if prefix != "" {
		prefix = sanitizeName(prefix)
		if prefix != "" {
			return prefix + "-" + base
		}
	}
	return base
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	if s == "" {
		return ""
	}
	// Replace spaces and path-ish separators.
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, string(filepath.Separator), "-")
	// Keep only safe chars.
	re := regexp.MustCompile(`[^a-z0-9_-]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-_")
	s = collapseRepeats(s, '-')
	s = collapseRepeats(s, '_')
	if s == "" {
		s = "session"
	}
	return s
}

func collapseRepeats(s string, ch rune) string {
	var b strings.Builder
	b.Grow(len(s))
	var prev rune
	for i, r := range s {
		if i == 0 {
			b.WriteRune(r)
			prev = r
			continue
		}
		if r == ch && prev == ch {
			continue
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}
