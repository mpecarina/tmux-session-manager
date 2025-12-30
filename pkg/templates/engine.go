package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Engine compiles a session spec (actions) into tmux commands and can optionally execute them.
//
// Design goals:
// - Support both "safe" declarative actions and explicit escape hatches.
// - Make execution paths obvious: safe actions vs shell vs raw tmux passthrough.
// - Provide deterministic dry-run output (for TUI preview) and robust validation.
//
// This file intentionally does not perform project discovery or spec file parsing.
// It assumes you already have a validated Spec (see types below) and a concrete Context.
//
// NOTE: Execution is kept lightweight and delegates actual tmux invocation to a Runner.
type Engine struct {
	Policy Policy
	Runner Runner
	Clock  func() time.Time
}

func NewEngine() *Engine {
	return &Engine{
		Policy: DefaultPolicy(),
		Runner: &NoopRunner{},
		Clock:  time.Now,
	}
}

// Policy controls what is allowed when compiling/executing templates.
type Policy struct {
	// AllowShell permits ActionShell. Disabled by default (safer).
	AllowShell bool

	// AllowTmuxPassthrough permits ActionTmux. Disabled by default (safer).
	AllowTmuxPassthrough bool

	// AllowedTmuxCommands is the allowlist of tmux subcommands accepted for passthrough.
	// If empty, a conservative default allowlist is used.
	AllowedTmuxCommands map[string]bool

	// DisallowTmuxCommands blocks specific tmux subcommands even if allowlisted (belt+braces).
	DisallowTmuxCommands map[string]bool

	// MaxActions is a guardrail against runaway specs.
	MaxActions int

	// MaxCommandLen bounds generated command strings (shell and tmux args).
	MaxCommandLen int
}

func DefaultPolicy() Policy {
	return Policy{
		AllowShell:           false,
		AllowTmuxPassthrough: false,
		AllowedTmuxCommands:  defaultAllowedTmuxCommands(),
		DisallowTmuxCommands: map[string]bool{
			// Explicitly dangerous by default if passthrough is ever enabled.
			"run-shell":    true,
			"if-shell":     true,
			"pipe-pane":    true,
			"respawn-pane": true,
		},
		MaxActions:    200,
		MaxCommandLen: 4096,
	}
}

func defaultAllowedTmuxCommands() map[string]bool {
	// Conservative: only layout-building and navigation primitives.
	cmds := []string{
		"new-session",
		"kill-session",
		"rename-session",
		"switch-client",
		"select-session",

		"new-window",
		"kill-window",
		"rename-window",
		"select-window",
		"move-window",
		"swap-window",
		"list-windows",
		"list-sessions",

		"split-window",
		"kill-pane",
		"select-pane",
		"resize-pane",
		"select-layout",

		"send-keys",
		"display-message",
		"set-option",
		"set-buffer",
	}
	out := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		out[c] = true
	}
	return out
}

// Runner executes tmux commands.
type Runner interface {
	// Run runs the tmux command: tmux <args...>
	Run(args []string) error

	// RunOutput runs the tmux command and returns combined stdout/stderr output.
	// This is required for safe "introspection" style actions (e.g. capture-pane polling)
	// without using shell passthrough.
	RunOutput(args []string) (string, error)
}

// NoopRunner is useful for dry-run contexts or tests.
type NoopRunner struct{}

func (r *NoopRunner) Run(_ []string) error { return nil }

func (r *NoopRunner) RunOutput(_ []string) (string, error) { return "", nil }

// Context provides variable substitution and target session information.
type Context struct {
	ProjectName string
	ProjectPath string

	// SessionName is the tmux session to create/target.
	SessionName string

	// WorkingDir is used as default cwd when not otherwise specified.
	WorkingDir string

	// Env provides values for ${VAR} substitutions (in addition to process env).
	Env map[string]string

	// Optional: socket name / server selection in multi-tmux setups.
	TmuxSocket string
}

// Spec is a parsed project-local template definition (YAML/JSON), reduced to a list of actions.
// Higher layers can build this from .tmux-session.{yaml,json}.
type Spec struct {
	Version int
	ID      string
	Name    string

	// Actions are executed in order.
	Actions []Action

	// Optional: metadata for UX
	Unsafe bool // if true, spec declares it needs unsafe features (shell/passthrough)
}

// ActionKind identifies the action type.
type ActionKind string

const (
	ActionEnsureSession ActionKind = "ensure_session"
	ActionNewWindow     ActionKind = "new_window"
	ActionSplitWindow   ActionKind = "split_window"
	ActionSelectWindow  ActionKind = "select_window"
	ActionSelectPane    ActionKind = "select_pane"
	ActionSelectLayout  ActionKind = "select_layout"
	ActionSendKeys      ActionKind = "send_keys"
	ActionSetOption     ActionKind = "set_option"
	ActionDisplay       ActionKind = "display_message"

	// Safe: window/session construction primitives
	ActionRenameWindow ActionKind = "rename_window"

	// Safe: readiness / gating primitives (no shell required)
	ActionWaitForPrompt ActionKind = "wait_for_prompt"

	// Safe: structured SSH connect (no shell required).
	//
	// For password automation, we delegate to tmux-ssh-manager’s internal PTY connector:
	//   tmux-ssh-manager __connect --host <host> [--user <user>]
	//
	// This avoids re-implementing credential/expect logic here and prevents leaking secrets via tmux send-keys.
	ActionSshManagerConnect ActionKind = "ssh_manager_connect"

	// Escape hatches
	ActionShell ActionKind = "shell" // bash -lc "<cmd>" via tmux send-keys or as a window command
	ActionTmux  ActionKind = "tmux"  // raw tmux args (validated)
)

// Action is a single declarative unit.
type Action struct {
	Kind ActionKind

	// Generic targeting
	Session string // optional override; defaults to ctx.SessionName
	Window  string // window name or index (implementation uses name)
	Pane    string // pane index or target (%id); optional

	// Cwd to use for new window/splits (tmux -c)
	Cwd string

	// Name for new window (or new name for rename-window)
	Name string

	// For rename-window
	// If From is empty, the caller may encode the target in Window (e.g. "0" or "editor"),
	// otherwise From is treated as the source window identifier.
	From string

	// For split
	Direction string // "h" or "v"
	Percent   int    // 1-99 optional

	// For layout
	Layout string // "tiled", "even-horizontal", etc.

	// For send-keys
	Command string   // command string (expanded)
	Keys    []string // raw key tokens; if set, used instead of Command
	Enter   bool     // append Enter/C-m

	// For wait_for_prompt (safe polling gate; executor performs tmux capture-pane polling)
	TimeoutMS  int    // total timeout; if <=0, executor should default (e.g. 15000)
	MinQuietMS int    // require unchanged output for at least this long; if <=0 default (e.g. 500)
	SettleMS   int    // extra delay after ready; if <=0 default (e.g. 250)
	PromptRe   string // optional prompt regex; if empty executor default (e.g. (?m)(^.*[#>$] ?$))
	MaxLines   int    // max lines of pane output to inspect; if <=0 default (e.g. 200)

	// For ssh_manager_connect (safe structured SSH connect).
	//
	// NOTE:
	// - We intentionally do NOT handle passwords in this engine.
	// - For login_mode=askpass, we delegate to tmux-ssh-manager __connect.
	Host             string // host or ssh alias (required)
	User             string // optional
	Port             int    // optional; if <=0, ssh default
	LoginMode        string // askpass|manual|key (executor default: askpass)
	ConnectTimeoutMS int    // optional; if <=0, executor default

	// For set-option
	Option string
	Value  string
	Global bool // set -g

	// For display-message
	Message    string
	DurationMS int

	// Unsafe: shell and tmux passthrough
	Shell    string   // shell snippet for ActionShell (expanded)
	TmuxArgs []string // tmux args (expanded) for ActionTmux, excluding leading "tmux"
}

// Compiled is the result of compiling a spec into tmux commands.
type Compiled struct {
	Commands []Command

	// UnsafeUsed indicates compilation required an unsafe capability (shell or tmux passthrough).
	UnsafeUsed bool

	// Warnings are non-fatal.
	Warnings []string
}

// Command is a single tmux invocation.
type Command struct {
	Args        []string
	Explanation string // for dry-run / UI preview
	Unsafe      bool
}

// Compile validates and compiles the spec to tmux commands without executing.
func (e *Engine) Compile(ctx Context, spec Spec) (Compiled, error) {
	p := e.Policy
	if p.MaxActions <= 0 {
		p.MaxActions = 200
	}
	if p.MaxCommandLen <= 0 {
		p.MaxCommandLen = 4096
	}

	if ctx.SessionName == "" {
		return Compiled{}, errors.New("context: missing SessionName")
	}
	if ctx.ProjectPath == "" {
		return Compiled{}, errors.New("context: missing ProjectPath")
	}
	ctx.ProjectPath = expandUser(ctx.ProjectPath)
	if abs, err := filepath.Abs(ctx.ProjectPath); err == nil {
		ctx.ProjectPath = abs
	}

	if ctx.ProjectName == "" {
		ctx.ProjectName = filepath.Base(ctx.ProjectPath)
	}
	if ctx.WorkingDir == "" {
		ctx.WorkingDir = ctx.ProjectPath
	}

	if len(spec.Actions) == 0 {
		return Compiled{}, errors.New("spec: no actions")
	}
	if len(spec.Actions) > p.MaxActions {
		return Compiled{}, fmt.Errorf("spec: too many actions (%d > %d)", len(spec.Actions), p.MaxActions)
	}

	var out Compiled

	for i, a := range spec.Actions {
		cmds, unsafeUsed, warns, err := e.compileAction(ctx, a)
		if err != nil {
			return Compiled{}, fmt.Errorf("spec action[%d] (%s): %w", i, a.Kind, err)
		}
		out.Commands = append(out.Commands, cmds...)
		out.UnsafeUsed = out.UnsafeUsed || unsafeUsed
		out.Warnings = append(out.Warnings, warns...)
	}

	// Soft limit: validate total length of arguments for each command.
	for i, c := range out.Commands {
		total := 0
		for _, a := range c.Args {
			total += len(a) + 1
		}
		if total > p.MaxCommandLen {
			return Compiled{}, fmt.Errorf("compiled command[%d] too long (%d chars > %d)", i, total, p.MaxCommandLen)
		}
	}

	return out, nil
}

// Execute runs compiled commands via the Engine's Runner.
// If dryRun is true, it does not execute and returns the dry-run lines.
func (e *Engine) Execute(compiled Compiled, dryRun bool) ([]string, error) {
	lines := DryRunLines(compiled)
	if dryRun {
		return lines, nil
	}
	if e.Runner == nil {
		return lines, errors.New("engine: Runner is nil")
	}

	for _, c := range compiled.Commands {
		// Special-case: execution-time polling gate (safe).
		if len(c.Args) > 0 && c.Args[0] == "__wait_for_prompt__" {
			if err := e.execWaitForPrompt(c); err != nil {
				return lines, err
			}
			continue
		}

		// Special-case: structured SSH connect (safe).
		if len(c.Args) > 0 && c.Args[0] == "__ssh_manager_connect__" {
			if err := e.execSshManagerConnect(c); err != nil {
				return lines, err
			}
			continue
		}

		if err := e.Runner.Run(c.Args); err != nil {
			return lines, err
		}
	}
	return lines, nil
}

// DryRunLines returns a user-friendly list of commands and explanations.
func (e *Engine) execWaitForPrompt(c Command) error {
	if e == nil || e.Runner == nil {
		return errors.New("wait_for_prompt: missing runner")
	}
	if len(c.Args) < 6 {
		return fmt.Errorf("wait_for_prompt: invalid sentinel args: %v", c.Args)
	}
	// c.Args[0] == "__wait_for_prompt__"
	target := strings.TrimSpace(c.Args[1])
	if target == "" {
		return errors.New("wait_for_prompt: empty target")
	}

	parseInt := func(s string, def int) int {
		s = strings.TrimSpace(s)
		if s == "" {
			return def
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return def
		}
		return n
	}

	timeoutMS := parseInt(c.Args[2], 15000)
	minQuietMS := parseInt(c.Args[3], 500)
	settleMS := parseInt(c.Args[4], 250)
	maxLines := parseInt(c.Args[5], 200)

	promptRe := ""
	if len(c.Args) >= 7 {
		promptRe = strings.TrimSpace(c.Args[6])
	}
	if promptRe == "" {
		// Conservative default: match a prompt-like last line ending in common prompt chars.
		promptRe = `(?m)(^.*[#>$] ?$)`
	}

	compiled, err := regexp.Compile(promptRe)
	if err != nil {
		return fmt.Errorf("wait_for_prompt: invalid prompt_regex %q: %w", promptRe, err)
	}

	// Polling parameters.
	if timeoutMS <= 0 {
		timeoutMS = 15000
	}
	if minQuietMS < 0 {
		minQuietMS = 0
	}
	if settleMS < 0 {
		settleMS = 0
	}
	if maxLines <= 0 {
		maxLines = 200
	}
	pollEvery := 100 * time.Millisecond

	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)

	lastSnap := ""
	lastChange := time.Now()

	// Helper: capture last N lines from pane (best-effort).
	capture := func() (string, error) {
		// -p: print to stdout
		// -t: target pane
		// -S -<n>: start n lines from the bottom (negative scrollback)
		start := fmt.Sprintf("-%d", maxLines)
		out, err := e.Runner.RunOutput([]string{"capture-pane", "-p", "-t", target, "-S", start})
		if err != nil {
			return "", err
		}
		// Normalize line endings and whitespace a bit to make "quiet" detection stable.
		out = strings.ReplaceAll(out, "\r\n", "\n")
		out = strings.ReplaceAll(out, "\r", "\n")
		return strings.TrimRight(out, "\n"), nil
	}

	// Helper: last non-empty line
	lastNonEmptyLine := func(s string) string {
		lines := strings.Split(s, "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			ln := strings.TrimSpace(lines[i])
			if ln != "" {
				return ln
			}
		}
		return ""
	}

	for time.Now().Before(deadline) {
		snap, err := capture()
		if err != nil {
			// If capture-pane fails transiently, keep trying until timeout.
			time.Sleep(pollEvery)
			continue
		}

		if snap != lastSnap {
			lastSnap = snap
			lastChange = time.Now()
		}

		quietFor := time.Since(lastChange)
		if quietFor < time.Duration(minQuietMS)*time.Millisecond {
			time.Sleep(pollEvery)
			continue
		}

		lastLine := lastNonEmptyLine(snap)
		if lastLine == "" {
			time.Sleep(pollEvery)
			continue
		}

		// Ready if prompt-like last line matches.
		if compiled.MatchString(lastLine) {
			if settleMS > 0 {
				time.Sleep(time.Duration(settleMS) * time.Millisecond)
			}
			return nil
		}

		time.Sleep(pollEvery)
	}

	return fmt.Errorf("wait_for_prompt: timed out after %dms waiting for readiness in %s", timeoutMS, target)
}

func (e *Engine) execSshManagerConnect(c Command) error {
	if e == nil || e.Runner == nil {
		return errors.New("ssh_manager_connect: missing runner")
	}
	// Sentinel encoding (from compileAction):
	//   ["__ssh_manager_connect__", <target>, <host>, <user>, <port>, <login_mode>, <connect_timeout_ms>]
	if len(c.Args) < 7 {
		return fmt.Errorf("ssh_manager_connect: invalid sentinel args: %v", c.Args)
	}

	target := strings.TrimSpace(c.Args[1])
	host := strings.TrimSpace(c.Args[2])
	user := strings.TrimSpace(c.Args[3])
	portStr := strings.TrimSpace(c.Args[4])
	loginMode := strings.TrimSpace(strings.ToLower(c.Args[5]))
	_ = strings.TrimSpace(c.Args[6]) // connect_timeout_ms is handled by subsequent wait_for_prompt (if present)

	if target == "" {
		return errors.New("ssh_manager_connect: empty target")
	}
	if host == "" {
		return errors.New("ssh_manager_connect: empty host")
	}
	if loginMode == "" {
		loginMode = "askpass"
	}
	if loginMode != "askpass" && loginMode != "manual" && loginMode != "key" {
		return fmt.Errorf("ssh_manager_connect: unsupported login_mode %q", loginMode)
	}

	port := 0
	if portStr != "" {
		if n, err := strconv.Atoi(portStr); err == nil {
			port = n
		}
	}

	// SECURITY + UX:
	// - Never pass passwords through tmux send-keys.
	// - For askpass automation, delegate to tmux-ssh-manager’s internal PTY connector, which:
	//   - fetches password from macOS Keychain (service tmux-ssh-manager)
	//   - detects password prompts and injects safely
	//   - leaves you in the remote shell (so breaking `watch` returns to the prompt, not your local shell)
	//
	// This also eliminates duplicated, security-sensitive credential logic in tmux-session-manager.
	switch loginMode {
	case "askpass":
		var argv []string
		argv = append(argv, "tmux-ssh-manager", "__connect", "--host", host)
		if user != "" {
			argv = append(argv, "--user", user)
		}
		// Note: tmux-ssh-manager __connect currently does not accept --port; host aliases / ssh config should handle it.
		// If you need explicit port support here, it should be added to tmux-ssh-manager __connect first.
		_ = port

		return e.Runner.Run([]string{"send-keys", "-t", target, shellJoin(argv), "C-m"})

	case "manual", "key":
		// Resolve destination (user@host if user provided)
		dest := host
		if user != "" {
			dest = user + "@" + host
		}

		var sshParts []string
		sshParts = append(sshParts, "ssh")
		if port > 0 {
			sshParts = append(sshParts, "-p", fmt.Sprintf("%d", port))
		}
		sshParts = append(sshParts, dest)

		return e.Runner.Run([]string{"send-keys", "-t", target, shellJoin(sshParts), "C-m"})

	default:
		// Should be unreachable due to earlier validation.
		return fmt.Errorf("ssh_manager_connect: unsupported login_mode %q", loginMode)
	}
}

// NOTE:
// ssh_manager_connect no longer implements Keychain/PTY/askpass logic internally.
// Password automation is delegated to tmux-ssh-manager __connect to avoid duplication and secret leakage.

func DryRunLines(compiled Compiled) []string {
	var lines []string
	if compiled.UnsafeUsed {
		lines = append(lines, "WARNING: unsafe actions present (shell and/or tmux passthrough)")
	}
	for _, w := range compiled.Warnings {
		lines = append(lines, "WARN: "+w)
	}
	for _, c := range compiled.Commands {
		prefix := "tmux "
		if c.Unsafe {
			prefix = "tmux (unsafe) "
		}
		if c.Explanation != "" {
			lines = append(lines, fmt.Sprintf("%s# %s", prefix, c.Explanation))
		}
		lines = append(lines, prefix+shellJoin(c.Args))
	}
	return lines
}

func (e *Engine) compileAction(ctx Context, a Action) ([]Command, bool, []string, error) {
	// Default session and cwd
	session := strings.TrimSpace(a.Session)
	if session == "" {
		session = ctx.SessionName
	}
	cwd := strings.TrimSpace(a.Cwd)
	if cwd == "" {
		cwd = ctx.WorkingDir
	} else {
		cwd = expandUser(subst(ctx, cwd))
	}

	var warnings []string
	unsafe := false

	switch a.Kind {
	case ActionEnsureSession:
		// Ensure session exists: has-session || new-session -d
		// We compile as:
		//   tmux has-session -t <s>   (best-effort; if it fails, create)
		//   tmux new-session -d -s <s> -c <cwd>
		//
		// Since tmux doesn't have "ensure" semantics, callers usually do this check in code.
		// Here, we provide a deterministic sequence for dry-run; execution may fail if session exists
		// depending on environment. If you want atomicity, do the check outside the engine.
		//
		// To avoid spurious failures, we compile only new-session -d and let it fail if exists,
		// but that is noisy. Prefer check+create in higher layer.
		//
		// For now we compile a create-only and warn.
		warnings = append(warnings, "ensure_session is non-atomic in pure tmux command lists; consider pre-checking in code")
		return []Command{
			{
				Args:        []string{"new-session", "-d", "-s", session, "-c", cwd},
				Explanation: "create detached session if missing (may fail if exists)",
			},
		}, false, warnings, nil

	case ActionNewWindow:
		name := strings.TrimSpace(a.Name)
		if name == "" {
			return nil, false, nil, errors.New("new_window: missing Name")
		}
		// Use tmux new-window -t session -n name -c cwd [command]
		args := []string{"new-window", "-t", session, "-n", name, "-c", cwd}
		if strings.TrimSpace(a.Command) != "" {
			cmd := subst(ctx, a.Command)
			args = append(args, "--", "bash", "-lc", cmd)
		}
		return []Command{{Args: args, Explanation: "create window " + name}}, false, nil, nil

	case ActionSplitWindow:
		dir := strings.ToLower(strings.TrimSpace(a.Direction))
		if dir != "h" && dir != "v" {
			return nil, false, nil, errors.New("split_window: Direction must be 'h' or 'v'")
		}
		flag := "-v"
		if dir == "h" {
			flag = "-h"
		}
		target := session
		if strings.TrimSpace(a.Window) != "" {
			target = session + ":" + strings.TrimSpace(a.Window)
		}
		args := []string{"split-window", flag, "-t", target, "-c", cwd}
		if a.Percent > 0 {
			if a.Percent < 1 || a.Percent > 99 {
				return nil, false, nil, errors.New("split_window: Percent must be 1-99")
			}
			args = append(args, "-p", fmt.Sprintf("%d", a.Percent))
		}
		if strings.TrimSpace(a.Command) != "" {
			cmd := subst(ctx, a.Command)
			args = append(args, "--", "bash", "-lc", cmd)
		}
		return []Command{{Args: args, Explanation: "split window (" + dir + ")"}}, false, nil, nil

	case ActionRenameWindow:
		// Safe wrapper around: tmux rename-window -t <session>:<fromOrWindow> <newName>
		//
		// Target resolution:
		// - If a.From is set => use it as the source window identifier.
		// - Else use a.Window.
		// - If neither is set => default to "0" (first window).
		newName := strings.TrimSpace(a.Name)
		if newName == "" {
			return nil, false, nil, errors.New("rename_window: missing Name (new window name)")
		}

		from := strings.TrimSpace(a.From)
		if from == "" {
			from = strings.TrimSpace(a.Window)
		}
		if from == "" {
			from = "0"
		}

		// Allow "0" / "editor" etc.
		target := session + ":" + from
		return []Command{
			{
				Args:        []string{"rename-window", "-t", target, newName},
				Explanation: "rename window " + target + " -> " + newName,
			},
		}, false, nil, nil

	case ActionSelectWindow:
		if strings.TrimSpace(a.Window) == "" {
			return nil, false, nil, errors.New("select_window: missing Window")
		}
		target := session + ":" + strings.TrimSpace(a.Window)
		return []Command{{Args: []string{"select-window", "-t", target}, Explanation: "select window " + target}}, false, nil, nil

	case ActionSelectPane:
		if strings.TrimSpace(a.Pane) == "" {
			return nil, false, nil, errors.New("select_pane: missing Pane")
		}
		// Pane can be "%3" or "session:window.pane"
		target := strings.TrimSpace(a.Pane)
		if !strings.HasPrefix(target, "%") && !strings.Contains(target, ":") {
			// Treat as pane index in current window of the session; best-effort.
			target = session + ":." + target
		}
		return []Command{{Args: []string{"select-pane", "-t", target}, Explanation: "select pane " + target}}, false, nil, nil

	case ActionSelectLayout:
		layout := strings.TrimSpace(a.Layout)
		if layout == "" {
			return nil, false, nil, errors.New("select_layout: missing Layout")
		}
		target := session
		if strings.TrimSpace(a.Window) != "" {
			target = session + ":" + strings.TrimSpace(a.Window)
		}
		return []Command{{Args: []string{"select-layout", "-t", target, layout}, Explanation: "select layout " + layout}}, false, nil, nil

	case ActionSendKeys:
		target := session
		if strings.TrimSpace(a.Window) != "" {
			target = session + ":" + strings.TrimSpace(a.Window)
		}
		if strings.TrimSpace(a.Pane) != "" {
			// If pane is specified and looks like a pane id, prefer that.
			if strings.HasPrefix(strings.TrimSpace(a.Pane), "%") {
				target = strings.TrimSpace(a.Pane)
			} else {
				// window.pane indexing
				target = target + "." + strings.TrimSpace(a.Pane)
			}
		}

		var keys []string
		if len(a.Keys) > 0 {
			for _, k := range a.Keys {
				ks := strings.TrimSpace(subst(ctx, k))
				if ks == "" {
					continue
				}
				keys = append(keys, ks)
			}
		} else if strings.TrimSpace(a.Command) != "" {
			keys = append(keys, subst(ctx, a.Command))
		} else {
			return nil, false, nil, errors.New("send_keys: missing Keys or Command")
		}

		args := []string{"send-keys", "-t", target}
		args = append(args, keys...)
		if a.Enter {
			args = append(args, "C-m")
		}
		return []Command{{Args: args, Explanation: "send keys to " + target}}, false, nil, nil

	case ActionWaitForPrompt:
		// Execution-time polling action. We encode it as a sentinel command so Engine.Execute
		// can run a safe polling loop using tmux capture-pane without requiring shell passthrough.
		//
		// c.Args encoding:
		//   ["__wait_for_prompt__", <target>, <timeout_ms>, <min_quiet_ms>, <settle_ms>, <max_lines>, <prompt_re>]
		//
		// prompt_re may be empty.
		target := session
		if strings.TrimSpace(a.Window) != "" {
			target = session + ":" + strings.TrimSpace(a.Window)
		}
		if strings.TrimSpace(a.Pane) != "" {
			if strings.HasPrefix(strings.TrimSpace(a.Pane), "%") {
				target = strings.TrimSpace(a.Pane)
			} else {
				target = target + "." + strings.TrimSpace(a.Pane)
			}
		}

		timeoutMS := a.TimeoutMS
		if timeoutMS <= 0 {
			timeoutMS = 15000
		}
		minQuietMS := a.MinQuietMS
		if minQuietMS <= 0 {
			minQuietMS = 500
		}
		settleMS := a.SettleMS
		if settleMS <= 0 {
			settleMS = 250
		}
		maxLines := a.MaxLines
		if maxLines <= 0 {
			maxLines = 200
		}

		promptRe := strings.TrimSpace(a.PromptRe)
		return []Command{{
			Args: []string{
				"__wait_for_prompt__",
				target,
				fmt.Sprintf("%d", timeoutMS),
				fmt.Sprintf("%d", minQuietMS),
				fmt.Sprintf("%d", settleMS),
				fmt.Sprintf("%d", maxLines),
				promptRe,
			},
			Explanation: "wait for prompt (best-effort) in " + target,
		}}, false, nil, nil

	case ActionSshManagerConnect:
		// Execution-time connect action. We encode it as a sentinel command so Engine.Execute
		// can safely send a fixed ssh+askpass wrapper into the target pane.
		//
		// c.Args encoding:
		//   ["__ssh_manager_connect__", <target>, <host>, <user>, <port>, <login_mode>, <connect_timeout_ms>]
		target := session
		if strings.TrimSpace(a.Window) != "" {
			target = session + ":" + strings.TrimSpace(a.Window)
		}
		if strings.TrimSpace(a.Pane) != "" {
			if strings.HasPrefix(strings.TrimSpace(a.Pane), "%") {
				target = strings.TrimSpace(a.Pane)
			} else {
				target = target + "." + strings.TrimSpace(a.Pane)
			}
		}

		host := strings.TrimSpace(a.Host)
		if host == "" {
			return nil, false, nil, errors.New("ssh_manager_connect: missing Host")
		}
		user := strings.TrimSpace(a.User)

		login := strings.TrimSpace(strings.ToLower(a.LoginMode))
		if login == "" {
			login = "askpass"
		}

		port := a.Port
		if port < 0 {
			port = 0
		}

		cto := a.ConnectTimeoutMS
		if cto < 0 {
			cto = 0
		}

		return []Command{{
			Args: []string{
				"__ssh_manager_connect__",
				target,
				host,
				user,
				fmt.Sprintf("%d", port),
				login,
				fmt.Sprintf("%d", cto),
			},
			Explanation: "ssh_manager_connect " + host + " in " + target,
		}}, false, nil, nil

	case ActionSetOption:
		if strings.TrimSpace(a.Option) == "" {
			return nil, false, nil, errors.New("set_option: missing Option")
		}
		opt := strings.TrimSpace(a.Option)
		val := subst(ctx, a.Value)
		args := []string{"set-option"}
		if a.Global {
			args = append(args, "-g")
		} else {
			args = append(args, "-t", session)
		}
		args = append(args, opt, val)
		return []Command{{Args: args, Explanation: "set option " + opt}}, false, nil, nil

	case ActionDisplay:
		msg := subst(ctx, a.Message)
		if strings.TrimSpace(msg) == "" {
			return nil, false, nil, errors.New("display_message: missing Message")
		}
		d := a.DurationMS
		if d <= 0 {
			d = 1500
		}
		args := []string{"display-message", "-d", fmt.Sprintf("%d", d), msg}
		return []Command{{Args: args, Explanation: "display message"}}, false, nil, nil

	case ActionShell:
		if !e.Policy.AllowShell {
			return nil, false, nil, errors.New("shell action disabled by policy")
		}
		unsafe = true
		sh := strings.TrimSpace(a.Shell)
		if sh == "" {
			sh = strings.TrimSpace(a.Command)
		}
		if sh == "" {
			return nil, unsafe, nil, errors.New("shell: missing Shell/Command")
		}
		// Default approach: create a new window and run bash -lc "<shell>" so output is visible.
		// Users can use send_keys if they want it in a specific pane.
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = "shell"
		}
		sh = subst(ctx, sh)
		args := []string{"new-window", "-t", session, "-n", name, "-c", cwd, "--", "bash", "-lc", sh}
		return []Command{{Args: args, Explanation: "unsafe shell window " + name, Unsafe: true}}, true, warnings, nil

	case ActionTmux:
		if !e.Policy.AllowTmuxPassthrough {
			return nil, false, nil, errors.New("tmux passthrough disabled by policy")
		}
		unsafe = true
		if len(a.TmuxArgs) == 0 {
			return nil, unsafe, nil, errors.New("tmux: missing TmuxArgs")
		}

		args := make([]string, 0, len(a.TmuxArgs))
		for _, t := range a.TmuxArgs {
			args = append(args, subst(ctx, t))
		}

		// Validate subcommand allowlist.
		sub := strings.TrimSpace(args[0])
		sub = strings.TrimPrefix(sub, "tmux ")
		sub = strings.Fields(sub)[0]
		if sub == "" {
			return nil, unsafe, nil, errors.New("tmux: empty subcommand")
		}
		if e.Policy.DisallowTmuxCommands != nil && e.Policy.DisallowTmuxCommands[sub] {
			return nil, unsafe, nil, fmt.Errorf("tmux: subcommand %q is disallowed", sub)
		}
		allow := e.Policy.AllowedTmuxCommands
		if len(allow) == 0 {
			allow = defaultAllowedTmuxCommands()
		}
		if !allow[sub] {
			return nil, unsafe, nil, fmt.Errorf("tmux: subcommand %q not in allowlist", sub)
		}

		return []Command{{Args: args, Explanation: "unsafe tmux passthrough", Unsafe: true}}, true, warnings, nil

	default:
		return nil, false, nil, fmt.Errorf("unknown action kind %q", a.Kind)
	}
}

// subst replaces ${VARS} in a string using Context + environment.
// Supports ${VAR} and ${VAR:-default}.
// Known builtins: PROJECT_NAME, PROJECT_PATH, SESSION_NAME, TMUX_SOCK.
func subst(ctx Context, s string) string {
	return expandVars(s, func(key, def string) string {
		switch key {
		case "PROJECT_NAME":
			if ctx.ProjectName != "" {
				return ctx.ProjectName
			}
		case "PROJECT_PATH":
			if ctx.ProjectPath != "" {
				return ctx.ProjectPath
			}
		case "SESSION_NAME":
			if ctx.SessionName != "" {
				return ctx.SessionName
			}
		case "TMUX_SOCK":
			if ctx.TmuxSocket != "" {
				return ctx.TmuxSocket
			}
		}
		if ctx.Env != nil {
			if v, ok := ctx.Env[key]; ok && v != "" {
				return v
			}
		}
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	})
}

var reVar = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-(.*?))?\}`)

func expandVars(s string, lookup func(key, def string) string) string {
	if s == "" {
		return s
	}
	return reVar.ReplaceAllStringFunc(s, func(m string) string {
		sub := reVar.FindStringSubmatch(m)
		if len(sub) < 2 {
			return m
		}
		key := sub[1]
		def := ""
		if len(sub) >= 3 {
			def = sub[2]
		}
		return lookup(key, def)
	})
}

func expandUser(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return p
		}
		if p == "~" {
			return home
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

func shellJoin(args []string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	needs := false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '"', '\'', '\\', '$', '`', '&', '|', ';', '<', '>', '(', ')', '{', '}', '*', '?', '!', '~', '#':
			needs = true
		}
		if needs {
			break
		}
	}
	if !needs {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\"'\"'`) + "'"
}

// HashPath returns a short stable hash for a project path. Useful for name collision avoidance.
func HashPath(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:])[:10]
}
