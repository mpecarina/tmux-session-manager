package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	core "tmux-session-manager/pkg/manager"
	"tmux-session-manager/pkg/spec"
	"tmux-session-manager/pkg/templates"
)

var (
	flagConfigPath        string
	flagPreferProjectSpec bool
	flagProjectSpecNames  string

	flagSpecPath    string
	flagSpecSession string
	flagSpecCwd     string

	flagProjectName string

	flagBootstrap            bool
	flagBootstrapInitSession string

	flagAllowShell           bool
	flagAllowTmuxPassthrough bool

	flagInitialQuery string
	flagMaxResults   int
	flagLaunchMode   string
	flagKeyBind      string

	flagRoots string
	flagDepth int

	flagTemplate string
	flagDryRun   bool
)

func init() {
	flag.StringVar(&flagConfigPath, "config", "", "Path to global config file (optional)")
	flag.BoolVar(&flagPreferProjectSpec, "prefer-project-spec", true, "Prefer project-local session spec over built-in templates")
	flag.StringVar(&flagProjectSpecNames, "project-spec-names", ".tmux-session.yaml,.tmux-session.yml,.tmux-session.json", "Comma-separated project-local spec filenames to look for")

	flag.StringVar(&flagSpecPath, "spec", "", "Apply a spec file directly (.yaml/.yml/.json); skips project discovery")
	flag.StringVar(&flagSpecSession, "spec-session", "", "Override tmux session name when applying --spec")
	flag.StringVar(&flagSpecCwd, "spec-cwd", "", "Working directory for applying --spec (resolves relative paths)")

	flag.StringVar(&flagProjectName, "project", "", "Apply a project by name by resolving <root>/<project>/.tmux-session.(yaml|yml|json) under --roots")

	flag.BoolVar(&flagBootstrap, "bootstrap", false, "When run outside tmux with --project/--spec, start/attach tmux and re-run inside it (opt-in)")
	flag.StringVar(&flagBootstrapInitSession, "bootstrap-init-session", "", "INTERNAL: bootstrap init session name")

	flag.BoolVar(&flagAllowShell, "allow-shell", false, "Allow specs/templates to execute shell commands (unsafe; opt-in)")
	flag.BoolVar(&flagAllowTmuxPassthrough, "allow-tmux-passthrough", false, "Allow specs/templates to run raw tmux commands (advanced; opt-in)")

	flag.StringVar(&flagInitialQuery, "query", "", "Initial query for the TUI selector")
	flag.IntVar(&flagMaxResults, "max", 30, "Maximum results to display in the TUI (0 uses default)")
	flag.StringVar(&flagLaunchMode, "launch-mode", "", "Launch mode hint for tmux launcher: window|popup")
	flag.StringVar(&flagKeyBind, "print-bind", "", "Print a suggested tmux binding line and exit")

	flag.StringVar(&flagRoots, "roots", "", "Comma-separated roots to scan for projects (default: ~/code,~/src,~/projects)")
	flag.IntVar(&flagDepth, "depth", 2, "Project scan depth under roots")
	flag.StringVar(&flagTemplate, "template", "", "Default template in TUI: auto|empty|node|python|go")

	flag.BoolVar(&flagDryRun, "dry-run", false, "Dry-run: show planned operations and do not execute")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "tmux-session-manager\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  tmux-session-manager [options]\n\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  tmux-session-manager\n")
		fmt.Fprintf(os.Stderr, "  tmux-session-manager --project vmlab\n")
		fmt.Fprintf(os.Stderr, "  tmux-session-manager --spec /path/to/.tmux-session.yaml\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()

	if strings.TrimSpace(flagBootstrapInitSession) != "" && strings.TrimSpace(os.Getenv("TMUX_SESSION_MANAGER_INIT_SESSION")) == "" {
		_ = os.Setenv("TMUX_SESSION_MANAGER_INIT_SESSION", strings.TrimSpace(flagBootstrapInitSession))
	}

	if flagKeyBind != "" {
		printSuggestedBind(flagKeyBind)
		return
	}

	outsideTmux := strings.TrimSpace(os.Getenv("TMUX")) == ""
	explicitIntent := strings.TrimSpace(flagProjectName) != "" || strings.TrimSpace(flagSpecPath) != ""
	bootstrapped := strings.TrimSpace(os.Getenv("TMUX_SESSION_MANAGER_BOOTSTRAPPED")) != ""
	bootstrapEnabled := flagBootstrap || parseEnvBool("TMUX_SESSION_MANAGER_BOOTSTRAP", false)

	if outsideTmux && explicitIntent && !bootstrapped {
		if bootstrapEnabled {
			self, err := os.Executable()
			if err == nil && strings.TrimSpace(self) != "" {
				initSession := "__tsm_init__"

				innerArgs := append([]string{}, os.Args[1:]...)
				innerArgs = append(innerArgs, "--bootstrap-init-session", initSession)

				sh := os.Getenv("SHELL")
				if strings.TrimSpace(sh) == "" {
					sh = "sh"
				}

				cmdStr := "export TMUX_SESSION_MANAGER_BOOTSTRAPPED=1; " + shellQuote(self) + " " + shellJoin(innerArgs) + "; exec " + shellQuote(sh)

				cmdArgs := []string{
					"new-session", "-A", "-s", initSession,
					"-c", ".",
					"--",
					sh, "-lc", cmdStr,
				}

				cmd := exec.Command("tmux", cmdArgs...)

				cmd.Env = os.Environ()
				cmd.Stdin = os.Stdin
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr

				if err := cmd.Run(); err == nil {
					return
				}

				// Fall through to normal behavior if tmux isn't reachable or attach fails.
			}
		} else {
			fmt.Fprintln(os.Stderr, "tmux-session-manager: not inside tmux. Re-run with --bootstrap (or set TMUX_SESSION_MANAGER_BOOTSTRAP=1).")
			os.Exit(1)
		}
	}

	if strings.TrimSpace(flagProjectName) != "" && strings.TrimSpace(flagSpecPath) == "" {
		project := strings.TrimSpace(flagProjectName)

		roots := splitAndTrim(os.Getenv("TMUX_SESSION_MANAGER_ROOTS"))
		if len(roots) == 0 {
			roots = splitAndTrim(flagRoots)
		}
		if len(roots) == 0 {
			home, _ := os.UserHomeDir()
			roots = []string{
				filepath.Join(home, "code"),
				filepath.Join(home, "src"),
				filepath.Join(home, "projects"),
			}
		}

		var resolvedSpec string
		var resolvedCwd string
		candidates := []string{".tmux-session.yaml", ".tmux-session.yml", ".tmux-session.json"}
		for _, r := range roots {
			r = expandHome(r)
			cwd := filepath.Join(r, project)
			for _, nm := range candidates {
				p := filepath.Join(cwd, nm)
				if st, err := os.Stat(p); err == nil && st != nil && !st.IsDir() {
					resolvedSpec = p
					resolvedCwd = cwd
					break
				}
			}
			if resolvedSpec != "" {
				break
			}
		}

		if resolvedSpec == "" {
			fmt.Fprintf(os.Stderr, "tmux-session-manager: --project %q: no .tmux-session.{yaml,yml,json} found under roots\n", project)
			os.Exit(1)
		}

		flagSpecPath = resolvedSpec
		if strings.TrimSpace(flagSpecCwd) == "" {
			flagSpecCwd = resolvedCwd
		}
		if strings.TrimSpace(flagSpecSession) == "" {
			flagSpecSession = project
		}
	}

	if strings.TrimSpace(flagSpecPath) != "" {
		specPath := expandHome(flagSpecPath)

		specCwd := strings.TrimSpace(flagSpecCwd)
		if specCwd == "" {
			specCwd = filepath.Dir(specPath)
		}
		specCwd = expandHome(specCwd)

		// Load spec directly from file path (do not rely on "project-local" lookup semantics here).
		loadedSpec, loadErr := spec.LoadFile(specPath)
		if loadErr != nil {
			fmt.Fprintf(os.Stderr, "tmux-session-manager: load spec: %v\n", loadErr)
			os.Exit(1)
		}

		// Determine effective attach/switch behavior (defaults: true).
		shouldAttach := true
		if loadedSpec.Session.Attach != nil {
			shouldAttach = *loadedSpec.Session.Attach
		}
		shouldSwitchClient := true
		if loadedSpec.Session.SwitchClient != nil {
			shouldSwitchClient = *loadedSpec.Session.SwitchClient
		}

		sessionName := strings.TrimSpace(flagSpecSession)
		if sessionName == "" {
			sessionName = filepath.Base(strings.TrimRight(specCwd, string(filepath.Separator)))
			sessionName = strings.TrimSpace(sessionName)
		}
		if sessionName == "" {
			fmt.Fprintf(os.Stderr, "tmux-session-manager: --spec requires --spec-session (or a non-empty --spec-cwd)\n")
			os.Exit(1)
		}

		if strings.TrimSpace(os.Getenv("TMUX")) != "" {
			if err := exec.Command("tmux", "has-session", "-t", sessionName).Run(); err != nil {
				_ = exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-c", specCwd).Run()
			}
		}

		opt := core.ApplySpecOptions{
			ProjectPath: specCwd,
			SessionName: sessionName,

			AllowShell:           parseEnvBool("TMUX_SESSION_MANAGER_ALLOW_SHELL", flagAllowShell),
			AllowTmuxPassthrough: parseEnvBool("TMUX_SESSION_MANAGER_ALLOW_TMUX_PASSTHROUGH", flagAllowTmuxPassthrough),

			IncludeEnsureSession: false,
			DryRun:               flagDryRun,
			Runner:               &templates.TmuxExecRunner{},
		}

		res, err := core.ApplySpecFile(specPath, opt)
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "no server running on ") ||
				strings.Contains(msg, "no server running") ||
				strings.Contains(msg, "server exited") ||
				strings.Contains(msg, "lost server") {
				fmt.Fprintln(os.Stderr, "tmux-session-manager: tmux server exited; stopping")
				os.Exit(0)
			}

			fmt.Fprintf(os.Stderr, "tmux-session-manager: %v\n", err)
			os.Exit(exitCodeFromErr(err))
		}

		if !flagDryRun {
			specNames := map[string]struct{}{}
			for _, w := range loadedSpec.Windows {
				n := strings.TrimSpace(w.Name)
				if n != "" {
					specNames[n] = struct{}{}
				}
			}

			baseIndex := 0
			if out, e := exec.Command("tmux", "show-option", "-gqv", "base-index").Output(); e == nil {
				if n, ne := strconv.Atoi(strings.TrimSpace(string(out))); ne == nil {
					baseIndex = n
				}
			}

			baseWinName, _ := exec.Command(
				"tmux",
				"display-message",
				"-p",
				"-t",
				fmt.Sprintf("%s:%d", sessionName, baseIndex),
				"#{window_name}",
			).Output()

			baseWinNameStr := strings.TrimSpace(string(baseWinName))
			if baseWinNameStr != "" {
				if _, isSpec := specNames[baseWinNameStr]; !isSpec {
					_ = exec.Command("tmux", "kill-window", "-t", fmt.Sprintf("%s:%d", sessionName, baseIndex)).Run()
				}

				// (moved) runConnectSubcommand is now defined at package scope.
			}
		}

		// Dry-run prints the plan for inspection.
		if flagDryRun {
			for _, ln := range res.DryRunLines {
				fmt.Println(ln)
			}
			return
		}

		if shouldAttach {
			if strings.TrimSpace(os.Getenv("TMUX")) != "" {
				if shouldSwitchClient {
					if err := exec.Command("tmux", "switch-client", "-t", sessionName).Run(); err != nil {
						fmt.Fprintf(os.Stderr, "tmux-session-manager: switch-client failed: %v\n", err)
						os.Exit(1)
					}
				}

				initSession := strings.TrimSpace(os.Getenv("TMUX_SESSION_MANAGER_INIT_SESSION"))
				if initSession == "" {
				}
				if initSession != "" && initSession != sessionName {
					_ = exec.Command("tmux", "kill-session", "-t", initSession).Run()
				}
			} else {
				return
			}
		}

		return
	}

	// Resolve runtime defaults from env (populated by the launcher from tmux options),
	// then let explicit CLI flags override.
	//
	// Launcher sets (examples):
	//   TMUX_SESSION_MANAGER_ROOTS
	//   TMUX_SESSION_MANAGER_PROJECT_DEPTH
	//   TMUX_SESSION_MANAGER_SPEC_NAMES
	//   TMUX_SESSION_MANAGER_PREFER_PROJECT_SPEC
	//   TMUX_SESSION_MANAGER_DEFAULT_TEMPLATE
	//   TMUX_SESSION_MANAGER_ALLOW_SHELL
	//   TMUX_SESSION_MANAGER_ALLOW_TMUX_PASSTHROUGH
	//   TMUX_SESSION_MANAGER_LAUNCH_MODE
	//
	envRoots := splitAndTrim(os.Getenv("TMUX_SESSION_MANAGER_ROOTS"))
	if len(envRoots) == 0 {
		// Fall back to flag (or defaults inside UI if still empty).
		envRoots = splitAndTrim(flagRoots)
	}

	envSpecNames := splitAndTrim(os.Getenv("TMUX_SESSION_MANAGER_SPEC_NAMES"))
	if len(envSpecNames) == 0 {
		envSpecNames = splitAndTrim(flagProjectSpecNames)
	}

	envPreferSpec := parseEnvBool("TMUX_SESSION_MANAGER_PREFER_PROJECT_SPEC", flagPreferProjectSpec)
	envAllowShell := parseEnvBool("TMUX_SESSION_MANAGER_ALLOW_SHELL", flagAllowShell)
	envAllowTmux := parseEnvBool("TMUX_SESSION_MANAGER_ALLOW_TMUX_PASSTHROUGH", flagAllowTmuxPassthrough)

	envLaunchMode := strings.TrimSpace(os.Getenv("TMUX_SESSION_MANAGER_LAUNCH_MODE"))
	if envLaunchMode == "" {
		envLaunchMode = strings.TrimSpace(flagLaunchMode)
	}

	envDefaultTemplate := strings.TrimSpace(os.Getenv("TMUX_SESSION_MANAGER_DEFAULT_TEMPLATE"))
	if envDefaultTemplate == "" {
		envDefaultTemplate = strings.TrimSpace(flagTemplate)
	}

	envDepth := parseEnvInt("TMUX_SESSION_MANAGER_PROJECT_DEPTH", flagDepth)

	// CLI flags (when set) override env.
	// Roots: if user supplied --roots explicitly, use it. Otherwise use env.
	finalRoots := envRoots
	if strings.TrimSpace(flagRoots) != "" {
		finalRoots = splitAndTrim(flagRoots)
	}

	finalSpecNames := envSpecNames
	if strings.TrimSpace(flagProjectSpecNames) != "" {
		// Note: we always have a default value for this flag; treat env as default source,
		// but allow explicit override when user passes a different value.
		finalSpecNames = splitAndTrim(flagProjectSpecNames)
	}

	finalTemplate := envDefaultTemplate
	if strings.TrimSpace(flagTemplate) != "" {
		finalTemplate = strings.TrimSpace(flagTemplate)
	}

	finalLaunchMode := envLaunchMode
	if strings.TrimSpace(flagLaunchMode) != "" {
		finalLaunchMode = strings.TrimSpace(flagLaunchMode)
	}

	finalDepth := envDepth
	if flagDepth != 0 {
		finalDepth = flagDepth
	}

	opts := core.UIOptions{
		InitialQuery:    flagInitialQuery,
		LaunchMode:      finalLaunchMode,
		ProjectsPaths:   finalRoots,
		MaxResults:      flagMaxResults,
		DefaultTemplate: finalTemplate,

		ProjectSpecNames:  finalSpecNames,
		PreferProjectSpec: envPreferSpec,

		AllowShell:           envAllowShell,
		AllowTmuxPassthrough: envAllowTmux,
		DryRun:               flagDryRun,

		ProjectScanDepth: finalDepth,
	}

	_ = flagConfigPath // reserved for a future global config loader

	if err := core.RunTUI(opts); err != nil {
		fmt.Fprintf(os.Stderr, "tmux-session-manager: %v\n", err)
		os.Exit(exitCodeFromErr(err))
	}
}

func printSuggestedBind(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		fmt.Println("bind-key <key> run-shell \"~/.tmux/plugins/tmux-session-manager/scripts/tmux_session_manager.tmux\"")
		return
	}
	fmt.Printf("bind-key %s run-shell \"~/.tmux/plugins/tmux-session-manager/scripts/tmux_session_manager.tmux\"\n", shellEscapeForTmuxBind(key))
}

func shellEscapeForTmuxBind(s string) string {
	// tmux bind-key takes key tokens, not arbitrary shell strings.
	// For common single-key binds we just pass through; callers can supply "C-s", "M-o", etc.
	return strings.TrimSpace(s)
}

func splitAndTrim(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
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
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		if home == "" {
			return p
		}
		return filepath.Join(home, p[2:])
	}
	return p
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

func parseEnvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// shellJoin renders args into a shell-safe command string.
// This is used for bootstrap re-exec via `SHELL -lc "<cmd>"`.
func shellJoin(args []string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellQuote returns a POSIX shell-safe single-quoted string, escaping embedded single quotes.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Use single quotes; escape any embedded single quote by closing/opening:
	//   abc'def  ->  'abc'"'"'def'
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	return 1
}
