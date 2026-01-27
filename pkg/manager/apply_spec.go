package manager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tmux-session-manager/pkg/spec"
	"tmux-session-manager/pkg/templates"
)

// sanitizeSessionNameForApply converts a user-facing name into a tmux-safe session identifier.
func sanitizeSessionNameForApply(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
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
		return "session"
	}
	return out
}

// ApplySpecOptions controls how a spec is validated, compiled, and executed.
type ApplySpecOptions struct {
	// ProjectPath is the "context root" used for ${PROJECT_PATH} substitutions and window/pane cwd defaults.
	// If empty, defaults to the directory containing SpecPath.
	ProjectPath string

	// ProjectName is used for ${PROJECT_NAME} substitutions. If empty, derived from ProjectPath basename.
	ProjectName string

	// SessionName overrides the spec's session name and derived session naming.
	// If empty, a default is derived (see ApplySpecFile).
	SessionName string

	// AllowShell enables spec "shell" actions (unsafe; opt-in).
	AllowShell bool

	// AllowTmuxPassthrough enables spec "tmux" actions (advanced; opt-in and allowlisted).
	AllowTmuxPassthrough bool

	// IncludeEnsureSession prepends an ensure/create session action in the compiled plan.
	// If false (default), the caller is expected to create the session separately (typical for the TUI),
	// and the plan focuses on windows/panes/layout/actions.
	IncludeEnsureSession bool

	// DryRun, when true, does not execute; it returns the compiled commands as a preview.
	DryRun bool

	// Runner, when non-nil, is used to execute compiled tmux commands. If nil and DryRun=false,
	// ApplySpec will return an error.
	Runner templates.Runner
}

// ApplyResult describes the outcome of applying a spec.
type ApplyResult struct {
	SpecPath     string
	ProjectPath  string
	SessionName  string
	UnsafeUsed   bool
	DryRunLines  []string
	Warnings     []string
	CompiledArgs int // number of tmux commands in the compiled plan
}

// ApplySpecFile loads, validates, compiles, and optionally executes a spec file.
func ApplySpecFile(specPath string, opt ApplySpecOptions) (ApplyResult, error) {
	specPath = strings.TrimSpace(specPath)
	if specPath == "" {
		return ApplyResult{}, errors.New("spec path is required")
	}

	absSpecPath, err := filepath.Abs(specPath)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("resolve spec path: %w", err)
	}
	specPath = absSpecPath

	st, err := os.Stat(specPath)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("stat spec: %w", err)
	}
	if st.IsDir() {
		return ApplyResult{}, fmt.Errorf("spec path is a directory: %s", specPath)
	}

	// Load + structural validation
	s, err := spec.LoadFile(specPath)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("load spec: %w", err)
	}

	projectPath := strings.TrimSpace(opt.ProjectPath)
	if projectPath == "" {
		projectPath = filepath.Dir(specPath)
	}
	projectPath = expandHome(projectPath)
	if abs, aerr := filepath.Abs(projectPath); aerr == nil {
		projectPath = abs
	}

	projectName := strings.TrimSpace(opt.ProjectName)
	if projectName == "" {
		projectName = filepath.Base(strings.TrimRight(projectPath, string(filepath.Separator)))
	}

	// Build spec policy (gates).
	pol := spec.DefaultPolicy()
	pol.AllowShell = opt.AllowShell
	pol.AllowTmuxPassthrough = opt.AllowTmuxPassthrough

	if err := s.ValidatePolicy(pol); err != nil {
		return ApplyResult{}, fmt.Errorf("spec policy rejected: %w", err)
	}

	// Session name precedence: opt.SessionName > spec.session.name > sanitized project name.
	sessionName := strings.TrimSpace(opt.SessionName)
	if sessionName == "" {
		sessionName = strings.TrimSpace(s.Session.Name)
	}
	if sessionName == "" {
		sessionName = sanitizeSessionNameForApply(projectName)
	}
	if sessionName == "" {
		sessionName = "session"
	}

	// Build engine + compile.
	eng := templates.NewEngine()
	eng.Policy.AllowShell = opt.AllowShell
	eng.Policy.AllowTmuxPassthrough = opt.AllowTmuxPassthrough

	ctx := templates.Context{
		ProjectName: projectName,
		ProjectPath: projectPath,
		SessionName: sessionName,
		WorkingDir:  projectPath,
		Env:         s.Env,
	}

	tpl, err := templates.FromSpec(ctx, *s, opt.AllowShell, opt.AllowTmuxPassthrough, opt.IncludeEnsureSession)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("convert spec: %w", err)
	}

	compiled, err := eng.Compile(ctx, tpl)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("compile spec: %w", err)
	}

	res := ApplyResult{
		SpecPath:     specPath,
		ProjectPath:  projectPath,
		SessionName:  ctx.SessionName,
		UnsafeUsed:   compiled.UnsafeUsed,
		Warnings:     append([]string(nil), compiled.Warnings...),
		DryRunLines:  templates.DryRunLines(compiled),
		CompiledArgs: len(compiled.Commands),
	}

	if opt.DryRun {
		return res, nil
	}

	// Execute
	if opt.Runner == nil {
		return ApplyResult{}, errors.New("no runner provided for execution (set DryRun=true or provide a Runner)")
	}
	eng.Runner = opt.Runner

	_, err = eng.Execute(compiled, false)
	if err != nil {
		return res, fmt.Errorf("execute spec: %w", err)
	}

	return res, nil
}

// expandHome is defined elsewhere in this package.
