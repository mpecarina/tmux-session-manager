package templates

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"tmux-session-manager/pkg/spec"
)

// This file provides the glue between the formal project-local spec schema (pkg/spec)
// and the template engine (pkg/templates).
//
// Responsibilities:
//   - Convert spec.Spec (windows/panes/actions) -> templates.Spec (linear action list)
//   - Enforce policy (shell disabled by default; tmux passthrough allowlisted)
//   - Provide a single entrypoint for higher layers (TUI/CLI) to compile a project-local spec
//     into tmux commands with a clear safety model.
//
// Notes:
//   - The TUI/CLI should be responsible for:
//       * selecting the project directory
//       * deriving/choosing a session name (or using spec.Session.Name)
//       * ensuring the base session exists (or allow Engine action EnsureSession)
//       * switching/attaching after execution
//   - This converter tries to be deterministic and explicit. It does not attempt to infer
//     complex split graphs beyond a simple "first pane + sequential splits" model.

// FromSpec converts a formal spec.Spec into an Engine-compatible templates.Spec for the provided Context.
// It is a convenience wrapper around BuildFromSpec that keeps call sites (TUI/CLI) thin.
//
// Safety / policy:
// - Shell actions remain opt-in (allowShell must be true, and the spec must pass ValidatePolicy).
// - Raw tmux passthrough remains opt-in (allowTmuxPassthrough must be true, and allowlist rules apply).
//
// includeEnsureSession controls whether the plan begins with an ActionEnsureSession.
// If false, the caller is expected to create the session before executing the compiled plan.
//
// This helper does NOT execute tmux; use Engine.Compile + Engine.Execute for that.
func FromSpec(
	ctx Context,
	s spec.Spec,
	allowShell bool,
	allowTmuxPassthrough bool,
	includeEnsureSession bool,
) (Spec, error) {
	projectPath := strings.TrimSpace(ctx.ProjectPath)
	if projectPath == "" {
		return Spec{}, errors.New("context: missing ProjectPath")
	}

	// Derive project name if not provided.
	projectName := strings.TrimSpace(ctx.ProjectName)
	if projectName == "" {
		projectName = filepath.Base(strings.TrimRight(projectPath, string(filepath.Separator)))
	}

	// If caller didn't provide a session name, derive from spec + project path.
	sessionName := strings.TrimSpace(ctx.SessionName)
	if sessionName == "" {
		if strings.TrimSpace(s.Session.Name) != "" {
			sessionName = s.Session.Name
		} else {
			sessionName = spec.DeriveSessionName(strings.TrimSpace(s.Session.Prefix), projectPath)
		}
	}

	// Ensure env exists for substitution (Engine.subst uses ctx.Env plus process env).
	// We do not mutate the caller's context; BuildFromSpec clones the map from spec.Env.
	if ctx.Env == nil && s.Env != nil {
		ctx.Env = s.Env
	}

	opt := BuildOptions{
		ProjectRoot: projectPath,
		ProjectName: projectName,
		SessionName: sessionName,

		PreferWindows:        true,
		IncludeEnsureSession: includeEnsureSession,

		AllowShell:           allowShell,
		AllowTmuxPassthrough: allowTmuxPassthrough,
	}

	_, tpl, _, err := BuildFromSpec(&s, opt)
	if err != nil {
		return Spec{}, err
	}
	return tpl, nil
}

// BuildOptions controls conversion and policy enforcement.
type BuildOptions struct {
	// ProjectRoot is the absolute (or user-expanded) path to the project root directory.
	ProjectRoot string

	// ProjectName is optional; if empty, derived from basename(ProjectRoot).
	ProjectName string

	// SessionName is the session name the executor intends to target.
	// If empty, we derive from spec.Session (Prefix/Name) and project root.
	SessionName string

	// PreferWindows, when true, will prefer Spec.Windows representation over Spec.Actions if both exist.
	// If false, Actions takes precedence when provided.
	PreferWindows bool

	// Allow creating "ensure session" action at the beginning of the plan.
	// If false, we assume the caller already created the session.
	IncludeEnsureSession bool

	// Safety policy gates (opt-in).
	AllowShell           bool
	AllowTmuxPassthrough bool

	// AllowedTmuxCommands allows customizing the tmux passthrough allowlist. If nil/empty,
	// defaults to Engine.DefaultPolicy allowlist.
	AllowedTmuxCommands map[string]bool

	// DisallowTmuxCommands can further deny commands.
	DisallowTmuxCommands map[string]bool
}

// BuildFromSpec converts a formal spec.Spec into a templates.Spec and a templates.Context,
// performing policy enforcement and normalization along the way.
//
// It returns:
//   - ctx: substitution context for ${VARS}
//   - tpl: templates.Spec (linear action list)
//   - unsafeRequired: true if the spec contains Shell or Tmux passthrough actions
func BuildFromSpec(s *spec.Spec, opt BuildOptions) (ctx Context, tpl Spec, unsafeRequired bool, err error) {
	if s == nil {
		return Context{}, Spec{}, false, errors.New("spec is nil")
	}

	// Structural validation (version, shapes). Policy validation happens below.
	if err := s.Validate(); err != nil {
		return Context{}, Spec{}, false, err
	}

	projectRoot := strings.TrimSpace(opt.ProjectRoot)
	if projectRoot == "" {
		return Context{}, Spec{}, false, errors.New("BuildOptions.ProjectRoot is required")
	}
	projectRoot = expandUser(projectRoot)

	projectName := strings.TrimSpace(opt.ProjectName)
	if projectName == "" {
		projectName = filepath.Base(strings.TrimRight(projectRoot, string(filepath.Separator)))
	}

	// Decide final session name:
	// - explicit BuildOptions.SessionName wins
	// - else spec.Session.Name wins
	// - else derived from spec.Session.Prefix + project basename
	sessionName := strings.TrimSpace(opt.SessionName)
	if sessionName == "" {
		if strings.TrimSpace(s.Session.Name) != "" {
			// Spec session name may contain variables; allow resolution at compile-time.
			// We still sanitize it to be tmux-safe-ish.
			sessionName = strings.TrimSpace(s.Session.Name)
		} else {
			sessionName = spec.DeriveSessionName(strings.TrimSpace(s.Session.Prefix), projectRoot)
		}
	}
	sessionName = sanitizeSessionName(sessionName)
	if sessionName == "" {
		return Context{}, Spec{}, false, errors.New("resolved session name is empty")
	}

	// Resolve root (cwd) for session:
	root := strings.TrimSpace(s.Session.Root)
	if root == "" {
		root = projectRoot
	} else {
		root = expandUser(root)
	}

	// Policy: default allowlist if not supplied.
	allowed := opt.AllowedTmuxCommands
	if len(allowed) == 0 {
		allowed = DefaultPolicy().AllowedTmuxCommands
	}
	disallowed := opt.DisallowTmuxCommands
	if disallowed == nil {
		disallowed = DefaultPolicy().DisallowTmuxCommands
	}

	pol := spec.DefaultPolicy()
	pol.AllowShell = opt.AllowShell
	pol.AllowTmuxPassthrough = opt.AllowTmuxPassthrough
	pol.AllowedTmuxCommands = allowed

	// Validate policy for the spec as-declared.
	if err := s.ValidatePolicy(pol); err != nil {
		return Context{}, Spec{}, false, err
	}

	// Establish substitution context. spec.Env becomes ctx.Env.
	// Built-ins are supported by Engine.subst(): PROJECT_NAME/PROJECT_PATH/SESSION_NAME/TMUX_SOCK
	ctx = Context{
		ProjectName: projectName,
		ProjectPath: projectRoot,
		SessionName: sessionName,
		WorkingDir:  root,
		Env:         cloneStringMap(s.Env),
		TmuxSocket:  "",
	}

	// Convert spec -> templates.Spec
	tpl = Spec{
		Version: s.Version,
		ID:      "",
		Name:    firstNonEmpty(s.Name, projectName),
		Unsafe:  false,
	}

	// Track whether spec uses unsafe actions.
	unsafeRequired = false

	// Optional: include base session options early (safe tmux commands)
	// We keep these in the plan so preview/dry-run includes them, but they can be disabled by callers.
	if opt.IncludeEnsureSession {
		tpl.Actions = append(tpl.Actions, Action{
			Kind:    ActionEnsureSession,
			Session: sessionName,
			Cwd:     root,
		})
	}

	// Apply base index options if provided.
	if s.Session.BaseIndex != nil {
		tpl.Actions = append(tpl.Actions, Action{
			Kind:   ActionSetOption,
			Global: true,
			Option: "base-index",
			Value:  fmt.Sprintf("%d", *s.Session.BaseIndex),
		})
	}
	if s.Session.PaneBaseIndex != nil {
		tpl.Actions = append(tpl.Actions, Action{
			Kind:   ActionSetOption,
			Global: true,
			Option: "pane-base-index",
			Value:  fmt.Sprintf("%d", *s.Session.PaneBaseIndex),
		})
	}

	// Choose representation: Actions (script-like) or Windows (declarative).
	useActions := len(s.Actions) > 0
	if opt.PreferWindows && len(s.Windows) > 0 {
		useActions = false
	}

	if useActions {
		acts, usedUnsafe, err := convertActions(ctx, sessionName, s.Actions, pol, disallowed)
		if err != nil {
			return Context{}, Spec{}, false, err
		}
		unsafeRequired = unsafeRequired || usedUnsafe
		tpl.Actions = append(tpl.Actions, acts...)
	} else {
		acts, usedUnsafe, err := convertWindows(ctx, sessionName, root, s.Windows, pol, disallowed)
		if err != nil {
			return Context{}, Spec{}, false, err
		}
		unsafeRequired = unsafeRequired || usedUnsafe
		tpl.Actions = append(tpl.Actions, acts...)

		// Session-scoped final window focus (safe):
		//
		// Compile session.focus_window into a final select-window action when using the windows
		// representation. This lets users choose the final active window without enabling tmux
		// passthrough.
		//
		// Semantics:
		// - session.focus_window: "active" => no-op
		// - session.focus_window: "<n>" or "<name>" => select-window -t "<session>:<value>"
		fw := strings.TrimSpace(strings.ToLower(s.Session.FocusWindow))
		if fw != "" && fw != "active" {
			tpl.Actions = append(tpl.Actions, Action{
				Kind:    ActionSelectWindow,
				Session: sessionName,
				Window:  strings.TrimSpace(s.Session.FocusWindow),
			})
		}
	}

	tpl.Unsafe = unsafeRequired
	return ctx, tpl, unsafeRequired, nil
}

// NewEngineFromSpecPolicy builds an Engine.Policy from spec.Policy-style runtime allowances.
// This is used by callers that want the Engine to enforce the same allowlist/denylist.
func NewEngineFromSpecPolicy(allowShell, allowTmux bool, allowed map[string]bool, disallowed map[string]bool) Policy {
	p := DefaultPolicy()
	p.AllowShell = allowShell
	p.AllowTmuxPassthrough = allowTmux
	if len(allowed) > 0 {
		p.AllowedTmuxCommands = allowed
	}
	if disallowed != nil {
		p.DisallowTmuxCommands = disallowed
	}
	return p
}

// -------------------------
// Conversion: Spec.Actions
// -------------------------

func convertActions(ctx Context, sessionName string, actions []spec.Action, pol spec.Policy, disallowed map[string]bool) ([]Action, bool, error) {
	var out []Action
	unsafeUsed := false

	for i, a := range actions {
		kind, act, usedUnsafe, err := convertSingleAction(ctx, sessionName, a, pol, disallowed)
		if err != nil {
			return nil, false, fmt.Errorf("actions[%d] (%s): %w", i, kind, err)
		}
		unsafeUsed = unsafeUsed || usedUnsafe
		out = append(out, act...)
	}

	return out, unsafeUsed, nil
}

func convertSingleAction(ctx Context, defaultSession string, a spec.Action, pol spec.Policy, disallowed map[string]bool) (string, []Action, bool, error) {
	// Determine target session/window/pane (best-effort)
	sess := strings.TrimSpace(a.Target.Session)
	if sess == "" {
		sess = defaultSession
	}

	switch a.Type {
	case "send_keys":
		if a.SendKeys == nil {
			return "send_keys", nil, false, errors.New("missing send_keys{}")
		}
		targetWin := strings.TrimSpace(a.Target.Window)
		targetPane := strings.TrimSpace(a.Target.Pane)

		act := Action{
			Kind:    ActionSendKeys,
			Session: sess,
			Window:  targetWin,
			Pane:    targetPane,
			Keys:    append([]string(nil), a.SendKeys.Keys...),
			Enter:   a.SendKeys.Enter,
		}
		return "send_keys", []Action{act}, false, nil

	case "run":
		if a.Run == nil {
			return "run", nil, false, errors.New("missing run{}")
		}
		// We model run as send-keys of a shell command line for MVP.
		argv := append([]string{a.Run.Program}, a.Run.Args...)
		cmdLine := shellJoin(argv)
		enter := true
		if a.Run.Enter != nil {
			enter = *a.Run.Enter
		}
		act := Action{
			Kind:    ActionSendKeys,
			Session: sess,
			Window:  strings.TrimSpace(a.Target.Window),
			Pane:    strings.TrimSpace(a.Target.Pane),
			Command: cmdLine,
			Enter:   enter,
		}
		return "run", []Action{act}, false, nil

	case "ssh_manager_connect":
		if a.SshManagerConnect == nil {
			return "ssh_manager_connect", nil, false, errors.New("missing ssh_manager_connect{}")
		}
		act := Action{
			Kind:             ActionSshManagerConnect,
			Session:          sess,
			Window:           strings.TrimSpace(a.Target.Window),
			Pane:             strings.TrimSpace(a.Target.Pane),
			Host:             strings.TrimSpace(a.SshManagerConnect.Host),
			User:             strings.TrimSpace(a.SshManagerConnect.User),
			Port:             a.SshManagerConnect.Port,
			LoginMode:        strings.TrimSpace(strings.ToLower(a.SshManagerConnect.LoginMode)),
			ConnectTimeoutMS: a.SshManagerConnect.ConnectTimeoutMS,
		}
		return "ssh_manager_connect", []Action{act}, false, nil

	case "wait_for_prompt":
		if a.WaitForPrompt == nil {
			return "wait_for_prompt", nil, false, errors.New("missing wait_for_prompt{}")
		}
		act := Action{
			Kind:       ActionWaitForPrompt,
			Session:    sess,
			Window:     strings.TrimSpace(a.Target.Window),
			Pane:       strings.TrimSpace(a.Target.Pane),
			TimeoutMS:  a.WaitForPrompt.TimeoutMS,
			MinQuietMS: a.WaitForPrompt.MinQuietMS,
			SettleMS:   a.WaitForPrompt.SettleMS,
			PromptRe:   strings.TrimSpace(a.WaitForPrompt.PromptRegex),
			MaxLines:   a.WaitForPrompt.MaxLines,
		}
		return "wait_for_prompt", []Action{act}, false, nil

	case "watch":
		if a.Watch == nil {
			return "watch", nil, false, errors.New("missing watch{}")
		}
		interval := a.Watch.IntervalSeconds
		if interval <= 0 {
			interval = 2
		}
		cmd := strings.TrimSpace(a.Watch.Command)
		if cmd == "" {
			return "watch", nil, false, errors.New("watch.command empty")
		}
		wrapped := fmt.Sprintf("watch -n %d -t -- %s", interval, cmd)

		act := Action{
			Kind:    ActionSendKeys,
			Session: sess,
			Window:  strings.TrimSpace(a.Target.Window),
			Pane:    strings.TrimSpace(a.Target.Pane),
			Command: wrapped,
			Enter:   true,
		}
		return "watch", []Action{act}, false, nil

	case "shell":
		if a.Shell == nil {
			return "shell", nil, false, errors.New("missing shell{}")
		}
		if !pol.AllowShell {
			return "shell", nil, false, errors.New("shell actions disabled by policy")
		}
		cmd := strings.TrimSpace(a.Shell.Cmd)
		if cmd == "" {
			return "shell", nil, false, errors.New("shell.cmd empty")
		}
		// For simplicity, treat as a send-keys in targeted pane/window.
		act := Action{
			Kind:    ActionShell,
			Session: sess,
			Window:  strings.TrimSpace(a.Target.Window),
			Pane:    strings.TrimSpace(a.Target.Pane),
			Shell:   cmd,
		}
		return "shell", []Action{act}, true, nil

	case "tmux":
		if a.Tmux == nil {
			return "tmux", nil, false, errors.New("missing tmux{}")
		}
		if !pol.AllowTmuxPassthrough {
			return "tmux", nil, false, errors.New("tmux passthrough disabled by policy")
		}
		cmd := strings.TrimSpace(a.Tmux.Name)
		if cmd == "" {
			return "tmux", nil, false, errors.New("tmux.name empty")
		}
		if disallowed != nil && disallowed[cmd] {
			return "tmux", nil, false, fmt.Errorf("tmux subcommand %q is disallowed", cmd)
		}
		if pol.AllowedTmuxCommands != nil && !pol.AllowedTmuxCommands[cmd] {
			return "tmux", nil, false, fmt.Errorf("tmux subcommand %q not in allowlist", cmd)
		}

		args := append([]string{cmd}, a.Tmux.Args...)
		act := Action{
			Kind:     ActionTmux,
			Session:  sess,
			Window:   strings.TrimSpace(a.Target.Window),
			Pane:     strings.TrimSpace(a.Target.Pane),
			TmuxArgs: args,
		}
		return "tmux", []Action{act}, true, nil

	case "sleep":
		if a.Sleep == nil {
			return "sleep", nil, false, errors.New("missing sleep{}")
		}
		// Engine does not have a native sleep action; encode as a shell sleep (still unsafe).
		// We treat it as safe-ish if AllowShell=true, otherwise reject.
		if !pol.AllowShell {
			return "sleep", nil, false, errors.New("sleep requires shell enabled (policy)")
		}
		ms := a.Sleep.Milliseconds
		if ms < 0 {
			return "sleep", nil, false, errors.New("sleep.ms must be >= 0")
		}
		cmd := fmt.Sprintf("sleep %.3f", float64(ms)/1000.0)
		act := Action{
			Kind:    ActionShell,
			Session: sess,
			Window:  strings.TrimSpace(a.Target.Window),
			Pane:    strings.TrimSpace(a.Target.Pane),
			Shell:   cmd,
		}
		return "sleep", []Action{act}, true, nil

	default:
		return a.Type, nil, false, fmt.Errorf("unknown action type %q", a.Type)
	}
}

// --------------------------
// Conversion: Spec.Windows[]
// --------------------------

func convertWindows(ctx Context, sessionName string, sessionRoot string, windows []spec.Window, pol spec.Policy, disallowed map[string]bool) ([]Action, bool, error) {
	if len(windows) == 0 {
		return nil, false, errors.New("no windows in spec")
	}

	// Note: tmux will often create a default window when the session is created externally
	// (e.g., via `tmux new-session -d ...`), and base-index can be 0 or 1 depending on user config.
	//
	// We intentionally do NOT attempt to clean up/kill the default window here because doing so
	// reliably would require either:
	// - tmux passthrough (which is opt-in via AllowTmuxPassthrough), or
	// - runtime inspection (list-windows) that doesn't fit the purely declarative compilation phase.
	//
	// If you want the spec to "own" the session cleanly (only editor/dev/etc.), implement that
	// cleanup in the CLI apply path (where we can safely inspect and kill by actual window id/index).
	var out []Action
	unsafeUsed := false

	for wi, w := range windows {
		if strings.TrimSpace(w.Name) == "" {
			return nil, false, fmt.Errorf("windows[%d]: missing name", wi)
		}

		winRoot := strings.TrimSpace(w.Root)
		if winRoot == "" {
			winRoot = sessionRoot
		}
		winRoot = expandUser(subst(ctx, winRoot))

		// Window creation strategy:
		// - Always create spec windows explicitly via new-window -n <name>.
		//   This avoids relying on an initial session window index (base-index can be 0 or 1),
		//   and avoids rename-window against a window that may not exist yet in some tmux setups.
		out = append(out, Action{
			Kind:    ActionNewWindow,
			Session: sessionName,
			Name:    w.Name,
			Cwd:     winRoot,
		})

		// Ensure the newly created window is selected before any subsequent pane actions.
		// This makes send-keys/splits deterministic (they target a known window by name).
		out = append(out, Action{
			Kind:    ActionSelectWindow,
			Session: sessionName,
			Window:  w.Name,
		})

		// Apply window-scoped actions (rare; support basic subset)
		if len(w.Actions) > 0 {
			acts, usedUnsafe, err := convertActions(ctx, sessionName, w.Actions, pol, disallowed)
			if err != nil {
				return nil, false, fmt.Errorf("window %q actions: %w", w.Name, err)
			}
			unsafeUsed = unsafeUsed || usedUnsafe
			// Ensure we target this window by name for these actions (best-effort)
			for i := range acts {
				if acts[i].Window == "" {
					acts[i].Window = w.Name
				}
				if acts[i].Session == "" {
					acts[i].Session = sessionName
				}
			}
			out = append(out, acts...)
		}

		// Pane creation strategy:
		// - Prefer PanePlan when present (encodes split geometry safely).
		// - Otherwise fall back to the simple sequential panes[] behavior.
		if len(w.PanePlan) > 0 {
			planActs, usedUnsafe, err := convertPanePlan(ctx, sessionName, w, winRoot, pol, disallowed)
			if err != nil {
				return nil, false, err
			}
			unsafeUsed = unsafeUsed || usedUnsafe
			out = append(out, planActs...)
		} else if len(w.Panes) > 0 {
			// Panes: simple sequential split model (legacy).
			for pi, p := range w.Panes {
				paneRoot := strings.TrimSpace(p.Root)
				if paneRoot == "" {
					paneRoot = winRoot
				}
				paneRoot = expandUser(subst(ctx, paneRoot))

				if pi == 0 {
					// First pane exists. If paneRoot differs, we can send a cd.
					if paneRoot != "" && paneRoot != winRoot {
						out = append(out, Action{
							Kind:    ActionSendKeys,
							Session: sessionName,
							Window:  w.Name,
							Command: "cd " + shellQuote(paneRoot),
							Enter:   true,
						})
					}
				} else {
					// Split from active pane; default direction is horizontal for legacy list.
					out = append(out, Action{
						Kind:      ActionSplitWindow,
						Session:   sessionName,
						Window:    w.Name,
						Direction: "h",
						Cwd:       paneRoot,
					})
				}

				// Pane shorthand: Pane.Command already normalized by spec.Validate() into a Shell action.
				// Convert pane actions (run/send_keys/shell/tmux).
				if len(p.Actions) > 0 {
					acts, usedUnsafe, err := convertActions(ctx, sessionName, p.Actions, pol, disallowed)
					if err != nil {
						return nil, false, fmt.Errorf("window %q pane[%d] actions: %w", w.Name, pi, err)
					}
					unsafeUsed = unsafeUsed || usedUnsafe

					// Deterministic targeting: if an action doesn't specify a window, default it to the
					// current spec window name so send-keys/splits don't accidentally target another window.
					for i := range acts {
						if strings.TrimSpace(acts[i].Session) == "" {
							acts[i].Session = sessionName
						}
						if strings.TrimSpace(acts[i].Window) == "" {
							acts[i].Window = w.Name
						}
					}

					out = append(out, acts...)
				}

				// Pane focus:
				// Do NOT assume pane index 0. Users commonly set `pane-base-index` to 1, and tmux
				// also varies indices depending on options. Since our specs already set the active
				// pane deterministically during pane_plan execution, the safest "focus" we can do
				// without querying pane IDs is to ensure the correct window is selected.
				if p.Focus {
					out = append(out, Action{
						Kind:    ActionSelectWindow,
						Session: sessionName,
						Window:  w.Name,
					})
				}
			}
		}

		// Layout
		if strings.TrimSpace(w.Layout) != "" {
			out = append(out, Action{
				Kind:    ActionSelectLayout,
				Session: sessionName,
				Window:  w.Name,
				Layout:  w.Layout,
			})
		}

		// Window focus
		if w.Focus {
			out = append(out, Action{
				Kind:    ActionSelectWindow,
				Session: sessionName,
				Window:  w.Name,
			})
		}

		// Deterministic post-plan focus (safe):
		//
		// If the spec requests a specific pane index to be focused after the window is created,
		// compile it into a select-pane action. This avoids requiring users to enable tmux
		// passthrough just to choose the final focused pane.
		//
		// Semantics:
		// - focus_pane: "active" => no-op
		// - focus_pane: "<n>"    => select-pane -t "<session>:<window>.<n>"
		//
		// NOTE: This targets the pane index as expressed in the spec. Users who set
		// `pane-base-index 1` should use "1" for the first pane.
		fp := strings.TrimSpace(strings.ToLower(w.FocusPane))
		if fp != "" && fp != "active" {
			out = append(out, Action{
				Kind:    ActionSelectPane,
				Session: sessionName,
				Window:  w.Name,
				Pane:    fp,
			})
		}
	}

	// Unsafe usage is determined by presence of escape hatches.
	unsafeUsed = unsafeUsed || containsKind(out, ActionTmux) || containsKind(out, ActionShell)

	return out, unsafeUsed, nil
}

func containsKind(actions []Action, kind ActionKind) bool {
	for _, a := range actions {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

// convertPanePlan converts a declarative pane_plan into explicit split/run actions without requiring
// raw tmux passthrough.
//
// The plan is interpreted left-to-right and relies on tmux behavior that split-window makes the
// new pane active. This matches our MVP executor strategy.
//
// Safety:
// - PanePlan itself is safe and declarative.
// - Any embedded spec.Action still goes through convertActions(), which enforces policy.
func convertPanePlan(
	ctx Context,
	sessionName string,
	w spec.Window,
	winRoot string,
	pol spec.Policy,
	disallowed map[string]bool,
) ([]Action, bool, error) {
	var out []Action
	unsafeUsed := false

	// Active pane context starts at first pane. We interpret "split" steps as splitting the active pane,
	// and the following "pane" step describes the newly created pane content.
	//
	// spec.Validate() ensures the first step is pane and last is pane.
	for i, step := range w.PanePlan {
		if step.Pane != nil {
			p := step.Pane
			paneRoot := strings.TrimSpace(p.Root)
			if paneRoot == "" {
				paneRoot = winRoot
			}
			paneRoot = expandUser(subst(ctx, paneRoot))

			// For the first pane: optionally cd if different from window root.
			if i == 0 && paneRoot != "" && paneRoot != winRoot {
				out = append(out, Action{
					Kind:    ActionSendKeys,
					Session: sessionName,
					Window:  w.Name,
					Command: "cd " + shellQuote(paneRoot),
					Enter:   true,
				})
			}

			// Pane shorthand Command is normalized to a shell action by spec.Validate(), so we only need to
			// convert pane actions.
			if len(p.Actions) > 0 {
				acts, usedUnsafe, err := convertActions(ctx, sessionName, p.Actions, pol, disallowed)
				if err != nil {
					return nil, false, fmt.Errorf("window %q pane_plan[%d].pane actions: %w", w.Name, i, err)
				}
				unsafeUsed = unsafeUsed || usedUnsafe

				// Deterministic targeting: if an action doesn't specify a window, default it to the
				// current spec window name so send-keys/splits don't accidentally target another window.
				for i := range acts {
					if strings.TrimSpace(acts[i].Session) == "" {
						acts[i].Session = sessionName
					}
					if strings.TrimSpace(acts[i].Window) == "" {
						acts[i].Window = w.Name
					}
				}

				out = append(out, acts...)
			}

			if p.Focus {
				// See note above: avoid selecting a hardcoded pane index (0) because it breaks with
				// `pane-base-index 1`. Selecting the window is sufficient for most workflows, and
				// the active pane at this point is already the pane we just created/last touched.
				out = append(out, Action{
					Kind:    ActionSelectWindow,
					Session: sessionName,
					Window:  w.Name,
				})
			}

			continue
		}

		if step.Split != nil {
			s := step.Split
			dir := strings.ToLower(strings.TrimSpace(s.Direction))
			if dir != "h" && dir != "v" {
				return nil, false, fmt.Errorf("window %q pane_plan[%d].split.direction must be 'h' or 'v'", w.Name, i)
			}

			// Size: support "NN%" (percent) and "NN" (absolute) by mapping to Percent when possible.
			percent := 0
			size := strings.TrimSpace(s.Size)
			if size != "" && strings.HasSuffix(size, "%") {
				num := strings.TrimSuffix(size, "%")
				// best-effort parse
				pv := 0
				for _, r := range num {
					if r < '0' || r > '9' {
						pv = 0
						break
					}
					pv = pv*10 + int(r-'0')
				}
				if pv > 0 && pv < 100 {
					percent = pv
				}
			}

			out = append(out, Action{
				Kind:      ActionSplitWindow,
				Session:   sessionName,
				Window:    w.Name,
				Direction: dir,
				Cwd:       winRoot,
				Percent:   percent,
			})
			continue
		}

		// Should be unreachable due to spec.Validate() guarantees.
		return nil, false, fmt.Errorf("window %q pane_plan[%d]: invalid step (expected pane or split)", w.Name, i)
	}

	return out, unsafeUsed, nil
}

// -------------------------
// Helpers
// -------------------------

func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func firstNonEmpty(a, b string) string {
	a = strings.TrimSpace(a)
	if a != "" {
		return a
	}
	return strings.TrimSpace(b)
}

func sanitizeSessionName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	// mirror spec.ValidateTmuxName but allow variables already substituted; keep it conservative.
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastUnderscore = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			lastUnderscore = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		case r == '-' || r == '_':
			b.WriteRune('_')
			lastUnderscore = true
		default:
			if !lastUnderscore {
				b.WriteRune('_')
				lastUnderscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return ""
	}
	return out
}
