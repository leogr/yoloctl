package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/sshtransport"
	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// lookupAgentProfile resolves an agent name through the internal/agentprofile
// catalog: embedded read-only built-ins plus custom definitions under
// ~/.config/yoloctl/agents/.
//
// fs.ErrNotExist from the loader is translated to a user-error
// listing the available profiles, so a typo gets a helpful message
// rather than a raw filesystem error.
func lookupAgentProfile(name string) (*agentprofile.Profile, error) {
	p, err := agentprofile.Lookup(name)
	if err == nil {
		return p, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		names, listErr := agentprofile.Names()
		if listErr != nil {
			return nil, err
		}
		return nil, asUserError(fmt.Errorf("unknown agent %q; available: %v", name, names))
	}
	return nil, err
}

// applyAgentProfileDefaults prefills the create image and required features
// from the agent catalog. Explicit image/feature flags take precedence.
func applyAgentProfileDefaults(cmd *cobra.Command, opts *agentboxCreateOptions, profile *agentprofile.Profile) {
	if !flagChanged(cmd, "image") {
		opts.Image = profile.Image
	}
	if !flagChanged(cmd, "features") && !flagChanged(cmd, "feature") {
		opts.Features = profileFeatures(profile)
	}
}

// profileFeatures is the agent profile's feature composition: its
// RequiresFeatures, de-duplicated and order-stable. That list includes the
// feature installing the agent's own CLI (e.g. claude → [claude, github]) — the
// agent has no separate agent_feature; capabilities reach the policy only via
// these features. Empty when the profile requires none.
func profileFeatures(profile *agentprofile.Profile) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range profile.RequiresFeatures {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// agentProfileNames returns the registered agent names in a stable
// order for error messages and --help text. Wraps
// agentprofile.Names with a panic-on-error since failure here means
// the config dir is unreachable, which yoloctl can't proceed past
// anyway.
func agentProfileNames() []string {
	names, err := agentprofile.Names()
	if err != nil {
		// Logged via stderr at the call site if needed; return
		// empty so help text degrades gracefully instead of
		// crashing on `--help`.
		return nil
	}
	return names
}

// genericShellArgv is the fallback for an agentless box without session_command:
// an interactive login shell, kept alive by the holder and joined by box attach.
func genericShellArgv() []string {
	return []string{"bash", "-l"}
}

// execInAgentboxInteractive runs cmdArgs inside the agentbox via the
// sandbox SSH socket, forwarding stdio, with a PTY (-t) when execWantsPTY
// says a person is on both ends.
func execInAgentboxInteractive(ctx context.Context, env, agentbox string, cmdArgs []string) error {
	if err := guardOperatorChatExecution(ctx, env, agentbox); err != nil {
		return err
	}
	if err := preflightBoxExec(ctx, env, agentbox); err != nil {
		return err
	}
	var conn net.Conn
	var err error
	if spaceenv.Native() || !currentControllerPrincipal(ctx).Admin() {
		// Native host and operator workers select the same confined endpoint
		// directly, without a second VM/stdio proxy and its raw helper errors.
		conn, err = dialOperatorSSH(ctx, agentbox)
	} else {
		yoloctlPath, pathErr := os.Executable()
		if pathErr != nil {
			return fmt.Errorf("locate yoloctl binary: %w", pathErr)
		}
		proxy := exec.CommandContext(ctx, yoloctlPath, "ssh-proxy", "--space", env, agentbox)
		proxy.Stderr = os.Stderr
		conn, err = sshtransport.ProcessConn(ctx, proxy)
	}
	if err != nil {
		return fmt.Errorf("cannot connect to box %q in space %q; command was not started: %w", agentbox, env, err)
	}
	client, err := sshtransport.Connect(ctx, conn, &ssh.ClientConfig{User: "sandbox", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		return fmt.Errorf("cannot establish an SSH connection to box %q in space %q; command was not started: %w", agentbox, env, err)
	}
	defer func() { _ = client.Close() }()
	// Keep channel/PTY preparation bounded while setup is still held. The
	// deadline is removed before command permission, never applied to the run.
	deadline := time.Now().Add(operatorTargetSetupTimeout)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("cannot prepare SSH connection to box %q in space %q; command was not started: %w", agentbox, env, err)
	}
	commandAttempted := false
	err = sshtransport.RunClientWithStartGate(ctx, client, remoteCommandString(sessionTerminalIdentity(ctx).Command(cmdArgs)), execWantsPTY(os.Stdin, os.Stdout), os.Stdin, os.Stdout, os.Stderr, func() error {
		// Handshake/channel/PTY setup cannot run the requested command. Keep
		// their failures before permission, and retain setup until selection.
		if err := conn.SetDeadline(time.Time{}); err != nil {
			return err
		}
		if err := controller.BeginScopedEffects(ctx, execIntent(agentbox)); err != nil {
			return err
		}
		if err := releaseSpaceOperation(ctx); err != nil {
			return err
		}
		commandAttempted = true
		return nil
	})
	if code, ok := sshtransport.ExitStatus(err); ok {
		return passthroughExitError{err: err, code: code}
	}
	if err != nil {
		if !commandAttempted {
			return fmt.Errorf("cannot prepare command in box %q in space %q; command was not started: %w", agentbox, env, err)
		}
		// A request can run even if its reply is lost. Keep true result loss
		// uncertain, never infer a safe retry from the transport error alone.
		return &controller.EffectsUncertain{Cause: fmt.Errorf("connection to box %q in space %q ended without a command exit status: %w", agentbox, env, err)}
	}
	return nil
}

// execWantsPTY reports whether `box exec` asks ssh for a remote PTY: only when
// stdin and stdout are both real terminals, so interactive programs (bash,
// claude) get one. A PTY turns "\n" into "\r\n" and folds the remote stderr
// into stdout, so output captured or piped from an interactive shell
// (`out=$(yoloctl box exec BOX -- CMD)`) must stay without. isInteractive is
// the wrong probe here: /dev/null is a character device, not a terminal.
func execWantsPTY(stdin, stdout *os.File) bool {
	return term.IsTerminal(int(stdin.Fd())) && term.IsTerminal(int(stdout.Fd()))
}
