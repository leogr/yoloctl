package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
)

// validateAgentboxName validates box admission; validateSpaceName accepts
// existing space references, including the historical 63-character names.
// New-space admission separately calls layout.ValidateName("space", name).
//
// Every CLI entry that takes a name and either (a) interpolates it into
// a shell command, (b) joins it into a path on the env VM, or (c) uses
// it as a container label must validate first — otherwise an
// unvalidated name becomes either a path-traversal or a shell-injection
// vector via the ssh ProxyCommand / nerdctl exec / `sudo rm -rf` paths.

func validateAgentboxName(name string) error {
	return asUserError(layout.ValidateName("agentbox", name))
}
func validateSpaceName(name string) error { return asUserError(layout.ValidateExistingSpaceName(name)) }

// expandUserPath resolves p to an absolute path, expanding a leading `~`
// (or `~/`) to the user's home dir. Does not require the path to exist —
// the workdir may be created on demand. Used by host-path flag resolution.
func expandUserPath(p string) (string, error) {
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			return home, nil
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~/"))
	}
	return filepath.Abs(p)
}

// shellQuote wraps s in POSIX single quotes so it survives /bin/sh -c
// as a single argv element. Embedded single quotes are escaped with the
// standard close-escape-reopen trick: the single quote becomes the four
// characters quote-backslash-quote-quote.
//
// OpenSSH runs the ProxyCommand string through /bin/sh, so every
// argument we splice into a ProxyCommand template — including the
// path returned by os.Executable() — has to be shell-safe. Validated
// names (alnum + ._-) don't need quoting in practice but get it anyway
// as defense in depth.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// remoteCommandString turns argv into the single command string OpenSSH
// sends in an exec request. Passing argv elements to ssh separately is
// not enough: OpenSSH joins them with spaces, so `sh -lc "echo ok"`
// becomes `sh -lc echo ok` on the remote side. Pre-quoting each argv
// element preserves argument boundaries through the remote shell.
func remoteCommandString(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}
