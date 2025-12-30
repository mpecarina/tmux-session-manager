package templates

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// TmuxExecRunner executes tmux commands by invoking the `tmux` binary.
//
// Notes:
//   - This runner does NOT prepend "tmux" into args. Callers must pass args like:
//     []string{"new-window", "-t", "mysession", "-n", "editor", "-c", "/path"}
//   - Stdout/stderr are captured and included in errors to aid debugging.
//   - ExtraEnv can be used to control which tmux server/socket is used.
//   - When running inside a tmux client, we must preserve the client server context.
//     In particular, bootstrapped runs should not "lose" their server and then see:
//     "no server running on /private/tmp/tmux-*/default"
type TmuxExecRunner struct {
	// Bin is the tmux executable path/name. If empty, defaults to "tmux".
	Bin string

	// ExtraEnv is appended to the process environment (KEY=VALUE strings).
	ExtraEnv []string

	// Timeout, if > 0, applies a per-command timeout.
	Timeout time.Duration

	// Debug prints executed commands and outputs to stderr when true.
	Debug bool
}

func (r *TmuxExecRunner) Run(args []string) error {
	_, err := r.RunOutput(args)
	return err
}

// RunOutput runs `tmux <args...>` and returns combined stdout/stderr output.
// This is required for safe introspection-style operations (e.g. capture-pane polling)
// without enabling shell passthrough.
func (r *TmuxExecRunner) RunOutput(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("tmux runner: empty args")
	}

	bin := strings.TrimSpace(r.Bin)
	if bin == "" {
		bin = "tmux"
	}

	// Use a context for optional timeout.
	ctx := context.Background()
	var cancel func()
	if r.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}

	// Prefer the current client context if available.
	//
	// Why:
	// tmux subcommands (new-window/split-window/send-keys/etc.) must talk to the same tmux server
	// that the client is connected to. When a process is launched from inside tmux, this context
	// is conveyed through the TMUX environment variable (and optionally TMUX_TMPDIR).
	//
	// If we drop that and simply run `tmux ...`, tmux may look at the default socket and report
	// "no server running ...", even though we are clearly inside a client.
	env := append([]string{}, os.Environ()...)
	env = append(env, r.ExtraEnv...)

	tmuxEnv := strings.TrimSpace(os.Getenv("TMUX"))
	if tmuxEnv != "" && !argsContainSocketOrServerOverride(args) {
		sock := parseTmuxSockPathFromEnv(tmuxEnv)
		if sock != "" {
			// Force tmux to use the active client's socket. This is more reliable than relying
			// on tmux's default socket discovery when multiple sockets exist or when bootstrapping.
			args = append([]string{"-S", sock}, args...)
		}
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if r.Debug {
		fmt.Fprintf(os.Stderr, "tmux-runner: exec: %s %s\n", bin, shellJoin(args))
	}

	err := cmd.Run()

	// Handle context timeout explicitly.
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("tmux runner: timed out after %s: %s %s", r.Timeout, bin, shellJoin(args))
	}

	out := strings.TrimSpace(stdout.String())
	serr := strings.TrimSpace(stderr.String())

	if err != nil {
		switch {
		case out == "" && serr == "":
			return "", fmt.Errorf("tmux runner: %s %s: %w", bin, shellJoin(args), err)
		case serr == "":
			return out, fmt.Errorf("tmux runner: %s %s: %w (stdout=%q)", bin, shellJoin(args), err, out)
		case out == "":
			return serr, fmt.Errorf("tmux runner: %s %s: %w (stderr=%q)", bin, shellJoin(args), err, serr)
		default:
			return out + "\n" + serr, fmt.Errorf("tmux runner: %s %s: %w (stdout=%q stderr=%q)", bin, shellJoin(args), err, out, serr)
		}
	}

	if r.Debug {
		if stdout.Len() > 0 {
			fmt.Fprintf(os.Stderr, "tmux-runner: stdout:\n%s\n", stdout.String())
		}
		if stderr.Len() > 0 {
			fmt.Fprintf(os.Stderr, "tmux-runner: stderr:\n%s\n", stderr.String())
		}
	}

	if out != "" && serr != "" {
		return out + "\n" + serr, nil
	}
	if out != "" {
		return out, nil
	}
	return serr, nil
}

func argsContainSocketOrServerOverride(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-S", "-L":
			return true
		}
	}
	return false
}

// parseTmuxSockPathFromEnv extracts the socket path from $TMUX.
//
// tmux sets $TMUX like:
//
//	/path/to/socket,PID,IDX
func parseTmuxSockPathFromEnv(tmuxEnv string) string {
	tmuxEnv = strings.TrimSpace(tmuxEnv)
	if tmuxEnv == "" {
		return ""
	}
	// Take everything before the first comma.
	if i := strings.Index(tmuxEnv, ","); i >= 0 {
		return strings.TrimSpace(tmuxEnv[:i])
	}
	return strings.TrimSpace(tmuxEnv)
}

// RenderDryRun formats a compiled plan into a deterministic, human-readable output
// that is suitable for:
// - preview panes in TUIs
// - CLI --dry-run output
// - logs
//
// It prefers explanations when provided, and flags unsafe commands.
func RenderDryRun(compiled Compiled) string {
	lines := DryRunLines(compiled)
	return strings.Join(lines, "\n")
}

// RenderDryRunWithHeader is like RenderDryRun, but includes a short header.
// Useful when you want the preview to clearly indicate policy and safety state.
func RenderDryRunWithHeader(title string, compiled Compiled) string {
	var b strings.Builder
	title = strings.TrimSpace(title)
	if title != "" {
		b.WriteString(title)
		b.WriteString("\n")
	}

	for _, ln := range DryRunLines(compiled) {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
