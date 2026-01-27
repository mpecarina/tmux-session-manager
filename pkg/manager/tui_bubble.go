package manager

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"tmux-session-manager/pkg/spec"
	"tmux-session-manager/pkg/templates"
)

// RunTUI launches the Bubble Tea UI.
func RunTUI(opts UIOptions) error {
	// Improve rendering reliability when launched inside tmux popups/wrappers.
	if os.Getenv("TMUX_SESSION_MANAGER_IN_POPUP") != "" {
		_ = os.Setenv("TERM", "xterm-256color")
	}

	m := newModel(opts)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// UIOptions controls the selector behavior for tmux-session-manager.
type UIOptions struct {
	InitialQuery string

	// Mode controls UI host container when called from tmux plugin wrapper:
	// - "window" (default): new tmux window
	// - "popup": tmux popup (tmux >= 3.2)
	// This is primarily used by launch scripts; the TUI only cares for small rendering tweaks.
	LaunchMode string

	// ProjectsPaths are root directories to scan for projects. If empty, defaults to:
	//   - ~/code
	//   - ~/src
	//   - ~/projects
	ProjectsPaths []string

	// ProjectScanDepth controls recursive scanning depth under each root.
	// If 0, defaults to the built-in scanner default.
	ProjectScanDepth int

	// ProjectSpecNames are filenames to look for inside a project directory.
	// If empty, defaults to pkg/spec defaults:
	//   - .tmux-session.yaml
	//   - .tmux-session.yml
	//   - .tmux-session.json
	ProjectSpecNames []string

	// PreferProjectSpec controls whether a project-local spec (if present) takes precedence
	// over built-in templates. Default true.
	PreferProjectSpec bool

	// MaxResults limits the list height (0 means auto).
	MaxResults int

	// DefaultTemplate is one of: "auto", "node", "python", "go", "empty"
	DefaultTemplate string

	// PreviewLines caps the preview height when enabled (0 means auto).
	PreviewLines int

	// DryRun prevents executing tmux mutations and only previews the plan.
	DryRun bool

	// AllowShell enables arbitrary shell actions in project-local specs (unsafe; opt-in).
	AllowShell bool

	// AllowTmuxPassthrough enables validated raw tmux passthrough actions in specs (advanced; opt-in).
	AllowTmuxPassthrough bool
}

type listMode int

const (
	modeSessions listMode = iota
	modeProjects
)

// snapshot defaults / paths
const (
	defaultSnapshotDirName  = "tmux-session-manager"
	defaultSnapshotFileMode = 0o600
)

type templateKind int

const (
	tplEmpty templateKind = iota
	tplNode
	tplPython
	tplGo
)

func (t templateKind) String() string {
	switch t {
	case tplNode:
		return "node"
	case tplPython:
		return "python"
	case tplGo:
		return "go"
	default:
		return "empty"
	}
}

type model struct {
	opts UIOptions

	// input is the incremental search (used for both sessions and projects).
	input textinput.Model

	mode listMode

	// sessions / projects are the backing datasets, filtered is view.
	sessions []sessionItem
	projects []projectItem

	filteredSessions []sessionItem
	filteredProjects []projectItem

	selected int
	scroll   int

	// help/preview
	showHelp    bool
	showPreview bool

	// confirm / prompts
	confirmKill bool
	renameMode  bool
	newMode     bool

	renameValue string
	newValue    string

	// template selection (only used when creating from project)
	template templateKind

	// multi-key sequences
	pendingG     bool
	lastGGAt     time.Time
	ggTimeout    time.Duration
	lastRefresh  time.Time
	refreshAfter time.Duration

	status      string
	statusUntil time.Time

	width  int
	height int

	quitting bool
}

type sessionItem struct {
	Name      string
	Windows   int
	Attached  bool
	CreatedAt string
	RawLine   string
}

type projectItem struct {
	Name string
	Path string
}

func newModel(opts UIOptions) model {
	// Derive safety toggles from env (default: safe).
	opts.AllowShell = parseEnvBool("TMUX_SESSION_MANAGER_ALLOW_SHELL", opts.AllowShell)
	opts.AllowTmuxPassthrough = parseEnvBool("TMUX_SESSION_MANAGER_ALLOW_TMUX_PASSTHROUGH", opts.AllowTmuxPassthrough)

	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "search..."
	ti.CharLimit = 256
	ti.Width = 40
	ti.Blur()

	m := model{
		opts:  opts,
		input: ti,

		mode:         modeSessions,
		showHelp:     false,
		showPreview:  true,
		template:     parseTemplate(opts.DefaultTemplate),
		ggTimeout:    650 * time.Millisecond,
		refreshAfter: 2 * time.Second,
	}

	if m.opts.MaxResults <= 0 {
		m.opts.MaxResults = 20
	}
	if m.opts.PreviewLines <= 0 {
		m.opts.PreviewLines = 12
	}

	m.refreshSessions()
	m.refreshProjects()
	m.recomputeFilter()
	return m
}

func parseTemplate(s string) templateKind {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "node", "js", "ts", "typescript":
		return tplNode
	case "python", "py":
		return tplPython
	case "go", "golang":
		return tplGo
	default:
		return tplEmpty
	}
}

func (m model) Init() tea.Cmd {
	// No async at first pass: keep simple and deterministic.
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch x := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = x.Width
		m.height = x.Height
		m.input.Width = clampInt(m.width-6, 10, 80)

		return m, nil

	case tea.KeyMsg:

		// Allow ESC to exit modes / blur, consistent with vim mental model.
		if m.renameMode || m.newMode {
			return m.handlePromptKeys(x)
		}
		if m.confirmKill {
			return m.handleConfirmKeys(x)
		}
		return m.handleGlobalKeys(x)
	}

	// Let textinput update when focused.
	if m.input.Focused() {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.recomputeFilter()
		return m, cmd
	}

	return m, nil
}

func (m model) handlePromptKeys(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.renameMode = false
		m.newMode = false
		m.renameValue = ""
		m.newValue = ""
		m.input.Blur()
		m.setStatus("cancelled", 1200*time.Millisecond)
		return m, nil
	case "enter":
		if m.renameMode {
			name := strings.TrimSpace(m.renameValue)
			if name == "" {
				m.setStatus("rename: empty name", 1500*time.Millisecond)
				return m, nil
			}
			cur := m.currentSessionName()
			if cur == "" {
				m.setStatus("rename: no session selected", 1500*time.Millisecond)
				return m, nil
			}
			if err := tmuxRenameSession(cur, name); err != nil {
				m.setStatus("rename failed: "+err.Error(), 2500*time.Millisecond)
				return m, nil
			}
			m.renameMode = false
			m.renameValue = ""
			m.refreshSessions()
			m.recomputeFilter()
			m.setStatus("renamed "+cur+" -> "+name, 1800*time.Millisecond)
			return m, nil
		}

		if m.newMode {
			name := strings.TrimSpace(m.newValue)
			if name == "" {
				m.setStatus("new: empty name", 1500*time.Millisecond)
				return m, nil
			}
			// Create new empty session (no project).
			if err := tmuxNewSessionDetached(name, ""); err != nil {
				m.setStatus("new failed: "+err.Error(), 2500*time.Millisecond)
				return m, nil
			}
			if err := tmuxSwitchClient(name); err != nil {
				m.setStatus("switch failed: "+err.Error(), 2500*time.Millisecond)
				return m, nil
			}
			m.newMode = false
			m.newValue = ""
			m.refreshSessions()
			m.recomputeFilter()
			m.setStatus("created "+name, 1800*time.Millisecond)
			return m, tea.Quit
		}
		return m, nil
	case "backspace":
		if m.renameMode {
			m.renameValue = dropLastRune(m.renameValue)
			return m, nil
		}
		if m.newMode {
			m.newValue = dropLastRune(m.newValue)
			return m, nil
		}
		return m, nil
	default:
		// typed chars
		if len(k.Runes) > 0 {
			if m.renameMode {
				m.renameValue += string(k.Runes)
				return m, nil
			}
			if m.newMode {
				m.newValue += string(k.Runes)
				return m, nil
			}
		}
	}

	return m, nil
}

func (m model) handleConfirmKeys(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "y", "Y":
		name := m.currentSessionName()
		if name == "" {
			m.confirmKill = false
			m.setStatus("kill: no session selected", 1500*time.Millisecond)
			return m, nil
		}
		if err := tmuxKillSession(name); err != nil {
			m.confirmKill = false
			m.setStatus("kill failed: "+err.Error(), 2500*time.Millisecond)
			return m, nil
		}
		m.confirmKill = false
		m.refreshSessions()
		m.recomputeFilter()
		m.selected = clampInt(m.selected, 0, m.currentListLen()-1)
		m.setStatus("killed "+name, 1800*time.Millisecond)
		return m, nil
	case "n", "N", "esc", "q":
		m.confirmKill = false
		m.setStatus("cancelled", 1200*time.Millisecond)
		return m, nil
	}
	return m, nil
}

func (m model) handleGlobalKeys(k tea.KeyMsg) (tea.Model, tea.Cmd) {

	// When search is focused, still allow keybindings that should work "globally"
	// (mode switching, help, preview, quit, accept, command bar).
	if m.input.Focused() {
		switch k.String() {
		case "esc":
			m.input.Blur()
			m.setStatus("search: off", 800*time.Millisecond)
			return m, nil

		case "enter":
			// accept current selection even if search focused
			m.input.Blur()
			return m.accept()

		case "tab", "ctrl+t":
			// Toggle mode even while search is focused.
			if m.mode == modeSessions {
				m.mode = modeProjects
				m.selected = 0
				m.scroll = 0
				m.recomputeFilter()
				m.setStatus("mode: projects", 900*time.Millisecond)
			} else {
				m.mode = modeSessions
				m.selected = 0
				m.scroll = 0
				m.recomputeFilter()
				m.setStatus("mode: sessions", 900*time.Millisecond)
			}
			return m, nil

		case "ctrl+p":
			// Force projects mode even while search is focused.
			if m.mode != modeProjects {
				m.mode = modeProjects
				m.selected = 0
				m.scroll = 0
				m.recomputeFilter()
				m.setStatus("mode: projects", 900*time.Millisecond)
			}
			return m, nil

		case "ctrl+o":
			// Force sessions mode even while search is focused.
			// (ctrl+s conflicts with tmux prefix when prefix is set to C-s.)
			if m.mode != modeSessions {
				m.mode = modeSessions
				m.selected = 0
				m.scroll = 0
				m.recomputeFilter()
				m.setStatus("mode: sessions", 900*time.Millisecond)
			}
			return m, nil

		case "q":
			m.quitting = true
			return m, tea.Quit

		case "?", "h":
			m.showHelp = !m.showHelp
			return m, nil

		case "p":
			m.showPreview = !m.showPreview
			return m, nil

		default:
			// let textinput handle everything else
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(k)
			m.recomputeFilter()
			return m, cmd
		}
	}

	// Vim-like sequences: gg
	now := time.Now()
	if m.pendingG {
		// clear pending after timeout
		if now.Sub(m.lastGGAt) > m.ggTimeout {
			m.pendingG = false
		}
	}
	switch k.String() {
	case "q":
		m.quitting = true
		return m, tea.Quit

	case "?", "h":
		m.showHelp = !m.showHelp
		return m, nil

	case "p":
		m.showPreview = !m.showPreview
		return m, nil

	case "/":
		m.input.Focus()
		m.setStatus("search: on", 800*time.Millisecond)
		return m, nil

	case "tab", "ctrl+t":
		// Some terminal/tmux setups won't deliver "tab" to the application reliably.
		// Provide ctrl+t as a second, deterministic toggle key.
		if m.mode == modeSessions {
			m.mode = modeProjects
			m.selected = 0
			m.scroll = 0
			m.recomputeFilter()
			m.setStatus("mode: projects", 900*time.Millisecond)
		} else {
			m.mode = modeSessions
			m.selected = 0
			m.scroll = 0
			m.recomputeFilter()
			m.setStatus("mode: sessions", 900*time.Millisecond)
		}
		return m, nil

	case "ctrl+p":
		// Force projects mode for environments where Tab/Ctrl+T are swallowed.
		if m.mode != modeProjects {
			m.mode = modeProjects
			m.selected = 0
			m.scroll = 0
			m.recomputeFilter()
			m.setStatus("mode: projects", 900*time.Millisecond)
		}
		return m, nil

	case "ctrl+o":
		// Force sessions mode for symmetry with ctrl+p.
		// (ctrl+s conflicts with tmux prefix when prefix is set to C-s.)
		if m.mode != modeSessions {
			m.mode = modeSessions
			m.selected = 0
			m.scroll = 0
			m.recomputeFilter()
			m.setStatus("mode: sessions", 900*time.Millisecond)
		}
		return m, nil

	case "j", "down":
		m.move(1)
		return m, nil
	case "k", "up":
		m.move(-1)
		return m, nil

	case "ctrl-d":
		m.pageDown()
		return m, nil
	case "ctrl-u":
		m.pageUp()
		return m, nil

	case "G":
		m.gotoBottom()
		return m, nil

	case "g":
		if m.pendingG && now.Sub(m.lastGGAt) <= m.ggTimeout {
			m.pendingG = false
			m.gotoTop()
			return m, nil
		}
		m.pendingG = true
		m.lastGGAt = now
		return m, nil

	case "enter":
		return m.accept()

	case "r":
		if m.mode != modeSessions {
			m.setStatus("rename: sessions mode only", 1500*time.Millisecond)
			return m, nil
		}
		name := m.currentSessionName()
		if name == "" {
			m.setStatus("rename: no session selected", 1500*time.Millisecond)
			return m, nil
		}
		m.renameMode = true
		m.renameValue = ""
		return m, nil

	case "n":
		if m.mode != modeSessions {
			m.setStatus("new: sessions mode only", 1500*time.Millisecond)
			return m, nil
		}
		m.newMode = true
		m.newValue = ""
		return m, nil

	case "d":
		if m.mode != modeSessions {
			m.setStatus("kill: sessions mode only", 1500*time.Millisecond)
			return m, nil
		}
		name := m.currentSessionName()
		if name == "" {
			m.setStatus("kill: no session selected", 1500*time.Millisecond)
			return m, nil
		}
		// Avoid killing current session without explicit confirm.
		m.confirmKill = true
		return m, nil

	case "e":
		// Edit mode:
		// - snapshot current session to ~/.config/tmux-session-manager/snapshots/<name>.<ts>.tmux-session.yaml
		// - create new session rooted at current pane path
		// - open editor there
		return m.editNewSessionInCurrentDir()

	case "t":
		// cycle template (only meaningful for project-driven create)
		m.template = (m.template + 1) % 4
		m.setStatus("template: "+m.template.String(), 1200*time.Millisecond)
		return m, nil

	case "w":
		// In projects mode: create/switch session for that project.
		// In sessions mode: no-op (reserved for window-like actions in future).
		if m.mode != modeProjects {
			m.setStatus("w: switch to projects mode (tab)", 1500*time.Millisecond)
			return m, nil
		}
		return m.projectAccept()

	case "R":
		m.refreshSessions()
		m.refreshProjects()
		m.recomputeFilter()
		m.setStatus("refreshed", 1000*time.Millisecond)
		return m, nil
	}

	return m, nil
}

func (m model) accept() (tea.Model, tea.Cmd) {
	switch m.mode {
	case modeSessions:
		// sessionx-like behavior:
		// - if a session is selected, switch to it
		// - else, if the user typed a query, create a new session with that name and switch to it
		name := m.currentSessionName()
		if name == "" {
			q := strings.TrimSpace(m.input.Value())
			if q == "" {
				m.setStatus("no session selected", 1200*time.Millisecond)
				return m, nil
			}

			newName := sanitizeSessionName(q)
			if newName == "" {
				m.setStatus("new: invalid name", 1500*time.Millisecond)
				return m, nil
			}

			// Create if missing, then switch.
			exists, _ := tmuxHasSession(newName)
			if !exists {
				if err := tmuxNewSessionDetached(newName, ""); err != nil {
					m.setStatus("new failed: "+err.Error(), 2500*time.Millisecond)
					return m, nil
				}
			}
			if err := tmuxSwitchClient(newName); err != nil {
				m.setStatus("switch failed: "+err.Error(), 2500*time.Millisecond)
				return m, nil
			}
			m.setStatus("switched to "+newName, 1000*time.Millisecond)
			return m, tea.Quit
		}

		// Switch client to selected session.
		if err := tmuxSwitchClient(name); err != nil {
			m.setStatus("switch failed: "+err.Error(), 2500*time.Millisecond)
			return m, nil
		}
		m.setStatus("switched to "+name, 1000*time.Millisecond)
		return m, tea.Quit

	case modeProjects:
		return m.projectAccept()
	default:
		return m, nil
	}
}

func (m model) projectAccept() (tea.Model, tea.Cmd) {
	prj := m.currentProject()
	if prj.Path == "" {
		m.setStatus("no project selected", 1200*time.Millisecond)
		return m, nil
	}
	sessionName := sanitizeSessionName(prj.Name)
	if sessionName == "" {
		sessionName = "project"
	}

	// If session exists, switch to it; otherwise create using spec (if enabled/present) or template.
	exists, _ := tmuxHasSession(sessionName)
	if !exists {
		if m.opts.DryRun {
			// In dry-run, do not mutate tmux. Just surface intent in preview/status.
			if m.opts.PreferProjectSpec {
				s, _, ok, err := spec.LoadProjectLocalWithNames(prj.Path, m.opts.ProjectSpecNames)
				if err != nil {
					m.setStatus("dry-run: spec load failed: "+err.Error(), 3000*time.Millisecond)
					return m, nil
				}
				if ok {
					pol := spec.DefaultPolicy()
					pol.AllowShell = m.opts.AllowShell
					pol.AllowTmuxPassthrough = m.opts.AllowTmuxPassthrough
					if verr := s.ValidatePolicy(pol); verr != nil {
						m.setStatus("dry-run: spec invalid: "+verr.Error(), 3000*time.Millisecond)
						return m, nil
					}

					eng := templates.NewEngine()
					eng.Policy.AllowShell = m.opts.AllowShell
					eng.Policy.AllowTmuxPassthrough = m.opts.AllowTmuxPassthrough

					ctx := templates.Context{
						ProjectName: prj.Name,
						ProjectPath: prj.Path,
						SessionName: sessionName,
						WorkingDir:  prj.Path,
						Env:         s.Env,
					}

					ts, terr := templates.FromSpec(
						ctx,
						*s,
						m.opts.AllowShell,
						m.opts.AllowTmuxPassthrough,
						false, // includeEnsureSession (TUI creates session before applying spec)
					)
					if terr != nil {
						m.setStatus("dry-run: spec compile failed: "+terr.Error(), 3000*time.Millisecond)
						return m, nil
					}

					compiled, cerr := eng.Compile(ctx, ts)
					if cerr != nil {
						m.setStatus("dry-run: spec compile failed: "+cerr.Error(), 3000*time.Millisecond)
						return m, nil
					}

					_ = compiled // preview uses compile output; no execution in dry-run
					m.setStatus("dry-run: would create session "+sessionName+" from spec", 2500*time.Millisecond)
					return m, nil
				}
			}

			m.setStatus("dry-run: would create session "+sessionName+" using template "+m.template.String(), 2500*time.Millisecond)
			return m, nil
		}

		if err := tmuxNewSessionDetached(sessionName, prj.Path); err != nil {
			m.setStatus("create failed: "+err.Error(), 2500*time.Millisecond)
			return m, nil
		}

		// Prefer project-local spec iff enabled.
		usedSpec := false
		if m.opts.PreferProjectSpec {
			s, _, ok, err := spec.LoadProjectLocalWithNames(prj.Path, m.opts.ProjectSpecNames)
			if err != nil {
				m.setStatus("spec load failed: "+err.Error(), 2500*time.Millisecond)
			} else if ok {
				pol := spec.DefaultPolicy()
				pol.AllowShell = m.opts.AllowShell
				pol.AllowTmuxPassthrough = m.opts.AllowTmuxPassthrough
				if verr := s.ValidatePolicy(pol); verr != nil {
					m.setStatus("spec invalid: "+verr.Error(), 2500*time.Millisecond)
				} else {
					eng := templates.NewEngine()
					eng.Policy.AllowShell = m.opts.AllowShell
					eng.Policy.AllowTmuxPassthrough = m.opts.AllowTmuxPassthrough
					eng.Runner = &templates.TmuxExecRunner{} // executes `tmux <args...>`

					ctx := templates.Context{
						ProjectName: prj.Name,
						ProjectPath: prj.Path,
						SessionName: sessionName,
						WorkingDir:  prj.Path,
						Env:         s.Env,
					}

					ts, terr := templates.FromSpec(
						ctx,
						*s,
						m.opts.AllowShell,
						m.opts.AllowTmuxPassthrough,
						false, // includeEnsureSession (TUI creates session before applying spec)
					)
					if terr != nil {
						m.setStatus("spec apply failed: "+terr.Error(), 2500*time.Millisecond)
					} else {
						compiled, cerr := eng.Compile(ctx, ts)
						if cerr != nil {
							m.setStatus("spec apply failed: "+cerr.Error(), 2500*time.Millisecond)
						} else {
							if _, eerr := eng.Execute(compiled, false); eerr != nil {
								m.setStatus("spec apply failed: "+eerr.Error(), 2500*time.Millisecond)
							} else {
								usedSpec = true
							}
						}
					}
				}
			}
		}

		// Fallback to built-in template if we did not use a spec.
		if !usedSpec {
			if err := applyTemplate(sessionName, prj.Path, m.template); err != nil {
				m.setStatus("template failed: "+err.Error(), 2500*time.Millisecond)
				// Still allow switching.
			}
		}
	}

	if m.opts.DryRun {
		m.setStatus("dry-run: would switch to "+sessionName, 2000*time.Millisecond)
		return m, nil
	}

	if err := tmuxSwitchClient(sessionName); err != nil {
		m.setStatus("switch failed: "+err.Error(), 2500*time.Millisecond)
		return m, nil
	}
	m.setStatus("switched to "+sessionName, 1000*time.Millisecond)
	return m, tea.Quit
}

func (m *model) recomputeFilter() {
	q := strings.TrimSpace(m.input.Value())
	if q == "" {
		m.filteredSessions = append([]sessionItem(nil), m.sessions...)
		m.filteredProjects = append([]projectItem(nil), m.projects...)
	} else {
		m.filteredSessions = m.filteredSessions[:0]
		for _, s := range m.sessions {
			if fuzzyContains(strings.ToLower(s.Name), strings.ToLower(q)) {
				m.filteredSessions = append(m.filteredSessions, s)
			}
		}
		m.filteredProjects = m.filteredProjects[:0]
		for _, p := range m.projects {
			hay := strings.ToLower(p.Name + " " + p.Path)
			if fuzzyContains(hay, strings.ToLower(q)) {
				m.filteredProjects = append(m.filteredProjects, p)
			}
		}
	}

	// Clamp selection/scroll.
	max := m.currentListLen()
	if max <= 0 {
		m.selected = 0
		m.scroll = 0
		return
	}
	m.selected = clampInt(m.selected, 0, max-1)
	m.scroll = clampInt(m.scroll, 0, max-1)
}

func (m *model) refreshSessions() {
	items, err := tmuxListSessions()
	if err != nil {
		m.sessions = nil
		m.setStatus("tmux list-sessions failed: "+err.Error(), 3000*time.Millisecond)
		return
	}
	m.sessions = items
}

func (m *model) refreshProjects() {
	paths := m.opts.ProjectsPaths
	depth := m.opts.ProjectScanDepth

	// Default roots if still empty.
	if len(paths) == 0 {
		home, _ := os.UserHomeDir()
		paths = []string{
			filepath.Join(home, "code"),
			filepath.Join(home, "src"),
			filepath.Join(home, "projects"),
		}
	}

	// Default depth if still unset.
	if depth <= 0 {
		depth = 2
	}

	projects := scanProjects(paths, depth)
	m.projects = projects
}

func (m *model) move(delta int) {
	n := m.currentListLen()
	if n <= 0 {
		m.selected = 0
		m.scroll = 0
		return
	}
	m.selected = clampInt(m.selected+delta, 0, n-1)

	// Maintain scroll window.
	visible := m.visibleListHeight()
	if visible <= 0 {
		visible = 10
	}
	if m.selected < m.scroll {
		m.scroll = m.selected
	} else if m.selected >= m.scroll+visible {
		m.scroll = m.selected - visible + 1
	}
}

func (m *model) gotoTop() {
	if m.currentListLen() <= 0 {
		m.selected = 0
		m.scroll = 0
		return
	}
	m.selected = 0
	m.scroll = 0
}

func (m *model) gotoBottom() {
	n := m.currentListLen()
	if n <= 0 {
		m.selected = 0
		m.scroll = 0
		return
	}
	m.selected = n - 1
	visible := m.visibleListHeight()
	if visible <= 0 {
		visible = 10
	}
	m.scroll = clampInt(n-visible, 0, n-1)
}

func (m *model) pageDown() {
	visible := m.visibleListHeight()
	if visible <= 0 {
		visible = 10
	}
	m.move(visible / 2)
}

func (m *model) pageUp() {
	visible := m.visibleListHeight()
	if visible <= 0 {
		visible = 10
	}
	m.move(-(visible / 2))
}

func (m model) visibleListHeight() int {
	// Header (~3) + footer/status (~2). Preview steals some height if visible.
	h := m.height
	if h <= 0 {
		return m.opts.MaxResults
	}

	header := 3
	footer := 2
	help := 0
	if m.showHelp {
		help = 8
	}
	preview := 0
	if m.showPreview {
		preview = m.opts.PreviewLines + 2
	}
	list := h - header - footer - help - preview
	list = clampInt(list, 4, m.opts.MaxResults)
	return list
}

func (m model) currentListLen() int {
	switch m.mode {
	case modeProjects:
		return len(m.filteredProjects)
	default:
		return len(m.filteredSessions)
	}
}

func (m model) currentSessionName() string {
	if m.mode != modeSessions {
		return ""
	}
	if m.selected < 0 || m.selected >= len(m.filteredSessions) {
		return ""
	}
	return m.filteredSessions[m.selected].Name
}

func (m model) currentProject() projectItem {
	if m.mode != modeProjects {
		return projectItem{}
	}
	if m.selected < 0 || m.selected >= len(m.filteredProjects) {
		return projectItem{}
	}
	return m.filteredProjects[m.selected]
}

func (m *model) setStatus(s string, d time.Duration) {
	m.status = s
	m.statusUntil = time.Now().Add(d)
}

func (m model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	// Styles
	titleStyle := lipgloss.NewStyle().Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	hlStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)

	modeLabel := "sessions"
	if m.mode == modeProjects {
		modeLabel = "projects"
	}

	// Header
	fmt.Fprintf(&b, "%s  %s\n", titleStyle.Render("tmux-session-manager"), dimStyle.Render("["+modeLabel+"]  (tab to toggle)"))

	if m.input.Focused() {
		fmt.Fprintf(&b, "%s\n", hlStyle.Render(m.input.View()))
	} else {
		q := strings.TrimSpace(m.input.Value())
		if q == "" {
			fmt.Fprintf(&b, "%s\n", dimStyle.Render("/ (search)  r(rename) n(new) d(kill)  w(create from project)  t(template)  p(preview)  ?(help)  q(quit)"))
		} else {
			fmt.Fprintf(&b, "%s\n", dimStyle.Render("query: "+q+"  (/ to edit, esc to clear focus)"))
		}
	}

	// Prompt overlays
	if m.renameMode {
		fmt.Fprintf(&b, "%s %s\n", hlStyle.Render("rename>"), m.renameValue)
	}
	if m.newMode {
		fmt.Fprintf(&b, "%s %s\n", hlStyle.Render("new>"), m.newValue)
	}
	if m.confirmKill {
		name := m.currentSessionName()
		if name == "" {
			name = "<none>"
		}
		fmt.Fprintf(&b, "%s %s\n", warnStyle.Render("kill?"), "Kill session "+name+" (y/n)")
	}

	// List
	listH := m.visibleListHeight()
	if listH <= 0 {
		listH = 10
	}

	switch m.mode {
	case modeSessions:
		if len(m.filteredSessions) == 0 {
			fmt.Fprintf(&b, "%s\n", dimStyle.Render("(no sessions)"))
		} else {
			end := minIntTUI(len(m.filteredSessions), m.scroll+listH)
			for i := m.scroll; i < end; i++ {
				s := m.filteredSessions[i]
				prefix := "  "
				lineStyle := lipgloss.NewStyle()
				if i == m.selected {
					prefix = "> "
					lineStyle = lineStyle.Bold(true).Foreground(lipgloss.Color("15"))
				} else {
					lineStyle = lineStyle.Foreground(lipgloss.Color("7"))
				}

				meta := ""
				if s.Attached {
					meta = " (attached)"
				}
				if s.Windows > 0 {
					meta = fmt.Sprintf(" [%dw]%s", s.Windows, meta)
				}

				fmt.Fprintf(&b, "%s%s\n", prefix, lineStyle.Render(s.Name+meta))
			}
		}

	case modeProjects:
		if len(m.filteredProjects) == 0 {
			fmt.Fprintf(&b, "%s\n", dimStyle.Render("(no projects found)"))
		} else {
			end := minIntTUI(len(m.filteredProjects), m.scroll+listH)
			for i := m.scroll; i < end; i++ {
				p := m.filteredProjects[i]
				prefix := "  "
				lineStyle := lipgloss.NewStyle()
				if i == m.selected {
					prefix = "> "
					lineStyle = lineStyle.Bold(true).Foreground(lipgloss.Color("15"))
				} else {
					lineStyle = lineStyle.Foreground(lipgloss.Color("7"))
				}

				sessionName := sanitizeSessionName(p.Name)
				if sessionName == "" {
					sessionName = "project"
				}
				meta := dimStyle.Render("  → " + sessionName + "  [" + m.template.String() + "]")
				fmt.Fprintf(&b, "%s%s\n", prefix, lineStyle.Render(p.Name)+" "+meta)
				fmt.Fprintf(&b, "%s%s\n", "  ", dimStyle.Render(p.Path))
			}
		}
	}

	// Preview
	if m.showPreview {
		fmt.Fprintf(&b, "\n%s\n", dimStyle.Render("preview"))
		fmt.Fprintf(&b, "%s\n", dimStyle.Render(strings.Repeat("-", clampInt(m.width, 30, 120))))
		prev := m.previewText()
		if prev == "" {
			prev = "(no preview)"
		}
		lines := strings.Split(prev, "\n")
		if len(lines) > m.opts.PreviewLines {
			lines = lines[:m.opts.PreviewLines]
		}
		for _, ln := range lines {
			fmt.Fprintf(&b, "%s\n", dimStyle.Render(ln))
		}
	}

	// Help
	if m.showHelp {
		fmt.Fprintf(&b, "\n%s\n", hlStyle.Render("help"))
		fmt.Fprintf(&b, "%s\n", dimStyle.Render("j/k move · gg/G top/bottom · ctrl-u/d page · / search · tab toggle mode"))
		fmt.Fprintf(&b, "%s\n", dimStyle.Render("enter switch/attach/create · d kill (confirm) · r rename · n new session · w create from project · e edit (snapshot+new)"))
		fmt.Fprintf(&b, "%s\n", dimStyle.Render("t cycle template (node/python/go/empty) · p preview · q quit"))
	}

	// Footer / status
	if m.status != "" && time.Now().Before(m.statusUntil) {
		fmt.Fprintf(&b, "\n%s\n", dimStyle.Render(m.status))
	} else {
		fmt.Fprintf(&b, "\n%s\n", dimStyle.Render("R refresh · template: "+m.template.String()))
	}

	return b.String()
}

func (m model) previewText() string {
	switch m.mode {
	case modeSessions:
		name := m.currentSessionName()
		if name == "" {
			// When no match is selected, preview the "would create" name (sessionx-like).
			q := strings.TrimSpace(m.input.Value())
			if q == "" {
				return ""
			}
			sn := sanitizeSessionName(q)
			if sn == "" {
				return "new session:\n  (invalid name)"
			}
			exists, _ := tmuxHasSession(sn)
			if exists {
				return "new session:\n  (already exists) " + sn
			}
			return "new session:\n  " + sn
		}

		out, err := tmuxCaptureSessionSummary(name)
		if err != nil {
			return "preview error: " + err.Error()
		}

		if tail, terr := tmuxCaptureSessionActivePaneTail(name, clampInt(m.opts.PreviewLines, 5, 40)); terr == nil && strings.TrimSpace(tail) != "" {
			return out + "\n\npane tail:\n" + strings.TrimRight(tail, "\n")
		}
		return out

	case modeProjects:
		p := m.currentProject()
		if p.Path == "" {
			return ""
		}

		// Show "execution path" preview:
		// - spec presence (yaml/json) (only if PreferProjectSpec is enabled)
		// - safety mode (actions-only vs shell enabled vs tmux passthrough)
		// - dry-run plan (what will run) if spec exists
		var b strings.Builder
		b.WriteString(projectPreview(p.Path))
		b.WriteString("\n\nexecution:\n")
		if m.opts.DryRun {
			b.WriteString(" - mode: dry-run (no tmux mutations)\n")
		} else {
			b.WriteString(" - mode: apply\n")
		}

		b.WriteString("\nproject spec:\n")
		if !m.opts.PreferProjectSpec {
			b.WriteString(" - disabled (PreferProjectSpec=false)\n")
			b.WriteString(" - using built-in template: " + m.template.String() + "\n")
			b.WriteString("\nplanned operations:\n")
			b.WriteString(renderHardcodedTemplatePlan(sanitizeSessionName(p.Name), p.Path, m.template))
			return b.String()
		}

		s, specPath, ok, err := spec.LoadProjectLocalWithNames(p.Path, m.opts.ProjectSpecNames)
		if err != nil {
			b.WriteString(" - error: " + err.Error() + "\n")
			return b.String()
		}
		if !ok {
			b.WriteString(" - none (fallback: built-in template)\n")
			b.WriteString(" - template: " + m.template.String() + "\n")
			b.WriteString("\nplanned operations:\n")
			b.WriteString(renderHardcodedTemplatePlan(sanitizeSessionName(p.Name), p.Path, m.template))
			return b.String()
		}

		b.WriteString(" - " + specPath + "\n")
		b.WriteString(" - safety: actions-only\n")
		if m.opts.AllowShell {
			b.WriteString(" - safety override: shell commands ENABLED (TMUX_SESSION_MANAGER_ALLOW_SHELL=1)\n")
		}
		if m.opts.AllowTmuxPassthrough {
			b.WriteString(" - safety override: tmux passthrough ENABLED (TMUX_SESSION_MANAGER_ALLOW_TMUX_PASSTHROUGH=1)\n")
		}

		sessionName := sanitizeSessionName(p.Name)
		if sessionName == "" {
			sessionName = "project"
		}

		pol := spec.DefaultPolicy()
		pol.AllowShell = m.opts.AllowShell
		pol.AllowTmuxPassthrough = m.opts.AllowTmuxPassthrough
		if verr := s.ValidatePolicy(pol); verr != nil {
			b.WriteString("\n(spec invalid: " + verr.Error() + ")\n")
			return b.String()
		}

		eng := templates.NewEngine()
		eng.Policy.AllowShell = m.opts.AllowShell
		eng.Policy.AllowTmuxPassthrough = m.opts.AllowTmuxPassthrough

		ctx := templates.Context{
			ProjectName: p.Name,
			ProjectPath: p.Path,
			SessionName: sessionName,
			WorkingDir:  p.Path,
			Env:         s.Env,
		}

		ts, terr := templates.FromSpec(
			ctx,
			*s,
			m.opts.AllowShell,
			m.opts.AllowTmuxPassthrough,
			false, // includeEnsureSession (preview assumes TUI creates session first)
		)
		if terr != nil {
			b.WriteString("\n(spec compile error: " + terr.Error() + ")\n")
			return b.String()
		}

		compiled, cerr := eng.Compile(ctx, ts)
		if cerr != nil {
			b.WriteString("\n(spec compile error: " + cerr.Error() + ")\n")
			return b.String()
		}

		b.WriteString("\nplanned operations:\n")
		for _, line := range templates.DryRunLines(compiled) {
			b.WriteString(line + "\n")
		}
		return strings.TrimRight(b.String(), "\n")

	default:
		return ""
	}
}

// ---------- tmux helpers ----------

func tmuxListSessions() ([]sessionItem, error) {
	// Use a stable format to parse:
	// name|windows|attached
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}|#{session_windows}|#{?session_attached,1,0}")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var items []sessionItem
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		parts := strings.Split(ln, "|")
		it := sessionItem{RawLine: ln}
		if len(parts) > 0 {
			it.Name = parts[0]
		}
		if len(parts) > 1 {
			it.Windows = atoiSafe(parts[1])
		}
		if len(parts) > 2 {
			it.Attached = strings.TrimSpace(parts[2]) == "1"
		}
		if it.Name != "" {
			items = append(items, it)
		}
	}
	// Sort by name for determinism.
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func tmuxHasSession(name string) (bool, error) {
	if strings.TrimSpace(name) == "" {
		return false, nil
	}
	err := exec.Command("tmux", "has-session", "-t", name).Run()
	if err == nil {
		return true, nil
	}
	// tmux returns exit 1 when not found.
	return false, nil
}

func tmuxSwitchClient(name string) error {
	return exec.Command("tmux", "switch-client", "-t", name).Run()
}

func tmuxNewSessionDetached(name string, dir string) error {
	args := []string{"new-session", "-d", "-s", name}
	if strings.TrimSpace(dir) != "" {
		args = append(args, "-c", dir)
	}
	return exec.Command("tmux", args...).Run()
}

// ---------- edit mode: snapshot current session + new session in current dir ----------

func (m model) editNewSessionInCurrentDir() (tea.Model, tea.Cmd) {

	curSession, _ := tmuxCurrentSessionName()
	curDir, _ := tmuxCurrentPanePath()
	if strings.TrimSpace(curDir) == "" {
		m.setStatus("edit: could not determine current dir", 2000*time.Millisecond)
		return m, nil
	}

	// Snapshot first.
	var snapPath string
	if strings.TrimSpace(curSession) != "" {
		if p, err := snapshotSessionToSpecFile(curSession); err == nil && p != "" {
			snapPath = p
		}
	}

	// Create a new session name derived from dir basename.
	base := filepath.Base(strings.TrimRight(curDir, string(filepath.Separator)))
	newName := sanitizeSessionName(base)
	if newName == "" {
		newName = "edit"
	}
	// Avoid collision by suffixing _2, _3, ...
	newName = makeUniqueSessionName(newName, 50)

	if err := tmuxNewSessionDetached(newName, curDir); err != nil {
		m.setStatus("edit: create failed: "+err.Error(), 2500*time.Millisecond)
		return m, nil
	}

	// Open editor in the new session.
	// Prefer env var already supported by config layer; fallback to nvim.
	editor := strings.TrimSpace(os.Getenv("TMUX_SESSION_MANAGER_EDITOR_CMD"))
	if editor == "" {
		editor = "nvim ."
	}
	_ = exec.Command("tmux", "send-keys", "-t", newName+":", editor, "Enter").Run()

	if err := tmuxSwitchClient(newName); err != nil {
		m.setStatus("edit: switch failed: "+err.Error(), 2500*time.Millisecond)
		return m, nil
	}

	if snapPath != "" {
		m.setStatus("edit: created "+newName+" (snapshot: "+snapPath+")", 2200*time.Millisecond)
	} else if strings.TrimSpace(curSession) != "" {
		m.setStatus("edit: created "+newName+" (snapshot failed)", 2200*time.Millisecond)
	} else {
		m.setStatus("edit: created "+newName, 1800*time.Millisecond)
	}

	return m, tea.Quit
}

func tmuxCurrentPanePath() (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-F", "#{pane_current_path}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func tmuxCurrentSessionName() (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-F", "#{session_name}").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func makeUniqueSessionName(base string, maxTries int) string {
	base = sanitizeSessionName(base)
	if base == "" {
		base = "session"
	}
	// If not exists, use base.
	exists, _ := tmuxHasSession(base)
	if !exists {
		return base
	}
	for i := 2; i <= maxTries; i++ {
		try := fmt.Sprintf("%s_%d", base, i)
		exists, _ := tmuxHasSession(try)
		if !exists {
			return try
		}
	}
	// last resort: include timestamp-ish suffix
	return fmt.Sprintf("%s_%d", base, time.Now().Unix())
}

func snapshotSessionToSpecFile(sessionName string) (string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "", errors.New("snapshot: empty session name")
	}

	home, _ := os.UserHomeDir()
	if strings.TrimSpace(home) == "" {
		return "", errors.New("snapshot: no home dir")
	}

	dir := filepath.Join(home, ".config", "tmux-session-manager", "snapshots")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("snapshot: mkdir: %w", err)
	}

	ts := time.Now().Format("20060102-150405")
	fileName := fmt.Sprintf("%s.%s.tmux-session.yaml", sanitizeSessionName(sessionName), ts)
	outPath := filepath.Join(dir, fileName)

	specText, err := tmuxSnapshotAsSpecYAML(sessionName)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(outPath, []byte(specText), defaultSnapshotFileMode); err != nil {
		return "", fmt.Errorf("snapshot: write: %w", err)
	}

	return outPath, nil
}

// tmuxSnapshotAsSpecYAML builds a tmux-session-manager spec that rehydrates the session shape
// (windows, layouts, pane cwd, and current command).
func tmuxSnapshotAsSpecYAML(sessionName string) (string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "", errors.New("snapshot: empty session name")
	}

	// Get windows (index, name, layout).
	// Use "index|name|layout".
	wOut, err := exec.Command(
		"tmux",
		"list-windows",
		"-t", sessionName,
		"-F", "#{window_index}|#{window_name}|#{window_layout}",
	).Output()
	if err != nil {
		return "", fmt.Errorf("snapshot: list-windows: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(wOut)), "\n")

	// Start YAML
	var b strings.Builder
	b.WriteString("version: 1\n")
	b.WriteString("session:\n")
	b.WriteString("  name: \"" + escapeYAMLString(sessionName) + "\"\n")
	b.WriteString("  root: \"${PROJECT_PATH}\"\n")
	b.WriteString("  attach: true\n")
	b.WriteString("  switch_client: true\n")
	b.WriteString("\n")
	b.WriteString("windows:\n")

	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		parts := strings.SplitN(ln, "|", 3)
		if len(parts) < 3 {
			continue
		}
		wIdx := strings.TrimSpace(parts[0])
		wName := strings.TrimSpace(parts[1])
		wLayout := strings.TrimSpace(parts[2])

		// panes: pane_index|pane_title|pane_current_path|pane_current_command
		pOut, pErr := exec.Command(
			"tmux",
			"list-panes",
			"-t", sessionName+":"+wIdx,
			"-F", "#{pane_index}|#{pane_title}|#{pane_current_path}|#{pane_current_command}",
		).Output()
		if pErr != nil {
			// Keep going; emit window without panes.
			pOut = []byte{}
		}
		pLines := strings.Split(strings.TrimSpace(string(pOut)), "\n")

		b.WriteString("  - name: \"" + escapeYAMLString(wName) + "\"\n")
		b.WriteString("    root: \"${PROJECT_PATH}\"\n")
		if wLayout != "" {
			b.WriteString("    layout: \"" + escapeYAMLString(wLayout) + "\"\n")
		}
		b.WriteString("    panes:\n")

		for _, pl := range pLines {
			pl = strings.TrimSpace(pl)
			if pl == "" {
				continue
			}
			pp := strings.SplitN(pl, "|", 4)
			if len(pp) < 4 {
				continue
			}
			pTitle := strings.TrimSpace(pp[1])
			pCwd := strings.TrimSpace(pp[2])
			pCmd := strings.TrimSpace(pp[3])

			b.WriteString("      - name: \"" + escapeYAMLString(pTitle) + "\"\n")
			if pCwd != "" {
				b.WriteString("        root: \"" + escapeYAMLString(pCwd) + "\"\n")
			}

			_ = pCmd
		}
	}

	return b.String(), nil
}

func escapeYAMLString(s string) string {
	// Minimal escape for double-quoted YAML scalars.
	// Replace backslash and double quote; normalize newlines to spaces.
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func tmuxKillSession(name string) error {
	return exec.Command("tmux", "kill-session", "-t", name).Run()
}

func tmuxRenameSession(from, to string) error {
	return exec.Command("tmux", "rename-session", "-t", from, to).Run()
}

func tmuxCaptureSessionSummary(name string) (string, error) {
	// Provide a human-friendly summary:
	// - windows list with active marker
	// - active window/pane current path
	var b strings.Builder

	wOut, err := exec.Command("tmux", "list-windows", "-t", name, "-F", "#{window_index}:#{window_name} #{?window_active,*, } [#{window_panes} panes] (#{window_layout})").Output()
	if err != nil {
		return "", err
	}
	b.WriteString("windows:\n")
	b.WriteString(strings.TrimRight(string(wOut), "\n"))
	b.WriteString("\n")

	pOut, err := exec.Command("tmux", "display-message", "-p", "-t", name, "active: #{session_name}:#{window_index}.#{pane_index}  path=#{pane_current_path}  cmd=#{pane_current_command}").Output()
	if err == nil {
		b.WriteString("\n")
		b.WriteString(strings.TrimRight(string(pOut), "\n"))
	}
	return b.String(), nil
}

// tmuxCaptureSessionActivePaneTail captures the tail of the active pane for preview.
func tmuxCaptureSessionActivePaneTail(sessionName string, lines int) (string, error) {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return "", fmt.Errorf("empty session name")
	}
	if lines <= 0 {
		lines = 20
	}

	// Capture last N lines from the active pane in the session.
	// Targeting "-t <sessionName>" will resolve to the session's current window/pane.
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", sessionName, "-S", fmt.Sprintf("-%d", lines)).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ---------- templates ----------

func applyTemplate(sessionName, projectDir string, tpl templateKind) error {
	// Minimal, useful first pass:
	// - Ensure we have 2 windows: "editor" and "server"/"repl"
	// - Start in projectDir
	//
	// This intentionally avoids external tools and relies on common defaults.
	switch tpl {
	case tplNode:
		return applyNodeTemplate(sessionName, projectDir)
	case tplPython:
		return applyPythonTemplate(sessionName, projectDir)
	case tplGo:
		return applyGoTemplate(sessionName, projectDir)
	default:
		return nil
	}
}

func applyNodeTemplate(sessionName, dir string) error {
	_ = exec.Command("tmux", "rename-window", "-t", sessionName+":", "editor").Run()
	_ = exec.Command("tmux", "split-window", "-t", sessionName+":editor", "-h", "-c", dir).Run()

	// Create server window
	_ = exec.Command("tmux", "new-window", "-t", sessionName, "-n", "server", "-c", dir).Run()

	cmd := detectNodeDevCommand(dir)
	if cmd != "" {
		_ = exec.Command("tmux", "send-keys", "-t", sessionName+":server", cmd, "Enter").Run()
	}
	return nil
}

func applyPythonTemplate(sessionName, dir string) error {
	_ = exec.Command("tmux", "rename-window", "-t", sessionName+":", "editor").Run()
	_ = exec.Command("tmux", "split-window", "-t", sessionName+":editor", "-h", "-c", dir).Run()

	_ = exec.Command("tmux", "new-window", "-t", sessionName, "-n", "repl", "-c", dir).Run()
	_ = exec.Command("tmux", "send-keys", "-t", sessionName+":repl", "python", "Enter").Run()
	return nil
}

func applyGoTemplate(sessionName, dir string) error {
	_ = exec.Command("tmux", "rename-window", "-t", sessionName+":", "editor").Run()
	_ = exec.Command("tmux", "split-window", "-t", sessionName+":editor", "-h", "-c", dir).Run()

	_ = exec.Command("tmux", "new-window", "-t", sessionName, "-n", "run", "-c", dir).Run()
	_ = exec.Command("tmux", "send-keys", "-t", sessionName+":run", "go test ./...", "Enter").Run()
	return nil
}

// detectNodeDevCommand picks a common dev command based on lockfiles.
// This is intentionally simple; it should be extended later with env-config overrides.
func detectNodeDevCommand(projectDir string) string {
	if fileExists(filepath.Join(projectDir, "pnpm-lock.yaml")) {
		return "pnpm dev"
	}
	if fileExists(filepath.Join(projectDir, "yarn.lock")) {
		return "yarn dev"
	}
	if fileExists(filepath.Join(projectDir, "package-lock.json")) {
		return "npm run dev"
	}
	if fileExists(filepath.Join(projectDir, "package.json")) {
		return "npm run dev"
	}
	return ""
}

// ---------- misc helpers ----------

// ---------- projects scanning / preview ----------

func scanProjects(roots []string, depth int) []projectItem {
	seen := map[string]bool{}
	var out []projectItem

	for _, root := range roots {
		root = expandHome(root)
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		walkProjects(root, root, depth, &out, seen)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func walkProjects(root, dir string, depth int, out *[]projectItem, seen map[string]bool) {
	if depth < 0 {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	// A directory is considered a project if it contains one of these markers.
	if dir != root && isProjectDir(dir, ents) {
		name := filepath.Base(dir)
		if !seen[dir] {
			seen[dir] = true
			*out = append(*out, projectItem{Name: name, Path: dir})
		}
		// Do not descend further once we identify a project directory.
		return
	}

	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor" || n == ".git" {
			continue
		}
		walkProjects(root, filepath.Join(dir, n), depth-1, out, seen)
	}
}

func isProjectDir(dir string, ents []os.DirEntry) bool {
	has := func(name string) bool {
		for _, e := range ents {
			if e.Name() == name {
				return true
			}
		}
		return false
	}
	// Common markers
	if has(".git") {
		return true
	}
	if has("go.mod") || has("go.work") {
		return true
	}
	if has("pyproject.toml") || has("requirements.txt") {
		return true
	}
	if has("package.json") {
		return true
	}

	// tmux-session-manager project spec markers
	// If a repo has a project-local session spec, treat it as a project even if it doesn't
	// have language markers (or uses a git worktree where ".git" may not be a directory).
	if has(".tmux-session.yaml") || has(".tmux-session.yml") || has(".tmux-session.json") {
		return true
	}

	return false
}

func projectPreview(dir string) string {
	var b strings.Builder
	b.WriteString("path: " + dir + "\n")

	ents, err := os.ReadDir(dir)
	if err != nil {
		return b.String() + "error: " + err.Error()
	}

	// Show a few interesting files.
	interesting := []string{"README.md", "go.mod", "pyproject.toml", "requirements.txt", "package.json"}
	for _, f := range interesting {
		if fileExists(filepath.Join(dir, f)) {
			b.WriteString(" - " + f + "\n")
		}
	}

	// Show top-level folders.
	var dirs []string
	for _, e := range ents {
		if e.IsDir() {
			n := e.Name()
			if strings.HasPrefix(n, ".") {
				continue
			}
			dirs = append(dirs, n)
		}
	}
	sort.Strings(dirs)
	if len(dirs) > 0 {
		b.WriteString("\nfolders:\n")
		max := minIntTUI(len(dirs), 10)
		for i := 0; i < max; i++ {
			b.WriteString(" - " + dirs[i] + "\n")
		}
		if len(dirs) > max {
			b.WriteString(fmt.Sprintf(" - ... (%d more)\n", len(dirs)-max))
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// ---------- misc helpers ----------

func fuzzyContains(hay, needle string) bool {
	// Simple ordered-subsequence match. Fast and "good enough" for first pass.
	if needle == "" {
		return true
	}
	i := 0
	for _, r := range hay {
		if i >= len(needle) {
			break
		}
		if byte(r) == needle[i] {
			i++
		}
	}
	return i == len(needle)
}

func sanitizeSessionName(name string) string {
	// tmux session names are fairly permissive, but spaces and punctuation cause friction.
	// Keep it simple and consistent.
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
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
	// Avoid tmux weirdness with empty/dot names
	out = strings.Trim(out, ".")
	return out
}

func atoiSafe(s string) int {
	s = strings.TrimSpace(s)
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func dropLastRune(s string) string {
	if s == "" {
		return s
	}
	rs := []rune(s)
	return string(rs[:len(rs)-1])
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minIntTUI(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func parseEnvBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return def
	}
}

func renderHardcodedTemplatePlan(sessionName, projectDir string, tpl templateKind) string {
	// Session is created by caller, so show template operations only.
	if sessionName == "" {
		sessionName = "project"
	}
	var b strings.Builder
	switch tpl {
	case tplNode:
		b.WriteString(" - rename-window -t " + sessionName + ":0 editor\n")
		b.WriteString(" - split-window -t " + sessionName + ":0 -h -c " + projectDir + "\n")
		b.WriteString(" - new-window -t " + sessionName + " -n server -c " + projectDir + "\n")
		b.WriteString(" - send-keys -t " + sessionName + ":server.0 <detect dev cmd> Enter\n")
	case tplPython:
		b.WriteString(" - rename-window -t " + sessionName + ":0 editor\n")
		b.WriteString(" - split-window -t " + sessionName + ":0 -h -c " + projectDir + "\n")
		b.WriteString(" - new-window -t " + sessionName + " -n repl -c " + projectDir + "\n")
		b.WriteString(" - send-keys -t " + sessionName + ":repl.0 python Enter\n")
	case tplGo:
		b.WriteString(" - rename-window -t " + sessionName + ":0 editor\n")
		b.WriteString(" - split-window -t " + sessionName + ":0 -h -c " + projectDir + "\n")
		b.WriteString(" - new-window -t " + sessionName + " -n run -c " + projectDir + "\n")
		b.WriteString(" - send-keys -t " + sessionName + ":run.0 go test ./... Enter\n")
	default:
		b.WriteString(" - (empty template)\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
