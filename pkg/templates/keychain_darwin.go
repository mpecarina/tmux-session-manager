//go:build darwin
// +build darwin

package templates

// NOTE:
// This file intentionally contains no implementation.
//
// tmux-session-manager no longer performs any macOS Keychain reads/writes from the
// templates layer. SSH credential handling is delegated to tmux-ssh-manager,
// which already owns the Keychain service and its storage conventions.
//
// This file is kept (empty) on darwin to avoid stale imports/build tags in forks
// that may still reference it, and to make the removal explicit.
