package main

import (
	"errors"
	"fmt"

	"time"

	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/pkg/prettylog"
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
		startMain       bool
		markMain        bool
		mainRecord      string
		mainRecordSHA   string
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

			// The journal or guest unit relay owns stderr; file records have
			// timestamps/levels and never use a terminal palette or reader.
			fileLog := logging.InitFile(cmd.ErrOrStderr())
			defer func() { _ = fileLog.Close() }()
			holderLog := fileLog.Slog().With(prettylog.Actor(logging.Session), logging.KeyBox, agentbox)
			logf := func(format string, a ...any) { holderLog.Info(fmt.Sprintf(format, a...)) }

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
				StartMain:         startMain,
				RefusalPath:       session.RefusalPath(agentbox),
				MarkMain:          markMain,
				MainRecord:        mainRecord,
				MainRecordSHA256:  mainRecordSHA,
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
	cmd.Flags().BoolVar(&startMain, "start-main", false, "Start the session as the box's main process: attach to it and hand it the launch (managed launches into a box created with the launcher)")
	cmd.Flags().StringVar(&mainRecord, "main-record", "", "A main start's selected generation record (record.json), for reading PID 1's exit code when the connection ends without a status")
	cmd.Flags().StringVar(&mainRecordSHA, "main-record-sha256", "", "SHA-256 of the selected generation record verified by the caller")
	cmd.Flags().BoolVar(&markMain, "mark-main", false, "Mark the session as a privileged box's main session before it starts (its endpoint stops the box after it)")
	cmd.Flags().DurationVar(&keepalive, "keepalive", 0, "SSH keepalive interval (default 4m; must stay < 600s)")
	return cmd
}
