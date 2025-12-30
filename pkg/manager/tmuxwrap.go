package manager

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Tmux provides a small, consistent wrapper around invoking `tmux`.
// It centralizes:
// - env inheritance/overrides
// - error formatting with stdout/stderr
// - optional debug logging
//
// This is intentionally lightweight; it should be safe to use from TUIs and scripts.
type Tmux struct {
	// Path to tmux binary. If empty, defaults to "tmux" (resolved via PATH).
	Bin string

	// ExtraEnv are environment variables appended to the process environment.
	// Values should be in KEY=VALUE form.
	ExtraEnv []string

	// Timeout applies to each tmux invocation. Zero means no timeout.
	Timeout time.Duration

	// Debug, when true, prints executed tmux commands and outputs to stderr.
	Debug bool
}

// NewTmux returns a Tmux wrapper with sensible defaults.
func NewTmux() *Tmux {
	return &Tmux{
		Bin:     "tmux",
		Timeout: 0,
		Debug:   false,
	}
}

// IsAvailable returns true if the tmux binary can be found/executed.
func (t *Tmux) IsAvailable() bool {
	_, err := exec.LookPath(t.bin())
	return err == nil
}

// InTmux returns true if we appear to be running inside a tmux client.
func (t *Tmux) InTmux() bool {
	// NOTE: This is best-effort; some contexts may talk to a tmux server without TMUX set.
	return os.Getenv("TMUX") != ""
}

// ServerReachable checks if a tmux server is reachable from this process context.
func (t *Tmux) ServerReachable() bool {
	_, err := t.Output("display-message", "-d", "1", "tmux-session-manager: ping")
	return err == nil
}

// Run executes `tmux <args...>` inheriting stdin/stdout/stderr.
// Use this for interactive commands that must attach to the current TTY.
func (t *Tmux) Run(args ...string) error {
	_, _, err := t.run(false /*capture*/, args...)
	return err
}

// Output executes `tmux <args...>` and returns stdout (trimmed of trailing newlines).
// Stderr is included in returned error.
func (t *Tmux) Output(args ...string) (string, error) {
	stdout, _, err := t.run(true /*capture*/, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(stdout, "\r\n"), nil
}

// OutputBytes executes `tmux <args...>` and returns raw stdout bytes.
// Stderr is included in returned error.
func (t *Tmux) OutputBytes(args ...string) ([]byte, error) {
	stdout, _, err := t.runBytes(true /*capture*/, args...)
	if err != nil {
		return nil, err
	}
	return stdout, nil
}

// DisplayMessage shows a brief tmux message if the server is reachable.
// It is safe to call even when not in tmux (no-op on failure).
func (t *Tmux) DisplayMessage(msg string, dur time.Duration) {
	d := durMillisOrDefault(dur, 1500)
	_ = t.Run("display-message", "-d", fmt.Sprintf("%d", d), msg)
}

// CurrentPanePath returns "#{pane_current_path}" expanded by tmux.
// If the server isn't reachable, returns empty string.
func (t *Tmux) CurrentPanePath() (string, error) {
	// This mirrors the common pattern: `tmux display-message -p -F ...`
	out, err := t.Output("display-message", "-p", "-F", "#{pane_current_path}")
	if err != nil {
		return "", err
	}
	return out, nil
}

// NewWindow runs a command in a new tmux window.
// If startDir is empty, it uses "#{pane_current_path}".
func (t *Tmux) NewWindow(name string, startDir string, command string) error {
	if startDir == "" {
		startDir = "#{pane_current_path}"
	}
	args := []string{"new-window", "-c", startDir}
	if strings.TrimSpace(name) != "" {
		args = append(args, "-n", name)
	}
	// Use bash -lc to match existing plugin patterns (shell expansions, env, etc).
	args = append(args, "--", "bash", "-lc", command)
	return t.Run(args...)
}

// SplitWindow splits the current pane and runs a command.
// - horizontal=true => `split-window -h` (side-by-side)
// - horizontal=false => `split-window -v` (stacked)
// If startDir is empty, it uses "#{pane_current_path}".
func (t *Tmux) SplitWindow(horizontal bool, startDir string, command string) error {
	if startDir == "" {
		startDir = "#{pane_current_path}"
	}
	flag := "-v"
	if horizontal {
		flag = "-h"
	}
	args := []string{"split-window", flag, "-c", startDir, "--", "bash", "-lc", command}
	return t.Run(args...)
}

// SelectWindow selects a window by target (e.g. "my:1").
func (t *Tmux) SelectWindow(target string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("tmux: empty window target")
	}
	return t.Run("select-window", "-t", target)
}

// SelectPane selects a pane by target (e.g. "%3" or "my:1.0").
func (t *Tmux) SelectPane(target string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("tmux: empty pane target")
	}
	return t.Run("select-pane", "-t", target)
}

// SetBuffer sets the tmux paste buffer to the provided text.
func (t *Tmux) SetBuffer(text string) error {
	// Use `--` to ensure leading '-' isn't treated as flag.
	return t.Run("set-buffer", "--", text)
}

// ShowOption reads a global tmux option value (like `tmux show -gqv @foo`).
// Returns empty string if not set or on failure.
func (t *Tmux) ShowOptionGlobal(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	out, err := t.Output("show", "-gqv", name)
	if err != nil {
		return ""
	}
	return out
}

// SetOptionGlobal sets a global tmux option.
func (t *Tmux) SetOptionGlobal(name string, value string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("tmux: empty option name")
	}
	return t.Run("set-option", "-g", name, value)
}

// HasCommand checks if `tmux <cmd>` is a valid tmux command by running `tmux -q`.
func (t *Tmux) HasCommand(cmd string) bool {
	if strings.TrimSpace(cmd) == "" {
		return false
	}
	// There is no perfect "has command" API; best-effort:
	// `tmux list-commands` exists; parse is costly. Instead attempt a harmless run.
	// If cmd is invalid, tmux returns non-zero.
	_, err := t.Output(cmd, "-h")
	return err == nil
}

func (t *Tmux) bin() string {
	if strings.TrimSpace(t.Bin) == "" {
		return "tmux"
	}
	return t.Bin
}

func (t *Tmux) run(capture bool, args ...string) (string, string, error) {
	stdout, stderr, err := t.runBytes(capture, args...)
	return string(stdout), string(stderr), err
}

func (t *Tmux) runBytes(capture bool, args ...string) ([]byte, []byte, error) {
	if len(args) == 0 {
		return nil, nil, errors.New("tmux: missing args")
	}

	cmd := exec.Command(t.bin(), args...)
	cmd.Env = append(os.Environ(), t.ExtraEnv...)

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer

	if capture {
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
	} else {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if t.Debug {
		fmt.Fprintf(os.Stderr, "tmuxwrap: exec: %s %s\n", t.bin(), shellJoin(args))
	}

	if t.Timeout > 0 {
		// Minimal timeout support without context package plumbing through all callsites.
		// If timeout elapses, kill the process and return an error.
		err := runWithTimeout(cmd, t.Timeout)
		if err != nil {
			if capture {
				return stdoutBuf.Bytes(), stderrBuf.Bytes(), t.wrapErr(args, stdoutBuf.Bytes(), stderrBuf.Bytes(), err)
			}
			return nil, nil, err
		}
	} else {
		err := cmd.Run()
		if err != nil {
			if capture {
				return stdoutBuf.Bytes(), stderrBuf.Bytes(), t.wrapErr(args, stdoutBuf.Bytes(), stderrBuf.Bytes(), err)
			}
			return nil, nil, err
		}
	}

	if t.Debug && capture {
		if stdoutBuf.Len() > 0 {
			fmt.Fprintf(os.Stderr, "tmuxwrap: stdout:\n%s\n", string(stdoutBuf.Bytes()))
		}
		if stderrBuf.Len() > 0 {
			fmt.Fprintf(os.Stderr, "tmuxwrap: stderr:\n%s\n", string(stderrBuf.Bytes()))
		}
	}

	return stdoutBuf.Bytes(), stderrBuf.Bytes(), nil
}

func (t *Tmux) wrapErr(args []string, stdout, stderr []byte, err error) error {
	// Include both stdout/stderr since tmux frequently prints useful hints on stderr.
	sout := strings.TrimSpace(string(stdout))
	serr := strings.TrimSpace(string(stderr))

	switch {
	case sout == "" && serr == "":
		return fmt.Errorf("tmux: %s: %w", shellJoin(args), err)
	case serr == "":
		return fmt.Errorf("tmux: %s: %w (stdout=%q)", shellJoin(args), err, sout)
	case sout == "":
		return fmt.Errorf("tmux: %s: %w (stderr=%q)", shellJoin(args), err, serr)
	default:
		return fmt.Errorf("tmux: %s: %w (stdout=%q stderr=%q)", shellJoin(args), err, sout, serr)
	}
}

func durMillisOrDefault(d time.Duration, def int) int {
	if d <= 0 {
		return def
	}
	ms := int(d / time.Millisecond)
	if ms <= 0 {
		return def
	}
	return ms
}

func shellJoin(args []string) string {
	// Best-effort for log/error messages; not intended for re-exec as-is.
	out := make([]string, 0, len(args))
	for _, a := range args {
		out = append(out, shellQuoteSimple(a))
	}
	return strings.Join(out, " ")
}

func shellQuoteSimple(s string) string {
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
	// Single-quote with safe embedded quote sequence.
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func runWithTimeout(cmd *exec.Cmd, timeout time.Duration) error {
	if timeout <= 0 {
		return cmd.Run()
	}

	type result struct {
		err error
	}
	ch := make(chan result, 1)

	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		ch <- result{err: cmd.Wait()}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case r := <-ch:
		return r.err
	case <-timer.C:
		_ = cmd.Process.Kill()
		// Wait for goroutine to finish to avoid zombies.
		<-ch
		return fmt.Errorf("tmux command timed out after %s", timeout)
	}
}
