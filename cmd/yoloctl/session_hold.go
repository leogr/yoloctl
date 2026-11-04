package main

import (
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/spf13/cobra"
)

// newSessionHoldCmd is the VM-side session holder: a hidden
// command executed by systemd-run inside the space VM, never by users on
// the host. internal/session.StartHolderUnit composes its invocation;
// see internal/session for the design and the spike-validated
// constraints it implements.
func newSessionHoldCmd() *cobra.Command {
	var (
		sshSock         string
		attachSock      string
		marker          string
		tombstone       string
		term            string
		cols            int
		rows            int
		ringBytes       int
		titleTemplate   string
		titleStrip      string
		hostIntegration bool
		hostWorkdir     string
		launchToken     string
		reapDaemons     []string
		keepalive       time.Duration
	)
	cmd := &cobra.Command{
		Use:    "session-hold BOX -- COMMAND [ARGS...]",
		Short:  "Run the VM-side session holder for a box (internal)",
		Hidden: true,
		Args:   cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentbox := args[0]
			argv := args[1:]
			if err := validateAgentboxName(agentbox); err != nil {
				return err
			}
			if launchToken != "" && !session.ValidLaunchToken(launchToken) {
				return fmt.Errorf("invalid --launch-token")
			}
			// Path flags exist for tests; production runs derive
			// everything from the agentbox name.
			if sshSock == "" {
				sshSock = sandbox.SocketHostPath(agentbox)
			}
			if attachSock == "" {
				attachSock = session.SocketPath(agentbox)
			}
			if marker == "" {
				marker = session.MarkerPath(agentbox)
			}
			if tombstone == "" {
				tombstone = session.TombstonePath(agentbox)
			}

			// Logs land in the unit's journal
			// (`journalctl -u yoloctl-session-<box>`).
			log.SetFlags(log.Ltime | log.Lmicroseconds)
			logf := func(format string, a ...any) { log.Printf("holder["+agentbox+"]: "+format, a...) }

			status, err := session.RunHolder(cmd.Context(), session.HolderOptions{
				Agentbox:          agentbox,
				SSHSocket:         sshSock,
				AttachSocket:      attachSock,
				MarkerPath:        marker,
				TombstonePath:     tombstone,
				LaunchToken:       launchToken,
				Argv:              argv,
				Term:              term,
				Cols:              cols,
				Rows:              rows,
				RingBytes:         ringBytes,
				TitleTemplate:     titleTemplate,
				TitleStrip:        titleStrip,
				HostIntegration:   hostIntegration,
				HostWorkdir:       hostWorkdir,
				Daemons:           reapDaemons,
				KeepaliveInterval: keepalive,
				Logf:              logf,
			})
			// A canceled context is the unit being stopped — clean by
			// design (the agent stays up per spike F1), so don't fail
			// the unit over it.
			if errors.Is(err, cmd.Context().Err()) && cmd.Context().Err() != nil {
				return nil
			}
			if err != nil {
				return err
			}
			// The agent's own exit status lives in the tombstone and in
			// the EXIT frame attached clients received; the unit itself
			// succeeded at its job (hold the session until agent exit),
			// so exit 0 regardless of status.
			_ = status
			return nil
		},
	}
	cmd.Flags().StringVar(&sshSock, "ssh-sock", "", "Sandbox SSH socket path (default: derived from BOX)")
	cmd.Flags().StringVar(&attachSock, "attach-sock", "", "Attach socket path to serve (default: derived from BOX)")
	cmd.Flags().StringVar(&marker, "marker", "", "Session marker file path (default: derived from BOX)")
	cmd.Flags().StringVar(&tombstone, "tombstone", "", "Tombstone file path (default: derived from BOX)")
	cmd.Flags().StringVar(&term, "term", "", "TERM for the agent PTY (default xterm-256color)")
	cmd.Flags().IntVar(&cols, "cols", 0, "Initial PTY columns (first attach resize corrects)")
	cmd.Flags().IntVar(&rows, "rows", 0, "Initial PTY rows (first attach resize corrects)")
	cmd.Flags().IntVar(&ringBytes, "ring-bytes", 0, "Output ring-buffer size in bytes (default 1 MiB)")
	cmd.Flags().StringVar(&titleTemplate, "title-template", "", "Expanded box title template for OSC 0/1/2 rewriting (default \"<box>: <title>\")")
	cmd.Flags().StringVar(&titleStrip, "title-strip", "", "Agent icon stripped from the front of agent-set titles")
	cmd.Flags().StringVar(&launchToken, "launch-token", "", "Launch correlation token written into the marker and tombstone (32 lowercase hex characters)")
	cmd.Flags().BoolVar(&hostIntegration, "host-integration", false, "Enable session-scoped browser requests and sandbox image transfer")
	cmd.Flags().StringVar(&hostWorkdir, "host-workdir", "", "Canonical host workdir for verified image reuse (empty for ephemeral boxes)")
	cmd.Flags().StringArrayVar(&reapDaemons, "reap-daemon", nil, "Agent daemon argv prefix stopped before the agent starts and after it exits (repeatable)")
	cmd.Flags().DurationVar(&keepalive, "keepalive", 0, "SSH keepalive interval (default 4m; must stay < 600s)")
	return cmd
}
