package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config contains runtime configuration resolved from (in priority order):
//  1. Explicit CLI flags (wired in cmd layer)
//  2. Environment variables
//  3. tmux options (queried by launcher and passed via env, or by future in-proc tmux option lookup)
//  4. Defaults
//
// This package intentionally does NOT talk to tmux directly; tmux option lookup should be done in the
// launcher script (consistent with tmux-ssh-manager patterns) and passed via env.
//
// Security model:
//
// By default, project-local specs (.tmux-session.yaml/.json) are treated as untrusted input and may
// only use "safe" actions (tmux primitives + send-keys without shell evaluation). Any action that
// runs arbitrary shell is disabled unless explicitly enabled via env/tmux option.
//
// This package captures those toggles + allowlists so other layers can enforce them.
type Config struct {
	// LaunchMode is a hint for UI tweaks; the launcher is responsible for actually running popup/window.
	// Allowed: "window", "popup".
	LaunchMode string

	// Project discovery roots (comma-separated in env).
	ProjectRoots []string

	// ProjectScanDepth controls recursive scanning depth under each root. 0 means default.
	ProjectScanDepth int

	// IgnoreDirNames excludes directories by basename while scanning.
	IgnoreDirNames []string

	// Spec file names accepted within a project directory.
	// Defaults: [".tmux-session.yaml", ".tmux-session.yml", ".tmux-session.json"].
	SpecFilenames []string

	// PreferProjectLocalSpec, when true, will use a project-local spec file if present. Default: true.
	PreferProjectLocalSpec bool

	// Allowlist + unsafe execution controls
	Safety Safety

	// Defaults used when spec does not define certain fields.
	Defaults Defaults

	// Logging/debug flags
	Debug bool

	// Timeouts for operations that invoke tmux.
	CommandTimeout time.Duration
}

// Safety governs what kinds of actions are allowed when applying specs/templates.
type Safety struct {
	// AllowShell, when true, enables shell execution actions (arbitrary commands) in specs/templates.
	// Default: false.
	AllowShell bool

	// AllowTmuxPassthrough, when true, allows raw tmux subcommand passthrough actions in specs/templates.
	// Default: false.
	AllowTmuxPassthrough bool

	// AllowedTmuxCommands is an allowlist for tmux subcommands when passthrough is enabled.
	// If empty, a conservative default allowlist is used.
	AllowedTmuxCommands []string

	// DeniedTmuxCommands always denies these tmux subcommands even if allowlisted.
	// This should include particularly risky commands by default.
	DeniedTmuxCommands []string

	// AllowedShellPrefixes optionally restricts arbitrary shell commands to those that begin with
	// one of these prefixes (trimmed). If empty and AllowShell==true, any shell command is allowed.
	// Example: ["npm ", "pnpm ", "yarn ", "go ", "python ", "pytest "].
	AllowedShellPrefixes []string
}

// Defaults are values applied when a spec omits fields.
type Defaults struct {
	// DefaultTemplate is used when no project-local spec exists and template inference is inconclusive.
	// Common: "empty", "node", "python", "go", "auto".
	DefaultTemplate string

	// EditorCmd is used by built-in templates/specs that reference an editor.
	EditorCmd string

	// ShellCmd is the fallback interactive shell command for panes/windows.
	ShellCmd string

	// SessionPrefix is used when generating session names from project dirs (if enabled by higher layers).
	SessionPrefix string
}

// EnvKeys groups supported env variables.
type EnvKeys struct {
	LaunchMode    string
	Roots         string
	Depth         string
	IgnoreDirs    string
	SpecNames     string
	PreferSpec    string
	Debug         string
	TimeoutMs     string
	EditorCmd     string
	ShellCmd      string
	DefaultTpl    string
	SessionPrefix string

	AllowShell           string
	AllowTmuxPassthrough string
	AllowedTmuxCommands  string
	DeniedTmuxCommands   string
	AllowedShellPrefixes string
}

// DefaultEnvKeys returns the canonical env variable names.
// These are designed to be stable when the project is extracted into its own repo.
func DefaultEnvKeys() EnvKeys {
	return EnvKeys{
		LaunchMode:    "TMUX_SESSION_MANAGER_LAUNCH_MODE",
		Roots:         "TMUX_SESSION_MANAGER_ROOTS",
		Depth:         "TMUX_SESSION_MANAGER_PROJECT_DEPTH",
		IgnoreDirs:    "TMUX_SESSION_MANAGER_IGNORE_DIRS",
		SpecNames:     "TMUX_SESSION_MANAGER_SPEC_NAMES",
		PreferSpec:    "TMUX_SESSION_MANAGER_PREFER_PROJECT_SPEC",
		Debug:         "TMUX_SESSION_MANAGER_DEBUG",
		TimeoutMs:     "TMUX_SESSION_MANAGER_COMMAND_TIMEOUT_MS",
		EditorCmd:     "TMUX_SESSION_MANAGER_EDITOR_CMD",
		ShellCmd:      "TMUX_SESSION_MANAGER_TERM_CMD",
		DefaultTpl:    "TMUX_SESSION_MANAGER_DEFAULT_TEMPLATE",
		SessionPrefix: "TMUX_SESSION_MANAGER_SESSION_PREFIX",

		AllowShell:           "TMUX_SESSION_MANAGER_ALLOW_SHELL",
		AllowTmuxPassthrough: "TMUX_SESSION_MANAGER_ALLOW_TMUX_PASSTHROUGH",
		AllowedTmuxCommands:  "TMUX_SESSION_MANAGER_ALLOWED_TMUX_COMMANDS",
		DeniedTmuxCommands:   "TMUX_SESSION_MANAGER_DENIED_TMUX_COMMANDS",
		AllowedShellPrefixes: "TMUX_SESSION_MANAGER_ALLOWED_SHELL_PREFIXES",
	}
}

// Resolve builds a Config from env using defaults.
// The cmd layer can further override fields from CLI flags.
func Resolve() Config {
	return ResolveWithEnv(DefaultEnvKeys())
}

// ResolveWithEnv builds Config using a provided EnvKeys set.
func ResolveWithEnv(keys EnvKeys) Config {
	cfg := defaultConfig()

	// Launch mode
	if v := strings.TrimSpace(os.Getenv(keys.LaunchMode)); v != "" {
		cfg.LaunchMode = normalizeLaunchMode(v, cfg.LaunchMode)
	}

	// Roots / depth / ignore
	if v := strings.TrimSpace(os.Getenv(keys.Roots)); v != "" {
		cfg.ProjectRoots = splitCommaPaths(v)
	}
	if v := strings.TrimSpace(os.Getenv(keys.Depth)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.ProjectScanDepth = n
		}
	}
	if v := strings.TrimSpace(os.Getenv(keys.IgnoreDirs)); v != "" {
		cfg.IgnoreDirNames = splitCommaList(v)
	}

	// Spec filenames
	if v := strings.TrimSpace(os.Getenv(keys.SpecNames)); v != "" {
		cfg.SpecFilenames = splitCommaList(v)
	}

	// Prefer project-local spec
	if v := strings.TrimSpace(os.Getenv(keys.PreferSpec)); v != "" {
		cfg.PreferProjectLocalSpec = parseBool(v, cfg.PreferProjectLocalSpec)
	}

	// Debug
	if v := strings.TrimSpace(os.Getenv(keys.Debug)); v != "" {
		cfg.Debug = parseBool(v, cfg.Debug)
	}

	// Timeout
	if v := strings.TrimSpace(os.Getenv(keys.TimeoutMs)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.CommandTimeout = time.Duration(n) * time.Millisecond
		}
	}

	// Defaults
	if v := strings.TrimSpace(os.Getenv(keys.EditorCmd)); v != "" {
		cfg.Defaults.EditorCmd = v
	}
	if v := strings.TrimSpace(os.Getenv(keys.ShellCmd)); v != "" {
		cfg.Defaults.ShellCmd = v
	}
	if v := strings.TrimSpace(os.Getenv(keys.DefaultTpl)); v != "" {
		cfg.Defaults.DefaultTemplate = strings.TrimSpace(v)
	}
	if v := strings.TrimSpace(os.Getenv(keys.SessionPrefix)); v != "" {
		cfg.Defaults.SessionPrefix = strings.TrimSpace(v)
	}

	// Safety toggles
	if v := strings.TrimSpace(os.Getenv(keys.AllowShell)); v != "" {
		cfg.Safety.AllowShell = parseBool(v, cfg.Safety.AllowShell)
	}
	if v := strings.TrimSpace(os.Getenv(keys.AllowTmuxPassthrough)); v != "" {
		cfg.Safety.AllowTmuxPassthrough = parseBool(v, cfg.Safety.AllowTmuxPassthrough)
	}
	if v := strings.TrimSpace(os.Getenv(keys.AllowedTmuxCommands)); v != "" {
		cfg.Safety.AllowedTmuxCommands = splitCommaList(v)
	}
	if v := strings.TrimSpace(os.Getenv(keys.DeniedTmuxCommands)); v != "" {
		cfg.Safety.DeniedTmuxCommands = splitCommaList(v)
	}
	if v := strings.TrimSpace(os.Getenv(keys.AllowedShellPrefixes)); v != "" {
		cfg.Safety.AllowedShellPrefixes = splitCommaListPreserveSpaces(v)
	}

	cfg = cfg.withDerivedDefaults()
	return cfg
}

// ApplyTmuxOptionEnvOverlay overlays config using env variables that are expected to be passed
// from tmux options by the launcher script.
//
// This exists to make the "tmux option -> env -> binary" workflow explicit and testable.
// Typical pattern in tmux launcher:
//
//	TMUX_SESSION_MANAGER_LAUNCH_MODE="$(tmux show -gqv @tmux_session_manager_launch_mode)"
//	TMUX_SESSION_MANAGER_ROOTS="$(tmux show -gqv @tmux_session_manager_roots)"
//
// If you already call Resolve(), you generally do not need this method, but it is useful
// when a caller wants to merge multiple sources with different precedence.
func (c Config) ApplyTmuxOptionEnvOverlay(optionEnv map[string]string) Config {
	out := c

	get := func(key string) string {
		if optionEnv == nil {
			return ""
		}
		return strings.TrimSpace(optionEnv[key])
	}

	// These keys match the env keys used in DefaultEnvKeys().
	if v := get("TMUX_SESSION_MANAGER_LAUNCH_MODE"); v != "" {
		out.LaunchMode = normalizeLaunchMode(v, out.LaunchMode)
	}
	if v := get("TMUX_SESSION_MANAGER_ROOTS"); v != "" {
		out.ProjectRoots = splitCommaPaths(v)
	}
	if v := get("TMUX_SESSION_MANAGER_PROJECT_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			out.ProjectScanDepth = n
		}
	}
	if v := get("TMUX_SESSION_MANAGER_IGNORE_DIRS"); v != "" {
		out.IgnoreDirNames = splitCommaList(v)
	}
	if v := get("TMUX_SESSION_MANAGER_SPEC_NAMES"); v != "" {
		out.SpecFilenames = splitCommaList(v)
	}
	if v := get("TMUX_SESSION_MANAGER_PREFER_PROJECT_SPEC"); v != "" {
		out.PreferProjectLocalSpec = parseBool(v, out.PreferProjectLocalSpec)
	}

	if v := get("TMUX_SESSION_MANAGER_ALLOW_SHELL"); v != "" {
		out.Safety.AllowShell = parseBool(v, out.Safety.AllowShell)
	}
	if v := get("TMUX_SESSION_MANAGER_ALLOW_TMUX_PASSTHROUGH"); v != "" {
		out.Safety.AllowTmuxPassthrough = parseBool(v, out.Safety.AllowTmuxPassthrough)
	}
	if v := get("TMUX_SESSION_MANAGER_ALLOWED_TMUX_COMMANDS"); v != "" {
		out.Safety.AllowedTmuxCommands = splitCommaList(v)
	}
	if v := get("TMUX_SESSION_MANAGER_DENIED_TMUX_COMMANDS"); v != "" {
		out.Safety.DeniedTmuxCommands = splitCommaList(v)
	}
	if v := get("TMUX_SESSION_MANAGER_ALLOWED_SHELL_PREFIXES"); v != "" {
		out.Safety.AllowedShellPrefixes = splitCommaListPreserveSpaces(v)
	}

	if v := get("TMUX_SESSION_MANAGER_DEFAULT_TEMPLATE"); v != "" {
		out.Defaults.DefaultTemplate = v
	}
	if v := get("TMUX_SESSION_MANAGER_EDITOR_CMD"); v != "" {
		out.Defaults.EditorCmd = v
	}
	if v := get("TMUX_SESSION_MANAGER_TERM_CMD"); v != "" {
		out.Defaults.ShellCmd = v
	}
	if v := get("TMUX_SESSION_MANAGER_SESSION_PREFIX"); v != "" {
		out.Defaults.SessionPrefix = v
	}

	if v := get("TMUX_SESSION_MANAGER_DEBUG"); v != "" {
		out.Debug = parseBool(v, out.Debug)
	}
	if v := get("TMUX_SESSION_MANAGER_COMMAND_TIMEOUT_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			out.CommandTimeout = time.Duration(n) * time.Millisecond
		}
	}

	return out.withDerivedDefaults()
}

func defaultConfig() Config {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.Getenv("HOME")
	}

	roots := []string{}
	for _, p := range []string{
		filepath.Join(home, "code"),
		filepath.Join(home, "src"),
		filepath.Join(home, "projects"),
	} {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			roots = append(roots, p)
		}
	}
	if len(roots) == 0 && home != "" {
		roots = []string{home}
	}

	return Config{
		LaunchMode:             "window",
		ProjectRoots:           roots,
		ProjectScanDepth:       2,
		IgnoreDirNames:         []string{".git", "node_modules", "vendor", "dist", "build", "target", ".venv", "__pycache__"},
		SpecFilenames:          []string{".tmux-session.yaml", ".tmux-session.yml", ".tmux-session.json"},
		PreferProjectLocalSpec: true,
		Safety: Safety{
			AllowShell:           false,
			AllowTmuxPassthrough: false,
			AllowedTmuxCommands:  defaultAllowedTmuxCommands(),
			DeniedTmuxCommands:   defaultDeniedTmuxCommands(),
			AllowedShellPrefixes: nil,
		},
		Defaults: Defaults{
			DefaultTemplate: "auto",
			EditorCmd:       "nvim .",
			ShellCmd:        "${SHELL}",
			SessionPrefix:   "",
		},
		Debug:          false,
		CommandTimeout: 0,
	}
}

func (c Config) withDerivedDefaults() Config {
	out := c
	out.LaunchMode = normalizeLaunchMode(out.LaunchMode, "window")

	// Normalize roots and expand ~
	if len(out.ProjectRoots) > 0 {
		norm := make([]string, 0, len(out.ProjectRoots))
		for _, r := range out.ProjectRoots {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			r = expandHome(r)
			norm = append(norm, r)
		}
		if len(norm) > 0 {
			out.ProjectRoots = norm
		}
	}

	// If allowlist empty, set conservative default.
	if len(out.Safety.AllowedTmuxCommands) == 0 {
		out.Safety.AllowedTmuxCommands = defaultAllowedTmuxCommands()
	}
	if len(out.Safety.DeniedTmuxCommands) == 0 {
		out.Safety.DeniedTmuxCommands = defaultDeniedTmuxCommands()
	}

	// Ensure spec filenames include the canonical defaults if user set an empty list accidentally.
	if len(out.SpecFilenames) == 0 {
		out.SpecFilenames = []string{".tmux-session.yaml", ".tmux-session.yml", ".tmux-session.json"}
	}

	// Sanitize depth
	if out.ProjectScanDepth < 0 {
		out.ProjectScanDepth = 0
	}

	return out
}

func normalizeLaunchMode(v string, def string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "popup":
		return "popup"
	case "window":
		return "window"
	default:
		return def
	}
}

// IsTmuxCommandAllowed determines whether a tmux subcommand is allowed under the current Safety config.
// This is intended for enforcement by the spec executor.
//
// Behavior:
// - If AllowTmuxPassthrough is false => always false.
// - If cmd is denied => false.
// - If cmd is in allowed list => true.
// - If allowed list is empty (shouldn't be after defaults) => false.
func (s Safety) IsTmuxCommandAllowed(cmd string) bool {
	if !s.AllowTmuxPassthrough {
		return false
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	lc := strings.ToLower(cmd)

	for _, d := range s.DeniedTmuxCommands {
		if strings.ToLower(strings.TrimSpace(d)) == lc {
			return false
		}
	}
	for _, a := range s.AllowedTmuxCommands {
		if strings.ToLower(strings.TrimSpace(a)) == lc {
			return true
		}
	}
	return false
}

// IsShellCommandAllowed returns true if shell execution is permitted.
// If AllowedShellPrefixes is non-empty, the command must start with one of them (after trimming leading spaces).
func (s Safety) IsShellCommandAllowed(cmd string) bool {
	if !s.AllowShell {
		return false
	}
	cmd = strings.TrimLeft(cmd, " \t")
	if cmd == "" {
		return false
	}
	if len(s.AllowedShellPrefixes) == 0 {
		return true
	}
	for _, p := range s.AllowedShellPrefixes {
		p = strings.TrimLeft(p, " \t")
		if p == "" {
			continue
		}
		if strings.HasPrefix(cmd, p) {
			return true
		}
	}
	return false
}

func defaultAllowedTmuxCommands() []string {
	// Conservative "session construction" interface.
	// Notably excludes: run-shell, pipe-pane, set-hook, source-file, display-popup, etc.
	return []string{
		"new-session",
		"kill-session",
		"rename-session",
		"switch-client",
		"select-session",
		"attach-session",

		"new-window",
		"kill-window",
		"rename-window",
		"select-window",
		"move-window",
		"swap-window",

		"split-window",
		"kill-pane",
		"select-pane",
		"swap-pane",
		"resize-pane",
		"break-pane",
		"join-pane",

		"select-layout",

		"send-keys",
		"set-buffer",
		"display-message",

		"set-option",
		"set-window-option",
		"set-hook",
	}
}

func defaultDeniedTmuxCommands() []string {
	// High-risk / surprising commands (execution vectors or state mutation beyond session layout).
	return []string{
		"run-shell",
		"pipe-pane",
		"pipep", // alias
		"source-file",
		"source",
		"display-popup",
		"load-buffer",
		"save-buffer",
		"capture-pane",
		"respawn-pane",
		"respawn-window",
	}
}

// Helpers

func splitCommaList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// splitCommaListPreserveSpaces splits on commas but does NOT trim internal spaces of each element,
// only trims surrounding whitespace. Useful for allowed shell prefixes which may intentionally end
// with a space ("npm ").
func splitCommaListPreserveSpaces(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		// Trim only leading/trailing whitespace
		p = strings.Trim(p, " \t\r\n")
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func splitCommaPaths(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, expandHome(p))
	}
	return out
}

func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if p == "~" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func parseBool(v string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}
