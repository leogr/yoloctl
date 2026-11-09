package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/leogr/yoloctl/internal/account"
	"github.com/leogr/yoloctl/internal/accountstate"
	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/devcontainer"
	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/manifestio"
	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/leogr/yoloctl/internal/port"
	"github.com/leogr/yoloctl/internal/sandbox"
	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	vmhost "github.com/leogr/yoloctl/internal/vm"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const (
	// defaultAgentboxImage is the base image used when the user does not set
	// --image. It comes from the agentboximage catalog so the flag default
	// and build-on-demand path agree on a single string.
	defaultAgentboxImage = agentboximage.RefAgentbox
)

func newAgentboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "box",
		Short: "Manage boxes (sandboxed containers, 1:1 with a coding agent)",
	}
	cmd.AddCommand(
		newAgentboxCreateCmd(),
		newAgentboxCopyCmd(),
		newAgentboxStartCmd(),
		newAgentboxStopCmd(),
		newAgentboxRestartCmd(),
		newAgentboxDestroyCmd(),
		newAgentboxRecreateCmd(),
		newAgentboxListCmd(),
		newAgentboxShowCmd(),
		newAgentboxSessionCmd(),
		newAgentboxInspectCmd(),
		newAgentboxExecCmd(),
		newAgentboxAttachCmd(),
		newAgentboxUseCmd(),
		newAgentboxLogsCmd(),
		newAgentboxReconcileCmd(),
		newAgentboxDiffCmd(),
		newAgentboxEditCmd(),
		newAgentboxSetCmd(),
	)
	return cmd
}

func newAgentboxLogsCmd() *cobra.Command {
	var (
		spaceName string
		follow    bool
	)
	cmd := &cobra.Command{
		Use:   "logs BOX",
		Short: "Stream a box's stdout/stderr",
		Long: `Wraps ` + "`sudo nerdctl logs`" + ` on the box container inside its
space. The first thing that lands in the log is openshell-sandbox's
init / netns / Landlock setup, which is the right place to look when
a box refuses to start.

  yoloctl box logs coder
  yoloctl box logs coder --follow
  yoloctl box logs coder --space prod-untrusted`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := validateAgentboxName(args[0]); err != nil {
				return err
			}
			return container.New(spaceName).Logs(cmd.Context(), args[0], follow)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow new output (nerdctl logs --follow)")
	return cmd
}

// newAgentboxAttachCmd builds the agent-session attach command.
// It is a factory called twice: once under `box` as
// `box attach`, once at the root as the `attach` alias. Both share this RunE +
// flag set verbatim; cobra forbids parenting one command twice, so each call
// returns an independent command closing over the same builder.
func newAgentboxAttachCmd() *cobra.Command {
	var (
		spaceName    string
		noAutoupdate bool
		newSession   bool
	)
	cmd := &cobra.Command{
		Use:   "attach BOX",
		Short: "Attach to the box's persistent terminal session",
		Long: `Attaches your terminal to the box's persistent session
with the shared terminal renderer (recommended): restore the screen,
history and input modes, then follow live output. Outside the TUI this uses
one full-screen view without tabs or a palette. The configured palette key
(default Ctrl+Y), then Ctrl+W, Ctrl+D or d (as in tmux) detaches; press the
palette key twice to send it to the application. All attachments remain
interactive; the latest resize sets the shared session geometry.

Managed views support explicit host PNG paste with Ctrl+V: reuse a copied PNG
already readable with matching contents in the box's shared workdir, or transfer
it to a temporary guest file. Insert its path without submitting. New sessions
also provide a BROWSER helper for HTTP(S) requests, opening automatically on the
host by default. With seams.browser: ask, a prompt appears immediately: Ctrl+O
opens the URL, Esc cancels. Configure seams.clipboard and seams.browser in
tui.yaml. Ordinary exec and --passthrough do not provide
these integrations; browser login callback ports are not forwarded automatically.

--passthrough uses the host terminal's rendering and selection instead.
This compatibility mode has only bounded transcript replay and forwards
recognized UTF-8 display/input controls. The holder answers queries once;
queries and opaque extensions (including graphics/DCS/APC) are not forwarded.
It cannot promise complete screen restoration or every host terminal feature.
Inside a TUI tab it still uses that tab's renderer. A passthrough-only session
cannot supply measured host colors for otherwise unknown color queries.
Its resizes report unknown pixel dimensions; pixel mouse input is not rescaled
between viewers with different fonts. Use managed mode for measured geometry.
Nonterminal attachments use this compatibility stream too; use box exec for
scripted command output. Closing either view leaves the session running.

Ensure-semantics (deliberate divergence from strict kubectl): when the
box has no live session — it exited, the VM rebooted, the box stopped —
attach starts one, restarting the box first if needed. If a stopped box
needs recreation, attach says so before any image build; at a terminal it
offers to recreate it (box recreate --force --no-attach) and then attaches.
A detached
session exit is reported once (exit status + time), then a fresh session
starts. The revived session runs the box's agent profile command with
its default args and agent_extra_args — or session_command for an agentless
box, falling back to yoloctl's login shell when unset. A custom positional
COMMAND from the original run is not persisted. If the previous session's holder died, the orphaned
session cannot be re-attached (no reattach API upstream) — attach
refuses with a recreate hint rather than start a second one. A fresh session
also refuses a verified stale recipe image in an existing container and prints
space-qualified inspect/recreate commands. Existing live sessions remain
attachable; attach never replaces their container automatically.

When the box's agent declares conversation identity (Claude), a new session
resumes the box's own conversation by ID instead of whatever the shared account
saw last, unless agent_session.resume is never or manual. --new-session starts
a new conversation instead and keeps the old ID in the box's history; it is
refused while a session runs (use box restart --new-session to replace it).

For an interactive bash shell, use ` + "`yoloctl box exec BOX -- bash`" + `.

--box-use is a separate collaborative frontend for agents and scripts. Keep
stdin/stdout pipes open: stdout starts with a versioned JSON ready record;
stdin accepts one JSON object per line with an id and op. Operations are screen,
wait, text, key, history and detach. Screen/wait return bounded plain text and
advisory peer input activity. Other attachments remain interactive. This view
follows shared geometry without resizing it or providing desktop integrations.
It uses managed snapshots, not the passthrough transcript.

Examples of JSON requests (coordinates and history offsets are zero-based):
  {"id":"1","op":"screen"}
  {"id":"2","op":"text","text":"Reply with OK"}
  {"id":"3","op":"key","key":"enter"}
  {"id":"4","op":"wait","since":1,"timeout_ms":30000,"settle_ms":100}
  {"id":"5","op":"history","offset":0,"limit":50}
  {"id":"6","op":"detach"}

Use screen/wait since with the last observation revision for changed rows;
otherwise a full bounded screen is returned. Text does not append Enter.
Key names include enter, escape, arrow_up and key_c, with optional modifiers
["ctrl"], ["alt"], ["shift"] or ["super"]. Input acknowledgements mean sent,
not executed. Inspect the session after uncertain delivery; never blindly retry.
EOF or detach leaves the agent running. --box-use and --passthrough are exclusive.

Closing an attachment cannot cancel a session start already in progress: the
controller finishes it, so the session may start after the client left. A
signal (Ctrl-C, kill, timeout, a closing terminal) before the attachment
starts ends the client, never the setup: it exits with status 75 and prints
the request ID. After the attachment starts, one signal is forwarded to it and
ends it (the ordinary detach, status 0); a second ends the client
unconditionally. A client timeout is not proof of a failed launch; inspect box
session or space operation by request ID and never replay input.

  yoloctl box attach claude
  yoloctl box attach coder --space prod-untrusted`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := validateAgentboxName(name); err != nil {
				return err
			}
			ctx := cmd.Context()
			if err := ensureSpaceRunning(ctx, spaceName); err != nil {
				return fmt.Errorf("ensure space %s: %w", spaceName, err)
			}
			return ensureAndAttachSession(ctx, container.New(spaceName), spaceName, name, cmd.ErrOrStderr(), noAutoupdate, false, sessionStartOptions{NewSession: newSession})
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().BoolVar(&noAutoupdate, "no-autoupdate", false, "Skip the npm install -g <pkg>@latest refresh when attach has to restart a stopped box")
	cmd.Flags().BoolVar(&newSession, newSessionFlag, false, "Start a new agent conversation instead of resuming the box's own (refused while a session runs)")
	addPassthroughFlag(cmd)
	cmd.Flags().Bool("box-use", false, "Use the structured collaborative terminal interface (JSON Lines over stdin/stdout)")
	cmd.MarkFlagsMutuallyExclusive("box-use", "passthrough")
	addChatAttachFlags(cmd)
	return cmd
}

func addPassthroughFlag(cmd *cobra.Command) {
	cmd.Flags().Bool("passthrough", false, "Use compatibility terminal output instead of snapshot rendering: bounded replay, filtered queries and no opaque terminal extensions")
}

// stdinIsTerminal reports whether the command's stdin is a real terminal —
// the auto-attach gate. A character device is not enough: /dev/null and
// /dev/zero are not terminals, and `yoloctl box create ... </dev/null` must
// not attach.
func stdinIsTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// Injectable seams so the auto-attach gate and tail are testable without a
// PTY or a VM (same pattern as newBoxStateResolver). Tests that swap them
// must not run in parallel and restore via t.Cleanup.
var (
	stdinIsTerminalFn        = stdinIsTerminal
	ensureAndAttachSessionFn = ensureAndAttachSession
)

// maybeAutoAttach is the lifecycle commands' auto-attach tail
// (create/copy/start/restart/recreate): attach to the box's session unless
// the user opted out (--no-attach) or stdin is not a terminal (scripts, CI,
// pipes — attaching would stream the session and block until it exits).
// Runs AFTER the command's own output; the non-terminal hint is
// space-qualified so it works verbatim under any pinned/default space. When
// it does attach, the session's exit status becomes the command's exit code
// and a detach exits 0 (finishAttach), exactly like `box attach`.
func maybeAutoAttach(cmd *cobra.Command, space, name string, noAttach, noAutoupdate, alreadyUpdated bool) error {
	if noAttach {
		return nil
	}
	protected, err := operatorManagedServerTargetFn(cmd.Context(), space, name)
	if err != nil {
		return err
	}
	if protected {
		_, err := fmt.Fprintln(cmd.ErrOrStderr(), "Managed chat server lifecycle completed; interactive server attachment is unavailable to operators.")
		return err
	}
	if !stdinIsTerminalFn(cmd.InOrStdin()) {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "attach with: yoloctl box attach %s --space %s\n", name, space)
		return nil
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "notice: attaching to box %s (skip with --no-attach)\n", name)
	return ensureAndAttachSessionFn(cmd.Context(), container.New(space), space, name, cmd.ErrOrStderr(), noAutoupdate, alreadyUpdated, sessionStartOptions{})
}

func newAgentboxShowCmd() *cobra.Command {
	var (
		spaceName string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "show BOX",
		Short: "Print a box's declaration, runtime, session, and drift state",
		Long: `Shows the per-box state that ` + "`box list`" + ` summarizes:
declaration, runtime, session, and reconcile drift. If the container exists,
the rich view also includes identity (name / image / labels), agent +
installed CLI version, policy, bind mounts (with read-only flag), Landlock
state, and account-state bind mount. If the runtime is missing or unavailable,
show prints a compact state summary and next action hints instead of hiding the
declared box.

  yoloctl box show coder
  yoloctl box show coder --space prod-untrusted
  yoloctl box show coder --json   # machine-readable output`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := validateAgentboxName(args[0]); err != nil {
				return err
			}
			resolved, err := newBoxStateResolver().ResolveOne(cmd.Context(), spaceName, args[0], boxstate.FullDiff)
			if err != nil {
				return err
			}
			if resolved.Declaration == boxstate.DeclarationMissing && resolved.Runtime == boxstate.RuntimeMissing {
				return asUserError(fmt.Errorf("box %q not found in space %q", args[0], spaceName))
			}
			if resolved.Container == nil {
				if asJSON {
					return writeBoxStateJSON(cmd.OutOrStdout(), resolved)
				}
				return writeBoxStateSummary(cmd.OutOrStdout(), resolved)
			}

			client := container.New(spaceName)
			d, err := client.Inspect(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			// Best-effort Landlock probe: failures are surfaced as
			// "unknown" rather than blocking show. The probe asks the
			// VM kernel for its ABI and scans the retained box log.
			ll, llErr := inspectLandlock(cmd.Context(), spaceName, args[0])
			if llErr != nil {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"warning: landlock probe failed: %v\n", llErr)
			}
			// Best-effort agent-state resolution. Surfaces the account-owned
			// bind-mount source so users can see
			// "where is my Claude state going?" at a glance. Skip silently when
			// the manifest or agent definition cannot be loaded — the box show
			// output stays useful.
			state := resolveAgentStateForShow(spaceName, args[0])
			meta := loadAgentboxShowMetadata(cmd.Context(), client, d, spaceName, args[0], cmd.ErrOrStderr())
			sess := sessionStatusFromBoxState(resolved.Session)
			if asJSON {
				return writeAgentboxShowJSON(cmd.OutOrStdout(), d, spaceName, ll, state, meta, sess, &resolved)
			}
			return writeAgentboxShow(cmd.OutOrStdout(), d, spaceName, ll, state, meta, sess, &resolved)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the human-readable view")
	return cmd
}

// writeAgentboxShow renders the human-readable view: a few identity
// lines followed by mount and label tables. The landlock arg comes from
// inspectLandlock and adds a "Landlock:" status line; the state arg
// drives the dedicated "Agent state:" block.
func writeAgentboxShow(w io.Writer, d *container.Details, querySpace string, ll landlockState, state agentStateMount, meta agentboxShowMetadata, sess sessionStatus, resolved *boxstate.Box) error {
	space := d.Labels[agentbox.LabelSpace]
	if space == "" {
		space = querySpace
	}
	_, _ = fmt.Fprintf(w, "Name:     %s\n", d.Name)
	if resolved != nil {
		_, _ = fmt.Fprintf(w, "State:    %s\n", resolved.State)
	}
	_, _ = fmt.Fprintf(w, "Status:   %s\n", d.Status)
	_, _ = fmt.Fprintf(w, "Image:    %s\n", displayImageRef(d.Image))
	_, _ = fmt.Fprintf(w, "Space:    %s\n", space)
	if meta.Workdir != "" {
		_, _ = fmt.Fprintf(w, "Workdir:  %s (%s)\n", meta.Workdir, meta.WorkdirMode)
		if meta.WorkdirContainer != "" {
			_, _ = fmt.Fprintf(w, "  in box: %s\n", meta.WorkdirContainer)
		}
	}
	_, _ = fmt.Fprintf(w, "Agent:    %s\n", agentDisplayLine(meta))
	// The plain account NAME (from the yoloctl.account label). Account content
	// is never a runtime signal.
	if acct := d.Labels[agentbox.LabelAccount]; acct != "" {
		_, _ = fmt.Fprintf(w, "Account:  %s\n", acct)
	}
	if meta.Policy != "" {
		_, _ = fmt.Fprintf(w, "Policy:   %s\n", meta.Policy)
	}
	if meta.UnconfinedNetwork {
		_, _ = fmt.Fprintf(w, "Network:  ⚠ UNCONFINED — open egress, no allow-list/L7 proxy (other confinement layers intact)\n")
	}
	if meta.privilegedView() {
		_, _ = fmt.Fprintf(w, "Runtime:  %s\n", privilegedNotice(meta.Runtime))
		_, _ = fmt.Fprintln(w, "Landlock: not applied (privileged box; OpenShell policy is recorded but not enforced)")
	} else {
		_, _ = fmt.Fprintf(w, "Landlock: %s\n", landlockSummaryLine(ll))
	}
	_, _ = fmt.Fprintf(w, "Session:  %s\n", sessionSummaryLine(sess, d.Name))
	if resolved != nil && resolved.Conversation != nil {
		_, _ = fmt.Fprintf(w, "Conversation: %s\n", resolved.Conversation.Line())
	}
	writeAgentStateBlock(w, state)
	if d.Created != "" {
		_, _ = fmt.Fprintf(w, "Created:  %s\n", formatCreatedForDisplay(d.Created, time.Now()))
	}
	if len(d.Mounts) == 0 {
		_, _ = fmt.Fprintln(w, "Mounts:   (none)")
	} else {
		_, _ = fmt.Fprintln(w, "Mounts:")
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "  SOURCE\tDESTINATION\tMODE")
		for _, m := range d.Mounts {
			mode := "rw"
			if m.ReadOnly {
				mode = "ro"
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", m.Source, m.Destination, mode)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	// Filter the nerdctl + containerd + OCI image-baked labels — they
	// are implementation noise from the user's perspective; the
	// underlying image tag and status convey the same information.
	// JSON consumers can opt back in by reading the raw labels from
	// `nerdctl inspect` directly.
	userLabels := filterUserLabels(d.Labels)
	if len(userLabels) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w, "Labels:")
	keys := make([]string, 0, len(userLabels))
	for k := range userLabels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "  %s=%s\n", k, userLabels[k])
	}
	return nil
}

func writeBoxStateSummary(w io.Writer, b boxstate.Box) error {
	_, _ = fmt.Fprintf(w, "Name:        %s\n", b.Name)
	_, _ = fmt.Fprintf(w, "State:       %s\n", b.State)
	_, _ = fmt.Fprintf(w, "Space:       %s\n", b.Space)
	_, _ = fmt.Fprintf(w, "Declaration: %s\n", b.Declaration)
	_, _ = fmt.Fprintf(w, "Runtime:     %s\n", b.Runtime)
	_, _ = fmt.Fprintf(w, "Session:     %s\n", b.Session.State)
	if b.Conversation != nil {
		_, _ = fmt.Fprintf(w, "Conversation: %s\n", b.Conversation.Line())
	}
	_, _ = fmt.Fprintf(w, "Drift:       %s\n", b.Drift)
	if b.Config != nil {
		_, _ = fmt.Fprintf(w, "Agent:       %s\n", b.Config.Agent)
		_, _ = fmt.Fprintf(w, "Image:       %s\n", b.Config.Image)
		_, _ = fmt.Fprintf(w, "Workdir:     %s\n", boxStateWorkdirField(b))
	}
	if reason := boxStateReasonField(b); reason != "" {
		_, _ = fmt.Fprintf(w, "Reason:      %s\n", reason)
	}
	if b.State == boxstate.DisplayUnavailable {
		_, _ = fmt.Fprintf(w, "Next:        yoloctl space start %s\n", b.Space)
	}
	if b.State == boxstate.DisplayConfigured {
		_, _ = fmt.Fprintf(w, "Next:        yoloctl box start %s --space %s\n", b.Name, b.Space)
	}
	return nil
}

func sessionStatusFromBoxState(s boxstate.SessionInfo) sessionStatus {
	out := sessionStatus{State: string(s.State), ExitCode: s.ExitCode, ExitedAt: s.ExitedAt}
	if out.State == "" || out.State == string(boxstate.SessionUnknown) {
		out.State = "none"
	}
	return out
}

// isInternalLabel reports whether a container label is internal to
// nerdctl / containerd / the OCI image spec — the kind of label users
// of `box show` don't want to see. yoloctl.* and any other user-
// provided label survives.
func isInternalLabel(key string) bool {
	switch {
	case strings.HasPrefix(key, "nerdctl/"):
		return true
	case strings.HasPrefix(key, "io.containerd."):
		return true
	case strings.HasPrefix(key, "org.opencontainers.image."):
		return true
	}
	return false
}

// writeAgentboxShowJSON emits a single top-level object.
// Env is deliberately omitted from JSON: credential values land here
// and machine consumers shouldn't be tempted to read them out.
func writeAgentboxShowJSON(w io.Writer, d *container.Details, querySpace string, ll landlockState, state agentStateMount, meta agentboxShowMetadata, sess sessionStatus, resolved *boxstate.Box) error {
	type mountJSON struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
		ReadOnly    bool   `json:"read_only"`
	}
	type landlockJSON struct {
		Summary         string `json:"summary"`
		KernelSupported bool   `json:"kernel_supported"`
		ABI             int    `json:"abi,omitempty"`
		ProbeError      string `json:"probe_error,omitempty"`
		LogHint         string `json:"log_hint,omitempty"`
	}
	type agentStateJSON struct {
		Source      string `json:"source,omitempty"`
		Destination string `json:"destination,omitempty"`
		Enabled     bool   `json:"enabled"`
	}
	type agentJSON struct {
		Profile          string `json:"profile"`
		Package          string `json:"package,omitempty"`
		InstalledVersion string `json:"installed_version,omitempty"`
		VersionNote      string `json:"version_note,omitempty"`
	}
	type sessionJSON struct {
		State    string `json:"state"` // running | exited | lost | none
		ExitCode *int   `json:"exit_code,omitempty"`
		ExitedAt string `json:"exited_at,omitempty"`
	}
	type boxDimensionsJSON struct {
		Declaration string `json:"declaration"`
		Runtime     string `json:"runtime"`
		Session     string `json:"session"`
		Drift       string `json:"drift"`
	}
	type reasonJSON struct {
		Code    string `json:"code"`
		Message string `json:"message,omitempty"`
	}
	type workdirJSON struct {
		Mode          string `json:"mode"`
		Workdir       string `json:"workdir,omitempty"`
		ContainerPath string `json:"container_path,omitempty"`
	}
	type showJSON struct {
		Name       string             `json:"name"`
		State      string             `json:"state,omitempty"`
		Status     string             `json:"status"`
		Running    bool               `json:"running"`
		Image      string             `json:"image"`
		Space      string             `json:"space"`
		Dimensions *boxDimensionsJSON `json:"dimensions,omitempty"`
		Reasons    []reasonJSON       `json:"reasons,omitempty"`
		Workdir    *workdirJSON       `json:"workdir,omitempty"`
		Agent      *agentJSON         `json:"agent,omitempty"`
		// Account is the plain account NAME the box selects (the
		// yoloctl.account label). Omitted for a generic shell; never secret
		// values.
		Account string `json:"account,omitempty"`
		Policy  string `json:"policy,omitempty"`
		// UnconfinedNetwork is the declared network confinement posture; true
		// means open egress (no allow-list / L7 proxy — openshell --mode=process).
		UnconfinedNetwork bool `json:"unconfined_network"`
		// Privileged is the declared privileged setting. RuntimeMode is
		// the inspected launch mode: privileged, confined, or empty when the
		// runtime evidence is incomplete or inconsistent.
		Privileged  bool              `json:"privileged"`
		RuntimeMode string            `json:"runtime_mode"`
		Created     string            `json:"created,omitempty"`
		Labels      map[string]string `json:"labels"`
		Mounts      []mountJSON       `json:"mounts"`
		Landlock    landlockJSON      `json:"landlock"`
		// AccountState is the account's per-agent state-leaf bind. The state
		// IS account-owned; TestBoxShowJSONAccountShape pins the key.
		AccountState agentStateJSON `json:"account_state"`
		Session      sessionJSON    `json:"session"`
		// Conversation is the agent's conversation identity; absent for
		// agentless boxes.
		Conversation *agentsession.Status `json:"conversation,omitempty"`
	}
	mounts := make([]mountJSON, 0, len(d.Mounts))
	for _, m := range d.Mounts {
		mounts = append(mounts, mountJSON{
			Source:      m.Source,
			Destination: m.Destination,
			ReadOnly:    m.ReadOnly,
		})
	}
	// JSON labels: filter the same internal noise as the table view so
	// machine consumers get a clean, stable shape. Anything starting
	// with nerdctl/ / io.containerd. / org.opencontainers.image. is
	// dropped. Available raw via `nerdctl inspect` for users who need
	// the full thing.
	labels := filterUserLabels(d.Labels)
	space := d.Labels[agentbox.LabelSpace]
	if space == "" {
		space = querySpace
	}
	out := showJSON{
		Name:    d.Name,
		Status:  d.Status,
		Running: d.Running,
		Image:   d.Image,
		Space:   space,
		Created: d.Created,
		Labels:  labels,
		Mounts:  mounts,
		Landlock: landlockJSON{
			Summary:         ll.Summary,
			KernelSupported: ll.KernelSupported,
			ABI:             ll.ABI,
			ProbeError:      ll.ProbeError,
			LogHint:         ll.LogHint,
		},
		AccountState: agentStateJSON(state),
	}
	if resolved != nil {
		out.State = string(resolved.State)
		out.Dimensions = &boxDimensionsJSON{
			Declaration: string(resolved.Declaration),
			Runtime:     string(resolved.Runtime),
			Session:     string(resolved.Session.State),
			Drift:       string(resolved.Drift),
		}
		for _, r := range resolved.Reasons {
			out.Reasons = append(out.Reasons, reasonJSON{Code: string(r.Code), Message: r.Message})
		}
	}
	if meta.AgentName != "" {
		out.Agent = &agentJSON{
			Profile:          meta.AgentName,
			Package:          meta.AgentPackage,
			InstalledVersion: meta.AgentVersion,
			VersionNote:      meta.AgentVersionNote,
		}
	}
	out.Session = sessionJSON{State: sess.State}
	if resolved != nil {
		out.Conversation = resolved.Conversation
	}
	if sess.State == "exited" {
		code := sess.ExitCode
		out.Session.ExitCode = &code
		out.Session.ExitedAt = sess.ExitedAt.Format(time.RFC3339)
	}
	out.Account = d.Labels[agentbox.LabelAccount]
	out.Policy = meta.Policy
	out.UnconfinedNetwork = meta.UnconfinedNetwork
	out.Privileged = meta.Privileged
	out.RuntimeMode = string(meta.Runtime.Mode)
	if meta.privilegedView() {
		out.Landlock = landlockJSON{Summary: "not-applied"}
	}
	if meta.WorkdirMode != "" {
		out.Workdir = &workdirJSON{Mode: meta.WorkdirMode, Workdir: meta.Workdir, ContainerPath: meta.WorkdirContainer}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func newAgentboxStartCmd() *cobra.Command {
	var (
		spaceName    string
		noAutoupdate bool
		noAttach     bool
		newSession   bool
	)
	cmd := &cobra.Command{
		Use:   "start [BOX]",
		Short: "Start or materialize a box from its declaration",
		Long: `Starts an existing stopped box container, or materializes a
configured box from its YAML when the container is absent. In both cases it
reconciles port forwards from the declaration.

An existing container is preserved. Changes requiring its replacement are
refused; inspect the box and explicitly use box recreate to apply them.

The container's bridge IP can change between runs, so any forward
systemd units that survived the previous stop are torn down before
reconcile re-installs them against the new IP.

After start, the agent CLI inside the box is autoupdated to the
current registry-latest version (best-effort; skipped silently when
the network or registry is unreachable). Pass --no-autoupdate to keep
the version baked into the image.

  yoloctl box start coder
  yoloctl box start coder --space prod-untrusted`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			name := args[0]
			if err := validateAgentboxName(name); err != nil {
				return err
			}
			var agentUpdated bool
			if err := startAgentbox(cmd, spaceName, name, noAutoupdate, noAttach, newSession, &agentUpdated); err != nil {
				return err
			}
			return maybeAutoAttach(cmd, spaceName, name, noAttach, noAutoupdate, agentUpdated)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().BoolVar(&noAutoupdate, "no-autoupdate", false, "Skip the post-start npm install -g <pkg>@latest that brings the agent CLI to current registry-latest. Use for air-gapped environments or when you want the version baked into the image.")
	addPassthroughFlag(cmd)
	cmd.Flags().BoolVar(&noAttach, "no-attach", false, "Skip the automatic attach to the box's session when the command finishes")
	cmd.Flags().BoolVar(&newSession, newSessionFlag, false, "Start a new agent conversation on the next session instead of resuming the box's own (refused while a session runs)")
	return cmd
}

// startAgentbox is the shared core of `box start` — and of `box restart`'s
// second leg. Under the per-agentbox reconcile lock it loads the declaration,
// resolves runtime state, preflights binds, and then either materializes a
// missing container (reconcile) or `nerdctl start`s a stopped one and
// re-reconciles its port forwards against the fresh bridge IP. Routes through
// the managed (session-aware) applier so the holder is handled consistently,
// and runs the post-start autoupdate unless noAutoupdate (updated, optional,
// reports whether that refresh actually ran — the auto-attach correlation).
// An already-running box is a notice + no-op. The lock is released when this
// returns, BEFORE any caller's auto-attach takes the terminal.
func startAgentbox(cmd *cobra.Command, spaceName, name string, noAutoupdate, noAttach, newSession bool, updated *bool) error {
	return startAgentboxWithForwardIntent(cmd, spaceName, name, noAutoupdate, noAttach, newSession, updated, nil)
}
func startAgentboxWithForwardIntent(cmd *cobra.Command, spaceName, name string, noAutoupdate, noAttach, newSession bool, updated *bool, forwardIntent *agentbox.Config) error {
	lock, err := lockSessionBoxFn(spaceName, name)
	if err != nil {
		return fmt.Errorf("acquire reconcile lock: %w", err)
	}
	defer lock.Release()

	c := container.New(spaceName)
	errw := cmd.ErrOrStderr()

	// Load the declared YAML first; the reconcile below re-applies
	// its port forwards. A missing declaration is a hard error —
	// yoloctl no longer reverse-engineers one from container state.
	cfg, err := loadManagedAgentbox(spaceName, name)
	if err != nil {
		return err
	}

	resolved, err := newBoxStateResolver().ResolveOne(cmd.Context(), spaceName, name, boxstate.RuntimeSummary)
	if err != nil {
		return err
	}
	if resolved.Declaration == boxstate.DeclarationInvalid {
		return asUserError(fmt.Errorf("box %q declaration is invalid: %s", name, resolved.ConfigError))
	}
	if resolved.Runtime == boxstate.RuntimeUnavailable {
		return asUserError(fmt.Errorf("box %q runtime is unavailable in space %q: %s", name, spaceName, boxStateReasonField(resolved)))
	}
	tracking, _, err := sessionTrackingFor(cfg)
	if err != nil {
		return err
	}
	if newSession && tracking == nil {
		return asUserError(fmt.Errorf("box %s does not track its agent conversation (resume policy manual or unsupported); --new-session has nothing to replace", name))
	}
	if err := prepareAutoAttach(cmd, spaceName, name, noAttach, false); err != nil {
		return err
	}
	if resolved.Runtime == boxstate.RuntimeRunning {
		if err := gateWithoutImageBuild(cmd.Context()); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(errw, "notice: box %s is already running\n", name)
		if newSession {
			if err := liveHolderError(cmd.Context(), c, name); err != nil {
				return err
			}
			return retireForNewSession(lock, tracking, errw)
		}
		return nil
	}
	stopped := resolved.Runtime == boxstate.RuntimeStopped
	if forwardIntent == nil {
		forwardIntent = copyStartForwardIntent(cfg)
	}
	if err := checkStartForwardIntent(cfg, forwardIntent); err != nil {
		return err
	}

	// Preflight BEFORE touching the container, so a workdir /
	// agent-state / extra mount that isn't live yet surfaces the
	// restart hint instead of a low-level `nerdctl start` failure
	// (it also normalizes the agent-state mount and enforces strict
	// account existence).
	// Refuse recreate-only drift before any image work: the same read-only
	// check repeats after preflight as the race guard.
	if stopped {
		if err := refuseRecreateBeforeImageWork(cmd.Context(), spaceName, cfg); err != nil {
			return err
		}
	}
	var prepared recordedNativeStart
	defer func() { closeNativeStart(prepared) }()
	nativeStart := false
	var qualify func(context.Context, *agentbox.Config) (bool, error)
	preflightCtx := cmd.Context()
	if resolved.Runtime == boxstate.RuntimeMissing {
		preflightCtx = defaultCreationContext(preflightCtx, cfg)
		qualify = func(check context.Context, declared *agentbox.Config) (bool, error) {
			return qualifyDefaultNativeCreation(check, c, declared)
		}
	}
	if resolved.Runtime == boxstate.RuntimeStopped {
		qualify = func(check context.Context, declared *agentbox.Config) (bool, error) {
			var err error
			prepared, nativeStart, err = prepareNativeExistingStart(check, c, declared, false)
			if err == nil && nativeStart {
				err = withdrawNativeStartForwards(check, c, declared.Name)
			}
			return nativeStart, err
		}
	}
	if err := preflightAgentboxRuntimeQualified(preflightCtx, c, cfg, true, qualify); err != nil {
		return err
	}

	if resolved.Runtime == boxstate.RuntimeMissing {
		inspector := agentbox.LiveInspector{Client: c}
		applier := newManagedLiveApplier(c, errw)
		result, err := agentbox.ReconcileForStart(cmd.Context(), cfg, inspector, applier)
		if err != nil {
			return err
		}
		if err := printReconcileResult(cmd.OutOrStdout(), name, result); err != nil {
			return err
		}
		if err := applySessionStart(lock, tracking, true, newSession, errw); err != nil {
			return err
		}
		if !noAutoupdate {
			if err := autoupdateAgentboxIfNeeded(cmd.Context(), c, name, cfg.Agent, errw, updated); err != nil {
				return fmt.Errorf("autoupdate %s: %w", name, err)
			}
		}
		return nil
	}

	// Refuse replacement before starting the old container or clearing its
	// session state. The post-start reconcile repeats the same preservation
	// guard, so changed observations cannot later authorize a replacement.
	inspector := agentbox.LiveInspector{Client: c}
	if err := agentbox.CheckStart(cmd.Context(), cfg, inspector); err != nil {
		return err
	}
	// Remove retained transports before starting a new container run. A lost
	// native stop acknowledgement must reach the private uncertainty bridge;
	// it cannot be downgraded to a warning followed by another runtime effect.
	if !nativeStart {
		if err := port.UnforwardAgentbox(cmd.Context(), c, name); err != nil {
			return fmt.Errorf("teardown retained port forwards on %s: %w", name, err)
		}
	}
	// Existing stopped container: `nerdctl start` it, then reconcile
	// port forwards against its fresh bridge IP.
	if err := startAndReconcileExistingAgentbox(cmd.Context(), c, cfg, prepared, inspector, newManagedLiveApplierForStart(c, errw, forwardIntent)); err != nil {
		return err
	}

	if err := applySessionStart(lock, tracking, stopped, newSession, errw); err != nil {
		return err
	}

	if !noAutoupdate {
		if err := autoupdateAgentboxIfNeeded(cmd.Context(), c, name, cfg.Agent, errw, updated); err != nil {
			return fmt.Errorf("autoupdate %s: %w", name, err)
		}
	}
	return nil
}

func newAgentboxCreateCmd() *cobra.Command {
	var (
		opts     agentboxCreateOptions
		specs    agentboxCreateSpecs
		operator bool
	)
	cmd := &cobra.Command{
		Use:   "create BOX",
		Short: "Create a confined box inside a space",
		Long: `Creates a containerized sandbox in the space, with openshell-sandbox
as PID 1 (Landlock + seccomp + netns + L7 proxy enforced). The
container UID is remapped to the host UID at start so /workdir
writes stay readable by the user's IDE.

The workdir defaults to the space's configured default workdir. Pass
--workdir PATH to share a different host directory at /workdir (use
--workdir . for the current directory), or --workdir-mode ephemeral for
throwaway VM-local storage. --read-only omits the fs.workdir.write
capability so /workdir is mounted read-only.

--operator adds yolo.space.operator and yolo.chat.skill, with open networking
unless --unconfined-network=false is explicit. This grants full box authority
in this space, including other accounts and their credentials, stronger grants,
delegation and workload destruction. Extra features and explicit workdir choices
are preserved. The preset is expanded only at creation; it does not enroll in
chat or add future preset features to existing boxes.

--privileged declares a privileged development box instead of a confined one:
no OpenShell confinement and sudo for the sandbox user, which is root over the
space VM. Only the host starts it; space operators may declare it with
--manifest-only and use, restart or recreate it while it runs.

  yoloctl box create coder --agent claude --workdir ~/src/myrepo
  yoloctl box create coordinator --agent codex --account personal --operator
  yoloctl box create reviewer --agent claude --workdir ~/src/myrepo --read-only
  yoloctl box create gpu-coder --agent claude --workdir . --cpus 8 --memory 16g
  yoloctl box create ephemeral-bot --agent codex --workdir-mode ephemeral`,
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(1)(cmd, args); err != nil {
				return err
			}
			// Args survives controller routing, so invalid names fail before
			// selecting or contacting a VM as well as in the native worker.
			return validateAgentboxName(args[0])
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Name = args[0]
			if err := validateAgentboxName(opts.Name); err != nil {
				return err
			}
			ports, err := parsePortFlags(specs.Ports)
			if err != nil {
				return err
			}
			opts.Ports = ports
			mounts, err := parseMountFlags(specs.Mounts)
			if err != nil {
				return err
			}
			opts.Mounts = mounts
			envMap, err := parseEnvFlags(specs.Envs)
			if err != nil {
				return err
			}
			opts.Env = envMap
			if err := resolveSpaceFlag(cmd, &opts.Space); err != nil {
				return err
			}
			// Resolve --agent / --account into the stored Agent + Account
			// fields. --agent omitted/none ⇒ generic shell (Agent "none",
			// Account "none", DECISION A); a real agent with no --account gets
			// the default account (DECISION B). The raw flag value is captured
			// first so resolution can tell "omitted" from "none".
			agentFlag := opts.Agent
			resolvedAgent, resolvedAccount, err := resolveAgentAndAccount(opts.Space, agentFlag, opts.Account)
			if err != nil {
				return err
			}
			opts.Agent, opts.Account = resolvedAgent, resolvedAccount
			if err := requireAccountForCreate(opts.Space, opts.Account); err != nil {
				return err
			}
			// --agent prefills the base image and the box's features from the
			// agent catalog. Required features include the agent CLI installer
			// and contribute their capabilities at compose time. Explicit
			// --image / --features win; generic-shell boxes skip it.
			if hasRealAgent(opts.Agent) {
				profile, err := lookupAgentProfile(opts.Agent)
				if err != nil {
					return err
				}
				applyAgentProfileDefaults(cmd, &opts, profile)
			}
			if operator {
				applyOperatorCreatePreset(cmd, &opts)
			}
			// --workdir defaults to the space's configured default workdir;
			// --workdir-mode ephemeral overrides to VM-local storage. The space
			// owns its mounts, so create never adds one — preflight validates the
			// resolved workdir is under a live grant.
			wd, err := resolveEffectiveWorkdir(opts.Space, opts.Name, opts.Workdir, opts.WorkdirMode, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			opts.workdir = wd
			// --manifest-only writes the YAML without touching runtime, so it
			// does not require (or start) the space VM.
			if !opts.ManifestOnly {
				if err := ensureSpaceRunning(cmd.Context(), opts.Space); err != nil {
					return err
				}
				if err := prepareAutoAttach(cmd, opts.Space, opts.Name, opts.NoAttach, true); err != nil {
					return err
				}
			}
			var agentUpdated bool
			if err := createAgentbox(cmd.Context(), opts, cmd.ErrOrStderr(), &agentUpdated); err != nil {
				return err
			}
			if opts.ManifestOnly {
				return nil
			}
			return maybeAutoAttach(cmd, opts.Space, opts.Name, opts.NoAttach, opts.NoAutoupdate, agentUpdated)
		},
	}
	registerAgentboxCreateFlags(cmd, &opts, &specs)
	cmd.Flags().BoolVar(&operator, "operator", false, "Apply the space operator preset: yolo.space.operator + yolo.chat.skill, open networking unless explicitly disabled, and the usual space-default workdir. Grants full box authority, including other accounts/credentials, stronger grants, delegation and destruction.")
	return cmd
}

// agentboxCreateSpecs holds the raw repeatable --port/--mount/--env specs that
// create-shaped commands parse in their RunE (the parsed forms land in
// agentboxCreateOptions).
type agentboxCreateSpecs struct {
	Ports  []string
	Mounts []string
	Envs   []string
}

// registerAgentboxCreateFlags registers the common creation options on cmd,
// binding into opts + specs. Shared by `box create` and `box copy` so their
// overrides agree. The creation-only --operator preset is registered separately.
func registerAgentboxCreateFlags(cmd *cobra.Command, opts *agentboxCreateOptions, specs *agentboxCreateSpecs) {
	cmd.Flags().StringVar(&opts.Space, "space", defaultSpaceName, "Space to host the box")
	cmd.Flags().StringVar(&opts.Workdir, "workdir", "", "Host directory to share at /workdir (use . for the current dir). Defaults to the space's default workdir; incompatible with --workdir-mode ephemeral.")
	cmd.Flags().StringVar(&opts.WorkdirMode, "workdir-mode", agentbox.WorkdirModeHost, "Workdir mode: host (a host --workdir, the default) or ephemeral (throwaway VM-local /workdir storage, wiped on destroy)")
	cmd.Flags().StringVar(&opts.Image, "image", defaultAgentboxImage, "Container image (overrides the agent image)")
	cmd.Flags().StringVar(&opts.Agent, "agent", "", fmt.Sprintf("Agent this box will host (%v). Pre-fills --image (the agent definition's plain base image) and --features (the agent's requires_features, which install the agent CLI); those features pull in their required capabilities. Required for 'yoloctl agent update AGENT' to find this box. Omit (or pass 'none') for a generic-shell box.", agentProfileNames()))
	cmd.Flags().StringVar(&opts.Account, "account", "", "Account this box selects (a plain name, e.g. default or personal). Defaults to the space's initial account for a real --agent; the selected account must exist in that space ('yoloctl account create --space SPACE'). Generic-shell boxes use 'none'.")
	cmd.Flags().StringSliceVar(&opts.AgentExtraArgs, "agent-extra-args", nil, "Extra args appended after the agent definition's default_args when yoloctl starts a managed agent session. Persisted as agent_extra_args in the box manifest. Repeatable / comma-separated. Non-secret only.")
	cmd.Flags().StringVar(&opts.AgentSessionResume, "agent-session-resume", "", "Conversation policy for agents that declare one (Claude): last resumes the box's own conversation on every start, never starts a new one each time, manual leaves identity to agent_extra_args. Unset means manual when agent_extra_args carry an identity flag (such as --resume), otherwise last.")
	cmd.Flags().StringArrayVar(&opts.SessionCommand, "session-command", nil, "Persist one argv element for an agentless managed session (repeat for each argument; no shell expansion). Applied on the next session start. Non-secret only.")
	cmd.Flags().StringVar(&opts.CPUs, "cpus", "", "CPU time cap in CPUs, e.g. 2 or 0.5 (omit to share all VM CPUs without a box cap)")
	cmd.Flags().StringVar(&opts.Memory, "memory", "shared", "Memory limit (shared uses the VM workload pool; e.g. 2g sets an additional box limit)")
	cmd.Flags().BoolVar(&opts.ReadOnly, "read-only", false, "Drop the default fs.workdir.write capability so /workdir is mounted read-only (sugar for --drop-capability fs.workdir.write)")
	cmd.Flags().BoolVar(&opts.UnconfinedNetwork, "unconfined-network", false, "Drop the network confinement layer: the agent gets the VM's OPEN outbound network — no egress allow-list, no L7 proxy (openshell --mode=process). Filesystem (Landlock), seccomp, and the VM boundary still apply, but a misaligned agent could exfiltrate to any reachable host. Opt-in for trusted workloads; declared network.* capabilities become moot.")
	cmd.Flags().BoolVar(&opts.Privileged, "privileged", false, privilegedFlagHelp)
	cmd.Flags().StringArrayVar(&opts.ExtraFeatures, "feature", nil, "Feature name or OCI reference (repeatable)")
	cmd.Flags().StringArrayVar(&opts.FeatureOptionSpecs, "feature-option", nil, "OCI Feature option: FEATURE.OPTION=VALUE (repeatable; non-secret)")
	cmd.Flags().StringSliceVar(&opts.Features, "features", nil, "Comma-separated feature names (yolo.space.operator, yolo.chat.skill, agy, claude, codex, gemini, opencode, pi, github, pypi, pip, npm, go, python-3.12, cargo, aws). Each feature grants its required capabilities AND installs its tool into the box's feature-composed runtime image (image = base + resolved features), built-or-selected on demand at create/reconcile and cached. The manual yoloctl image build --features FEATURE --tag REF path still exists for prebuilding.")
	cmd.Flags().StringSliceVar(&opts.Capabilities, "capability", nil, "Explicit positive capability to grant (repeatable), e.g. network.github, or fs.pty.allocate to let the box allocate pseudo-terminals (tmux, script, PTY-backed tests). See yoloctl box inspect for the effective set.")
	cmd.Flags().StringSliceVar(&opts.DropCapabilities, "drop-capability", nil, "Drop a capability from the box's default+explicit set (repeatable), e.g. fs.workdir.write. Cannot deny a capability a resolved feature requires (that fails with an explanation).")
	cmd.Flags().StringArrayVar(&specs.Mounts, "mount", nil, "Extra bind mount source:target[:ro] (repeatable; host source accepts ~ or relative paths, target must be absolute). Advanced escape hatch beyond /workdir and the managed account-state mount.")
	cmd.Flags().StringArrayVar(&specs.Envs, "env", nil, "Set a literal environment variable KEY=VALUE in the box (repeatable). Non-secret by convention — put secrets in the box's account.")
	cmd.Flags().StringArrayVar(&specs.Ports, "port", nil, "Forward a port into the box (repeatable). Docker host:container order: published:target (e.g. 8080:3000), or a bare target (e.g. 3000) which publishes on the same port. host_ip/protocol default to 127.0.0.1/tcp.")
	cmd.Flags().BoolVar(&opts.ManifestOnly, "manifest-only", false, "Write the box manifest on the running space controller without creating a container. Materialize later with: yoloctl box reconcile NAME.")
	cmd.Flags().BoolVar(&opts.NoAutoupdate, "no-autoupdate", false, "Skip the post-create npm install -g <pkg>@latest that brings the agent CLI to current registry-latest. Use this for air-gapped environments or when you want the version baked into the image.")
	addPassthroughFlag(cmd)
	cmd.Flags().BoolVar(&opts.NoAttach, "no-attach", false, "Skip the automatic attach to the box's session when the command finishes")
}

// agentboxCreateOptions is the input to the shared createAgentbox core.
type agentboxCreateOptions struct {
	Space string
	Name  string
	Image string
	// Workdir is the raw --workdir flag value (host path, may be ".").
	// WorkdirMode selects host (default) or ephemeral (VM-local) storage;
	// the resolved block lands in workdir.
	Workdir     string
	WorkdirMode string
	// Agent is the registered agentProfile name this agentbox hosts
	// ("claude", "codex", ...). "none" (or empty, pre-resolution) means
	// "generic shell" — no agent-specific defaulting and no profile lookup.
	// When set to a real agent without an explicit --image / --features,
	// createAgentbox prefills both from the profile.
	Agent string
	// Account is the resolved plain account name this agentbox selects, or
	// "none" for a generic-shell box. Resolved by the CLI before
	// createAgentbox: a real agent with no --account gets account.Default;
	// --agent omitted/none yields "none".
	Account string
	// AgentExtraArgs are persistent per-box args appended after the selected
	// agent definition's default_args whenever yoloctl starts the managed
	// session. Non-secret: argv is observable.
	AgentExtraArgs []string
	// AgentSessionResume is the declared conversation policy (last, never,
	// manual); empty leaves it unset.
	AgentSessionResume string
	SessionCommand     []string
	// Features are the explicit --features; Capabilities are the explicit
	// --capability grants. Both feed the positive-grant policy composer.
	ExtraFeatures      []string
	FeatureOptionSpecs []string
	FeatureOptions     map[string]map[string]string
	FeatureLocks       []ocifeature.Lock
	Features           []string
	Capabilities       []string
	// DropCapabilities are the --drop-capability names removed from the box's
	// default+explicit capability set at config-build time (--read-only appends
	// fs.workdir.write here). A drop can only remove from the explicit/default
	// set — it can NOT deny a capability a resolved feature requires; a drop that
	// a feature would re-add fails validation in buildAgentboxConfig.
	DropCapabilities []string
	// Mounts are the explicit --mount binds (source:target[:ro]), the advanced
	// bind-mount escape hatch beyond the workdir + managed agent-state mounts.
	Mounts []agentbox.Mount
	// Env are the explicit --env KEY=VALUE literals (non-secret by convention).
	// Merged into Config.Env alongside credential-derived entries.
	Env map[string]agentbox.EnvVar
	// Ports are the explicit --port forwards (published:target or bare
	// target), parsed from the CLI. Merged with devcontainer-derived
	// forwards in buildAgentboxConfig.
	Ports  []agentbox.Port
	CPUs   string
	Memory string
	// skipDevcontainerPorts disables the devcontainer.json forward discovery
	// in buildAgentboxConfig. Set by `box copy`, whose Ports are the SOURCE
	// manifest's persisted snapshot (per the contract, forwards are read once
	// at create and never re-read) — re-discovering would union today's file
	// into a verbatim copy. Zero value keeps create/run discovery on.
	skipDevcontainerPorts bool
	// ReadOnly is the --read-only flag: when true, createAgentbox omits the
	// fs.workdir.write capability so /workdir is mounted read-only.
	ReadOnly bool
	// UnconfinedNetwork is the --unconfined-network flag: when true,
	// createAgentbox sets Config.UnconfinedNetwork so the box runs openshell
	// with the network layer dropped (open egress). See the Config field for
	// the full trade-off; all other confinement layers stay on.
	UnconfinedNetwork bool
	// Privileged is the --privileged flag: the box declares privileged: true
	// (Config.Privileged).
	Privileged bool
	// workdir is the resolved working-directory grant (host or
	// ephemeral), computed by the CLI before ensureSpace so a fresh VM can
	// mount it.
	workdir agentbox.Workdir
	// ManifestOnly, when true, writes the agentbox manifest (YAML) and stops:
	// no VM start, no container reconcile, no auth bridge. The box is
	// materialized later with `box reconcile` / `box start`. The advanced
	// "declare now, apply later" path (manifest/runtime model, Phase 9).
	ManifestOnly bool
	// NoAutoupdate, when true, skips the post-create / post-start
	// `npm install -g <pkg>@latest` refresh that brings the agent
	// CLI up to current registry-latest. Default false so the
	// experience matches a host-native `claude` install (autoupdate
	// on by default; users opt out only when they specifically want
	// the baked-in version).
	NoAutoupdate bool
	// NoAttach, when true, skips the automatic post-command attach to the
	// box's session. Consumed by the create/copy RunE tails only —
	// createAgentbox never attaches. The attach is also skipped when stdin
	// is not a terminal (maybeAutoAttach).
	NoAttach bool
}

// parsePortFlags parses the repeatable --port flag values into agentbox.Ports.
// Each spec is "published:target" (Docker host:container order) or a bare
// "target" (then published == target), via port.ParseMapping. host_ip and
// protocol are left at their manifest defaults (127.0.0.1/tcp). A parse error
// is returned as a user error so a bad --port value fails the command cleanly.
func parsePortFlags(specs []string) ([]agentbox.Port, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]agentbox.Port, 0, len(specs))
	for _, s := range specs {
		published, target, err := port.ParseMapping(s)
		if err != nil {
			return nil, asUserError(fmt.Errorf("--port %q: %w", s, err))
		}
		out = append(out, agentbox.Port{Published: published, Target: target})
	}
	return out, nil
}

// parseMountFlags parses the repeatable --mount flag values into agentbox
// Mounts. Each spec is "source:target[:ro]" — a host source (tilde-expanded
// and made absolute), an absolute in-container target, and an optional ":ro"
// suffix marking the bind read-only. This is the advanced bind-mount escape
// hatch; the workdir + managed agent-state mounts are handled separately, so
// --mount is for extra binds only. A malformed spec is returned as a user error
// so a bad value fails the command cleanly with nothing persisted.
func parseMountFlags(specs []string) ([]agentbox.Mount, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]agentbox.Mount, 0, len(specs))
	for _, s := range specs {
		m, err := parseMountSpec(s)
		if err != nil {
			return nil, asUserError(fmt.Errorf("--mount %q: %w", s, err))
		}
		out = append(out, m)
	}
	return out, nil
}

// parseMountSpec parses one "source:target[:ro]" mount spec. A plain colon
// split yields exactly two parts (source, target), or three with the last being
// "ro". The host source accepts the same ~/relative spellings as --workdir and
// is normalized to absolute here; the container target must already be absolute.
func parseMountSpec(s string) (agentbox.Mount, error) {
	parts := strings.Split(s, ":")
	m := agentbox.Mount{}
	switch len(parts) {
	case 2:
		m.Source, m.Target = parts[0], parts[1]
	case 3:
		if parts[2] != "ro" {
			return agentbox.Mount{}, fmt.Errorf("invalid mode %q (only ':ro' is supported)", parts[2])
		}
		m.Source, m.Target, m.ReadOnly = parts[0], parts[1], true
	default:
		return agentbox.Mount{}, errors.New("expected source:target[:ro]")
	}
	if m.Source == "" || m.Target == "" {
		return agentbox.Mount{}, errors.New("expected source:target[:ro]")
	}
	expanded, err := expandUserPath(m.Source)
	if err != nil {
		return agentbox.Mount{}, fmt.Errorf("expand source %q: %w", m.Source, err)
	}
	m.Source = expanded
	if !filepath.IsAbs(m.Target) {
		return agentbox.Mount{}, fmt.Errorf("target %q must be an absolute path", m.Target)
	}
	return m, nil
}

// parseEnvFlags parses the repeatable --env flag values into a literal env map.
// Each spec is "KEY=VALUE", split on the FIRST '=' so the value may itself
// contain '=' (e.g. --env X=a=b → X="a=b"). The value is stored as a LITERAL
// EnvVar (env is non-secret literal by convention — secret values belong in the
// box's account; --env is the non-secret escape hatch and does NOT reject
// secret-like KEYs). A missing '=' or empty KEY is a user error.
func parseEnvFlags(specs []string) (map[string]agentbox.EnvVar, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make(map[string]agentbox.EnvVar, len(specs))
	for _, s := range specs {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return nil, asUserError(fmt.Errorf("--env %q: expected KEY=VALUE", s))
		}
		key := s[:eq]
		if key == "" {
			return nil, asUserError(fmt.Errorf("--env %q: empty key", s))
		}
		out[key] = agentbox.EnvVar{Literal: s[eq+1:]}
	}
	return out, nil
}

// resolveEffectiveWorkdir builds an agentbox's workdir for create / run:
// --workdir-mode ephemeral → VM-local storage; --workdir PATH → that host
// dir; neither → the space's configured default workdir. The space must
// already exist (its default workdir is required at `space create`) — a
// missing space is a hard error, since box creation never invents a space's
// default. The resolved host path is NOT yet checked against the space's
// grants; preflight does that before anything is saved.
func resolveEffectiveWorkdir(spaceName, agentboxName, workdirFlag, workdirMode string, notice io.Writer) (agentbox.Workdir, error) {
	switch workdirMode {
	case "", agentbox.WorkdirModeHost:
		// fall through to host resolution below.
	case agentbox.WorkdirModeEphemeral:
		if workdirFlag != "" {
			return agentbox.Workdir{}, asUserError(errors.New("--workdir is incompatible with --workdir-mode ephemeral"))
		}
		return agentbox.NewEphemeralWorkdir(agentboxName), nil
	default:
		return agentbox.Workdir{}, asUserError(fmt.Errorf("unknown --workdir-mode %q; valid: %s|%s",
			workdirMode, agentbox.WorkdirModeHost, agentbox.WorkdirModeEphemeral))
	}
	workdir := workdirFlag
	if workdir == "" {
		scfg, err := spaceconfig.Load(spaceName)
		if errors.Is(err, fs.ErrNotExist) {
			return agentbox.Workdir{}, asUserError(fmt.Errorf(
				"space %q is not configured — create it first (a space needs a default workdir):\n"+
					"    yoloctl space create %s --workdir PATH --account NAME",
				spaceName, spaceName))
		}
		if err != nil {
			return agentbox.Workdir{}, err
		}
		workdir = scfg.DefaultWorkdir
		_, _ = fmt.Fprintf(notice,
			"notice: using space %s default workdir %s (override with --workdir PATH or --workdir-mode ephemeral)\n",
			spaceName, workdir)
	}
	return buildWorkdir(agentboxName, workdir, agentbox.WorkdirModeHost, notice)
}

func buildWorkdir(name, workdir, workdirMode string, notice io.Writer) (agentbox.Workdir, error) {
	switch {
	case workdirMode == agentbox.WorkdirModeEphemeral && workdir != "":
		return agentbox.Workdir{}, asUserError(errors.New("--workdir is incompatible with --workdir-mode ephemeral"))
	case workdirMode == agentbox.WorkdirModeEphemeral:
		return agentbox.NewEphemeralWorkdir(name), nil
	case workdir == "":
		return agentbox.Workdir{}, asUserError(errors.New(
			"a workdir is required: pass --workdir PATH (use --workdir . for the current directory) or --workdir-mode ephemeral for VM-local storage"))
	default:
		expanded, err := expandUserPath(workdir)
		if err != nil {
			return agentbox.Workdir{}, asUserError(err)
		}
		wd, exists, err := agentbox.NewHostWorkdir(expanded)
		if err != nil {
			return agentbox.Workdir{}, asUserError(err)
		}
		if !exists {
			if err := os.MkdirAll(wd.Canonical, 0o755); err != nil {
				return agentbox.Workdir{}, asUserError(fmt.Errorf("create workdir %s: %w", wd.Canonical, err))
			}
			_, _ = fmt.Fprintf(notice, "notice: created host workdir %s (will mount at /workdir)\n", wd.Canonical)
		}
		return wd, nil
	}
}

// flagChanged is cmd.Flag(name).Changed with a nil-safety net for
// flags the calling command doesn't register. Shared command helpers use it
// to distinguish an omitted flag from its default value.
func flagChanged(cmd *cobra.Command, name string) bool {
	f := cmd.Flag(name)
	return f != nil && f.Changed
}

// parseLabels splits a comma-separated "k=v,k2=v2" string (as emitted by
// `nerdctl ps --format json` in its Labels field) into a map. Whitespace
// between entries is trimmed.
func parseLabels(s string) map[string]string {
	out := map[string]string{}
	if s == "" {
		return out
	}
	for kv := range strings.SplitSeq(s, ",") {
		kv = strings.TrimSpace(kv)
		if eq := strings.IndexByte(kv, '='); eq > 0 {
			out[kv[:eq]] = kv[eq+1:]
		}
	}
	return out
}

// loadManagedAgentbox loads an agentbox's declared YAML, mapping a
// missing declaration to a clear user-error. yoloctl no longer
// reverse-engineers ("synthesizes") config from a live container —
// every box is created via `yoloctl box create`, which
// writes the YAML up front. A missing YAML therefore means
// the agentbox isn't managed by this build (hand-removed, or made by an
// older one); apply / edit / set / policy / start / recreate all refuse
// rather than inventing a declaration from container state.
func loadManagedAgentbox(space, name string) (*agentbox.Config, error) {
	cfg, err := agentbox.Load(space, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, asUserError(fmt.Errorf(
				"box %q has no declaration in space %q (not managed by yoloctl).\n"+
					"  create it:  yoloctl box create %s --space %s --workdir PATH --agent AGENT",
				name, space, name, space))
		}
		return nil, fmt.Errorf("load box manifest: %w", err)
	}
	return cfg, nil
}

// hasRealAgent reports whether an agent flag value names a real managed agent
// (not the generic-shell sentinel). Mirrors agentbox.Config.HasAgent for the
// pre-Config resolution sites in the CLI.
func hasRealAgent(agentFlag string) bool {
	return agentFlag != "" && agentFlag != account.None
}

// resolveAgentAndAccount selects the space default for a real agent when
// --account is omitted. Generic shells use none/none. Existence is checked
// before persistence and runtime setup.
func resolveAgentAndAccount(space, agentFlag, accountFlag string) (agent, acct string, err error) {
	if !hasRealAgent(agentFlag) {
		// Generic shell. A real account name makes no sense without an agent.
		if accountFlag != "" && accountFlag != account.None {
			return "", "", asUserError(fmt.Errorf(
				"--account %q requires a real --agent: omit --agent (or pass --agent none) for a generic-shell box, which uses account: none",
				accountFlag))
		}
		return account.None, account.None, nil
	}
	if accountFlag == account.None {
		return "", "", asUserError(fmt.Errorf(
			"--agent %s requires a real account: real agents require real accounts (account: none is only for generic-shell boxes). Omit --account for the space default, or pass a real --account",
			agentFlag))
	}
	if accountFlag == "" {
		cfg, err := spaceconfig.Load(space)
		if err != nil {
			return "", "", err
		}
		return agentFlag, cfg.DefaultAccount, nil
	}
	return agentFlag, accountFlag, nil
}

// lockBoxAccount validates a real account name and acquires its per-account
// lock. The caller must defer the returned release. Never call it while
// already holding the same account's lock: flock(2) is per open file
// description, so a second acquisition in the same process self-deadlocks.
func lockBoxAccount(space, acct string) (release func(), err error) {
	if err := account.ValidateName(acct); err != nil {
		return nil, asUserError(err)
	}
	return account.Lock(space, acct)
}

// ensureAccountSelectableLocked requires a valid account in this space.
// Callers hold its account lock through their dependent writes.
func ensureAccountSelectableLocked(space, acct string) error {
	if acct == "" || acct == account.None {
		return nil
	}
	_, err := account.Load(space, acct)
	if err == nil {
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return asUserError(fmt.Errorf(
		"account %q does not exist in space %q; create it first: yoloctl account create %s --space %s",
		acct, space, acct, space))
}

// requireAccountForCreate also runs before VM boot so a typo has no runtime side effects.
func requireAccountForCreate(space, acct string) error {
	if acct == "" || acct == "none" {
		return nil
	}
	release, err := lockBoxAccount(space, acct)
	if err != nil {
		return err
	}
	defer release()
	return ensureAccountSelectableLocked(space, acct)
}

// persistBoxManifestWithAccount holds the selected account lock through
// validation and saving the box declaration. No native filesystem is touched.
// Lock order is space operation, box, account.
func persistBoxManifestWithAccount(cfg *agentbox.Config) (result error) {
	defer func() {
		if result == nil {
			warnBoxAuthority(cfg)
		}
	}()
	if cfg.HasRealAccount() {
		release, err := lockBoxAccount(cfg.Space, cfg.Account)
		if err != nil {
			return err
		}
		defer release()
		if err := ensureAccountSelectableLocked(cfg.Space, cfg.Account); err != nil {
			return err
		}

	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save box manifest: %w", err)
	}
	return nil
}

// saveRepointableBoxManifest validates a changed account reference under its
// lock before persisting. Missing and corrupt identities fail closed.
func saveRepointableBoxManifest(cfg *agentbox.Config) (result error) {
	defer func() {
		if result == nil {
			warnBoxAuthority(cfg)
		}
	}()
	if !cfg.HasRealAccount() {
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("save box manifest: %w", err)
		}
		return nil
	}
	release, err := lockBoxAccount(cfg.Space, cfg.Account)
	if err != nil {
		return err
	}
	defer release()
	if _, err := account.Load(cfg.Space, cfg.Account); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return asUserError(fmt.Errorf(
				"box %s selects account %q, which does not exist — create it first: yoloctl account create %s --space %s",
				cfg.Name, cfg.Account, cfg.Account, cfg.Space))
		}
		return err
	}
	if err := cfg.Save(); err != nil {
		return fmt.Errorf("save box manifest: %w", err)
	}
	return nil
}

// buildAgentboxConfig resolves create options into the desired agentbox
// Config WITHOUT any persistence or VM mutation: capability/feature resolution,
// workdir-flag validation, credential-env discovery, devcontainer
// port-forward derivation, and in-memory agent-state mount normalization.
// createAgentbox calls it and then preflights / saves / reconciles; `yoloctl
// run`'s reuse check calls it to compare the desired config against
// an already-saved one.
//
// Policy is a computed artifact, not an input: the box declares features +
// explicit capabilities, and the openshell policy is derived from the effective
// set (explicit + feature-required) at reconcile time. A writable
// /workdir is the fs.workdir.write capability — added by default and
// omitted by --read-only.
//
// Returns the config and the resolved agent-state mount (used for persistence
// preparation and the post-save notice). New OCI Feature selections may fetch
// metadata and populate the advisory cache; no declaration or runtime is created.
func buildAgentboxConfig(ctx context.Context, opts agentboxCreateOptions) (*agentbox.Config, agentStateMount, error) {
	opts.Features = append(append([]string(nil), opts.Features...), opts.ExtraFeatures...)
	featureOptions, optionErr := ocifeature.ApplyOptions(opts.Features, opts.FeatureOptions, opts.FeatureOptionSpecs)
	if optionErr != nil {
		return nil, agentStateMount{}, optionErr
	}
	// A box need not declare any policy intent. A generic-shell box (Agent
	// "none"/"") with no features and no explicit capabilities composes to a
	// maximally locked-down policy — read-only system paths, the default
	// writable /workdir (or read-only under --read-only), and no network egress
	// (default-deny). That is a valid sandbox, not a misconfiguration, so there
	// is no "must declare a policy" guard here: the empty set IS the safest box.

	if len(opts.AgentExtraArgs) > 0 && !hasRealAgent(opts.Agent) {
		return nil, agentStateMount{}, asUserError(errors.New("--agent-extra-args requires a real --agent (generic-shell boxes do not have managed agent argv)"))
	}
	if opts.AgentSessionResume != "" && !hasRealAgent(opts.Agent) {
		return nil, agentStateMount{}, asUserError(errors.New("--agent-session-resume requires a real --agent (generic-shell boxes have no agent conversation)"))
	}

	// Capabilities: explicit --capability grants, plus the workdir-write
	// capability by default. --drop-capability removes names from this
	// default+explicit set; --read-only is sugar for dropping fs.workdir.write
	// (so /workdir is mounted read-only). A drop only ever removes from the
	// explicit/default set — it cannot deny a capability a resolved feature
	// requires (validated below).
	caps := append([]string(nil), opts.Capabilities...)
	if !slices.Contains(caps, capabilities.WorkdirWriteCapability) {
		caps = append(caps, capabilities.WorkdirWriteCapability)
	}
	dropped := append([]string(nil), opts.DropCapabilities...)
	if opts.ReadOnly {
		// Sugar: --read-only drops the default workdir-write grant. Idempotent
		// with an explicit --drop-capability fs.workdir.write.
		dropped = append(dropped, capabilities.WorkdirWriteCapability)
	}
	caps = dropCapabilities(caps, dropped)
	// A drop is confined to the explicit/default set: it cannot deny a
	// capability a resolved feature requires. Fail closed if a requested drop
	// would still be re-added by the effective feature set, rather than silently
	// keeping it.
	if err := validateCapabilityDrops(dropped, opts.Features, opts.Agent); err != nil {
		return nil, agentStateMount{}, err
	}

	// Fail fast on an invalid resolved workdir before any VM work. The actual
	// /workdir prep (ephemeral dir creation, host mount-liveness check) runs
	// AFTER the agentbox YAML is saved — see prepareWorkdir below — so a
	// restart-to-apply-the-mount path doesn't lose the declaration.
	switch opts.workdir.Mode {
	case agentbox.WorkdirModeHost:
	case agentbox.WorkdirModeEphemeral:
		// ok
	default:
		return nil, agentStateMount{}, asUserError(fmt.Errorf("box %s has no workdir; pass --workdir or --workdir-mode ephemeral", opts.Name))
	}

	// Env is the explicit --env KEY=VALUE literals only (non-secret by
	// convention). A box does NOT own account secret values:
	// agent-declared secret_injection rules deliver supported account secrets
	// through the sandbox supervisor. Image-baked env (PATH, etc.) lives in the
	// container image, not the YAML.
	envMap := opts.Env

	// Ports: explicit --port flags first, then any from the host workdir's
	// devcontainer.json. Populated into Config.Ports so LiveApplier.Create
	// installs them as part of the create sequence. Ephemeral workdirs have no
	// host dir to read, so the devcontainer source is host-mode only. A
	// derived forward whose published listener is already claimed by an
	// explicit --port is skipped: Validate rejects the duplicate tuple, and an
	// explicit flag outranks discovery.
	ports := append([]agentbox.Port(nil), opts.Ports...)
	if !opts.skipDevcontainerPorts && opts.workdir.IsHost() {
		if dcfg, err := devcontainer.FindForWorkdir(opts.workdir.MountSource()); err == nil && dcfg != nil {
			for _, m := range dcfg.ForwardPorts {
				p := agentbox.Port{
					Published: m.Published,
					Target:    m.Target,
				}
				if claimsPublishedListener(ports, p) {
					continue
				}
				ports = append(ports, p)
			}
		}
	}

	cfg := &agentbox.Config{
		Name:           opts.Name,
		Space:          opts.Space,
		Agent:          opts.Agent,
		Account:        opts.Account,
		Image:          opts.Image,
		AgentExtraArgs: append([]string(nil), opts.AgentExtraArgs...),
		SessionCommand: append([]string(nil), opts.SessionCommand...),
		Workdir:        opts.workdir,
		Resources:      agentbox.Resources{CPUs: opts.CPUs, Memory: opts.Memory},
		Features:       opts.Features,
		FeatureOptions: featureOptions,
		FeatureLocks:   opts.FeatureLocks,
		Capabilities:   caps,
		Env:            envMap,
		Ports:          ports,
		// Explicit --mount binds (the advanced escape hatch). The managed
		// agent-state slot is appended afterward by applyAgentStateMount, so
		// these are the user's extra binds only.
		Mounts: append([]agentbox.Mount(nil), opts.Mounts...),
		// --unconfined-network: drop the openshell network layer (open egress).
		// All other confinement layers stay on; surfaced loudly by box show /
		// box inspect.
		UnconfinedNetwork: opts.UnconfinedNetwork,
		Privileged:        opts.Privileged,
	}
	if opts.AgentSessionResume != "" {
		cfg.AgentSession = &agentbox.AgentSession{Resume: opts.AgentSessionResume}
	}

	// Every declared name must resolve before any caller persists this config:
	// create saves the manifest BEFORE its reconcile composes the policy (and
	// --manifest-only never composes it), so an unknown --capability/--features
	// name would otherwise leave a declaration that blocks a corrected re-run
	// (create-only) until it is destroyed.
	if err := requireResolvableManifest(ctx, cfg); err != nil {
		return nil, agentStateMount{}, err
	}

	// Normalize the managed account-state mount IN-MEMORY (pure — no host-dir
	// creation, no save) so the binds the caller validates — and persists
	// after — match the box's account. The state dir is account-derived
	// (account.StatePath); runtime preflight creates the leaf while holding
	// the space lock. The build itself never creates native state.
	// createAgentbox defers notices until AFTER grant preflight + Save, so a bind
	// that isn't under a live space mount fails cleanly with nothing
	// persisted.
	var stateMount agentStateMount
	if cfg.HasAgent() {
		m, _, err := applyAgentStateMount(cfg)
		if err != nil {
			return nil, agentStateMount{}, err
		}
		stateMount = m
	}
	if _, err := canonicalizeUserMountSources(cfg); err != nil {
		return nil, agentStateMount{}, err
	}
	if err := rejectAccountRootBinds(cfg); err != nil {
		return nil, agentStateMount{}, err
	}
	return cfg, stateMount, nil
}

// claimsPublishedListener reports whether ports already declares p's resolved
// published listener — the (published, protocol, host_ip) tuple Config.Validate
// rejects as a duplicate. Keys on the RESOLVED protocol/host_ip so an omitted
// field and its explicit default count as the same listener.
func claimsPublishedListener(ports []agentbox.Port, p agentbox.Port) bool {
	for _, q := range ports {
		if q.Published == p.Published &&
			q.ResolvedProtocol() == p.ResolvedProtocol() &&
			q.ResolvedHostIP() == p.ResolvedHostIP() {
			return true
		}
	}
	return false
}

// dropCapabilities returns caps with every name in drop removed. Pure;
// order-preserving over the surviving names. A drop name not present is a
// no-op (the validation below decides whether a drop was meaningful).
func dropCapabilities(caps, drop []string) []string {
	if len(drop) == 0 {
		return caps
	}
	out := caps[:0:0] // fresh backing array; never alias caps
	for _, c := range caps {
		if !slices.Contains(drop, c) {
			out = append(out, c)
		}
	}
	return out
}

// validateCapabilityDrops enforces the capability-drop rule: a
// --drop-capability only removes from the box's explicit/default capability
// set; it CANNOT deny a capability that a resolved feature requires. If a
// requested drop would still be re-added by the effective feature set (the
// box's --features unioned with the agent profile's required features), it fails
// with an explanation naming the feature, rather than silently keeping the
// capability. fs.workdir.write is never feature-required, so dropping it (or
// --read-only) always passes.
//
// Pure-ish: reads the feature + agent-profile registries (no VM/network); the
// agent-profile lookup mirrors compose.effectiveFeatures so the validated set
// matches the set the policy composer will actually inherit.
func validateCapabilityDrops(drop, boxFeatures []string, agent string) error {
	if len(drop) == 0 {
		return nil
	}
	effFeatures := effectiveFeatureNames(boxFeatures, agent)
	for _, name := range effFeatures {
		f, err := features.Lookup(name)
		if err != nil {
			return err
		}
		for _, capName := range f.RequiresCapabilities {
			if slices.Contains(drop, capName) {
				return asUserError(fmt.Errorf(
					"cannot drop capability %q: required by feature %q", capName, name))
			}
		}
	}
	return nil
}

// effectiveFeatureNames is the feature set capability inheritance runs over: the
// box's declared features unioned with the selected agent profile's required
// features, de-duplicated and order-stable (box features first). Mirrors
// agentbox/compose.go effectiveFeatures, but at the CLI layer (pre-Config) so
// the drop validation sees the same set the composer will. A missing/edited
// agent profile is non-fatal (its features just don't contribute) — matching
// the composer's tolerance on the read path.
func effectiveFeatureNames(boxFeatures []string, agent string) []string {
	seen := make(map[string]struct{}, len(boxFeatures))
	out := make([]string, 0, len(boxFeatures))
	add := func(f string) {
		if f == "" {
			return
		}
		if _, dup := seen[f]; dup {
			return
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	for _, f := range boxFeatures {
		add(f)
	}
	if hasRealAgent(agent) {
		if p, err := agentprofile.Lookup(agent); err == nil {
			for _, f := range p.RequiresFeatures {
				add(f)
			}
		}
	}
	return out
}

// createAgentbox launches an agentbox container per the supplied
// options by building an agentbox.Config from the flags, saving it
// to ~/.config/yoloctl/spaces/<space>/boxes/<name>.yaml, and routing
// through the reconciler. LiveApplier.Create handles the
// policy stage, nerdctl run, sandbox-socket wait, and port-forward
// apply.
//
// Notice is where credential-discovery messages and similar setup
// output go (typically the command's stderr).
// updated (optional) is set true when the post-create autoupdate actually
// refreshed the agent CLI, so an auto-attach tail can correlate an immediate
// session failure with the update (see shouldWarnPostAutoupdate).
func createAgentbox(ctx context.Context, opts agentboxCreateOptions, notice io.Writer, updated *bool) error {
	c := container.New(opts.Space)

	// Serialize the whole create under the per-agentbox lock and refuse to
	// clobber an existing declaration or container (create-only
	// kubectl-run mapping). The lock is taken up front so two concurrent
	// creates can't both pass the absence check and then race on Save.
	release, err := agentbox.LockConfig(opts.Space, opts.Name)
	if err != nil {
		return fmt.Errorf("acquire reconcile lock: %w", err)
	}
	defer release()
	if err := ensureAgentboxAbsent(ctx, opts.Space, opts.Name); err != nil {
		return err
	}

	if opts.workdir.Mode == agentbox.WorkdirModeEphemeral {
		// Ephemeral /workdir is VM-local. `box destroy` wipes it
		// (no host copy); see the symmetric guard in
		// newAgentboxDestroyCmd. Print a one-time notice so users don't
		// discover this after losing work.
		_, _ = fmt.Fprintf(notice,
			"notice: box %s is --workdir-mode ephemeral; /workdir is VM-local at %s and `box destroy` will wipe it. Use --workdir PATH if the work isn't throwaway.\n",
			opts.Name, opts.workdir.Source)
	}

	// Resolve opts into the desired Config (policy, credentials, devcontainer
	// forwards, template seeding and agent-state normalization).
	cfg, stateMount, err := buildAgentboxConfig(ctx, opts)
	if err != nil {
		return err
	}

	// Manifest-only creation validates the scoped account without VM work.
	if opts.ManifestOnly {
		if gate, ok := ctx.Value(warmGateKey{}).(func() error); ok {
			if err := gate(); err != nil {
				return err
			}
		}
		if err := persistBoxManifestWithAccount(cfg); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(notice,
			"notice: wrote manifest for %s (not applied). Materialize it with: yoloctl box reconcile %s --space %s\n",
			opts.Name, opts.Name, opts.Space)
		if cfg.Privileged {
			_, _ = fmt.Fprintf(notice, "notice: box %s is privileged; only the host can materialize or start it\n", opts.Name)
		}
		return nil
	}
	// Refuse an operator's privileged launch before anything is saved.
	if err := guardOperatorPrivilegedCreate(ctx, cfg); err != nil {
		return err
	}

	// Validate live host grants and prepare guest-native state before saving.
	if err := preflightAgentboxRuntime(ctx, c, cfg, false); err != nil {
		return err
	}

	// Persist under the account lock; the space guard covers runtime setup.
	if err := persistBoxManifestWithAccount(cfg); err != nil {
		return err
	}

	// Persisted with live binds: surface where the agent's identity persists.
	if cfg.HasAgent() {
		if stateMount.Enabled {
			_, _ = fmt.Fprintf(notice,
				"notice: account-state: persisting %s state at %s (account %s)\n",
				cfg.Agent, stateMount.Source, cfg.Account)
		} else {
			_, _ = fmt.Fprintf(notice,
				"notice: account-state: agent %s declares no state path — nothing persists beyond box destroy\n",
				cfg.Agent)
		}
	}
	inspector := agentbox.LiveInspector{Client: c}
	applier := newManagedLiveApplier(c, notice)
	if _, err := agentbox.Reconcile(ctx, cfg, inspector, applier); err != nil {
		return err
	}

	if !opts.NoAutoupdate {
		if err := autoupdateAgentboxIfNeeded(ctx, c, opts.Name, opts.Agent, notice, updated); err != nil {
			return fmt.Errorf("autoupdate %s: %w", opts.Name, err)
		}
	}

	return nil
}

// runtimePreparation is computed read-only before image warming or native
// state preparation. Normalized binds are published only by locked apply.
type runtimePreparation struct {
	stateMount agentStateMount
	normalized bool
}

func validateAgentboxRuntime(ctx context.Context, c *container.Client, cfg *agentbox.Config, persisted bool) (runtimePreparation, error) {
	switch cfg.Workdir.Mode {
	case agentbox.WorkdirModeHost, agentbox.WorkdirModeEphemeral:
	default:
		return runtimePreparation{}, asUserError(fmt.Errorf("box %s has no workdir; pass --workdir or --workdir-mode ephemeral", cfg.Name))
	}
	// A privileged launch needs the VM's static endpoint binary; check it
	// before preparing anything for the box.
	if cfg.Privileged {
		if _, err := agentbox.InspectEndpointBinaryFn(ctx, c); err != nil {
			return runtimePreparation{}, asUserError(fmt.Errorf("privileged box %s: %w", cfg.Name, err))
		}
	}
	if persisted {
		if err := requireAccountForLifecycle(cfg); err != nil {
			return runtimePreparation{}, err
		}
	} else if err := requireAccountForCreate(cfg.Space, cfg.Account); err != nil {
		return runtimePreparation{}, err
	}
	stateMount, stateMountChanged, err := applyAgentStateMount(cfg)
	if err != nil {
		return runtimePreparation{}, err
	}
	userMountChanged, err := canonicalizeUserMountSources(cfg)
	if err != nil {
		return runtimePreparation{}, err
	}
	// Grant-authority guard BEFORE the liveness probes: an ordinary bind may
	// never intersect the managed account-state root.
	if err := rejectAccountRootBinds(cfg); err != nil {
		return runtimePreparation{}, err
	}
	// Validate every bind against the space's live mounts — never mutate them.
	spacecfg, err := effectiveSpaceConfig(cfg.Space)
	if errors.Is(err, fs.ErrNotExist) {
		return runtimePreparation{}, asUserError(fmt.Errorf(
			"space %q is not configured — create it first: yoloctl space create %s --workdir PATH --account NAME",
			cfg.Space, cfg.Space))
	}
	if err != nil {
		return runtimePreparation{}, err
	}
	// Native account binds do not enter the host mount grant/probe path.
	for _, hm := range agentboxHostMounts(cfg) {
		if err := ensureBindGranted(ctx, cfg.Space, spacecfg, hm.Path, hm.Writable, bindLabel(cfg, hm.Path)); err != nil {
			return runtimePreparation{}, err
		}
	}
	return runtimePreparation{stateMount: stateMount, normalized: persisted && (stateMountChanged || userMountChanged)}, nil
}

// preflightAgentboxRuntime validates account selection and host grants,
// normalizes managed binds, and prepares VM-native state and session paths.
// The space operation guard excludes destructive lifecycle and account
// mutations through runtime setup. It never widens a space mount grant.
func preflightAgentboxRuntime(ctx context.Context, c *container.Client, cfg *agentbox.Config, persisted bool) error {
	return preflightAgentboxRuntimeQualified(defaultCreationContext(ctx, cfg), c, cfg, persisted, func(check context.Context, declared *agentbox.Config) (bool, error) {
		return qualifyDefaultNativeCreation(check, c, declared)
	})
}

// Root native qualification runs AFTER image warming opens operation admission
// and pure runtime validation, BEFORE config/account/socket/workdir writes.
// A native result leaves protected SSH directory preparation to the SDK.
func preflightAgentboxRuntimeQualified(ctx context.Context, c *container.Client, cfg *agentbox.Config, persisted bool, qualify func(context.Context, *agentbox.Config) (bool, error)) error {
	if err := warmBoxBeforeEffects(ctx, c, cfg, persisted); err != nil {
		return err
	}
	prep, err := validateAgentboxRuntime(ctx, c, cfg, persisted)
	if err != nil {
		return err
	}
	return finishAgentboxRuntimePreparation(ctx, c, cfg, prep, qualify)
}

// The native prerequisite is the first step after pure validation. Actual
// callers supply the verifier, never request data or decoded authority flags.
func finishAgentboxRuntimePreparation(ctx context.Context, c *container.Client, cfg *agentbox.Config, prep runtimePreparation, qualify func(context.Context, *agentbox.Config) (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	native := false
	if qualify != nil {
		native, err = qualify(ctx, cfg)
		if err != nil {
			return err
		}
	}
	if prep.normalized {
		if err := cfg.Save(); err != nil {
			return fmt.Errorf("persist normalized mounts: %w", err)
		}
	}

	if prep.stateMount.Enabled {
		if _, err := accountStateOperation(ctx, cfg.Space, accountstate.Request{Action: "mkdir", Account: cfg.Account, Agent: cfg.Agent}); err != nil {
			return err
		}
	}
	// Sandbox socket dir must exist before nerdctl run binds it.
	if !native {
		if err := c.Shell(ctx, "sudo", "mkdir", "-p", sandbox.SocketDirHost(cfg.Name)); err != nil {
			return err
		}
	}
	if cfg.Workdir.Mode == agentbox.WorkdirModeEphemeral {
		if err := prepareEphemeralWorkdir(ctx, c, cfg.Workdir.Source); err != nil {
			return err
		}
	}
	return nil
}

// bindLabel describes a bind source for the grant-check error: the workdir
// gets a "workdir" label, anything else "mount".
func bindLabel(cfg *agentbox.Config, path string) string {
	if cfg.Workdir.IsHost() && path == cfg.Workdir.MountSource() {
		return "workdir"
	}
	return "mount"
}

// rejectAccountRootBinds rejects ordinary host binds that could expose
// managed config or native state. Only the derived account/agent bind is legal.
func rejectAccountRootBinds(cfg *agentbox.Config) error {
	scfg, err := spaceconfig.Load(cfg.Space)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	check := func(kind, path string) error {
		if path == "" {
			return nil
		}
		canonical, _, err := agentbox.CanonicalizeWorkdir(path)
		if err != nil {
			return fmt.Errorf("canonicalize %s %s: %w", kind, path, err)
		}
		return rejectSpaceHostPath(canonical, scfg)
	}
	if cfg.Workdir.IsHost() {
		if err := check("workdir", cfg.Workdir.MountSource()); err != nil {
			return err
		}
	}
	managedDest := managedStateDest(cfg)
	for _, m := range cfg.Mounts {
		if managedDest != "" && m.Target == managedDest {
			continue // the managed slot: normalized to the box's own leaf
		}
		if err := check("mount source", m.Source); err != nil {
			return err
		}
	}
	return nil
}

// requireResolvableManifest fails, before a changed manifest is saved, when cfg
// cannot be compiled: a capability or feature no catalog defines, a feature
// requiring one, an agent definition that is gone, or a composed policy the
// lints reject. Config.Validate checks names only syntactically (catalog
// lookups are I/O), so without this guard a typo is persisted and only the
// reconcile that follows fails, leaving a manifest nothing can apply. It resolves
// new OCI Feature locks, then runs the same offline policy/image planning as
// Reconcile. Installers still run only when an image is built. Invalid selections
// are reported as user errors before any declaration is saved.
func requireResolvableManifest(ctx context.Context, cfg *agentbox.Config) error {
	if err := cfg.ResolveFeatures(ctx); err != nil {
		return asUserError(err)
	}
	if _, err := agentbox.ComposePolicy(cfg); err != nil {
		return asUserError(err)
	}
	if _, _, err := agentbox.ResolvedImage(cfg); err != nil {
		return asUserError(err)
	}
	return validateAgentSessionPolicy(cfg)
}

// pathsIntersect reports whether cleaned paths a and b are equal or one
// contains the other.
func pathsIntersect(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(a, strings.TrimSuffix(b, sep)+sep) || strings.HasPrefix(b, strings.TrimSuffix(a, sep)+sep)
}

// prepareEphemeralWorkdir creates the VM-local /workdir dir for an
// ephemeral agentbox and chowns it to the host UID/GID. Chown is to the
// NUMERIC host uid/gid (this process's), not the name `sandbox`: the
// entrypoint shim remaps the container's sandbox user to
// YOLOCTL_HOST_UID/GID at start, so chowning to the VM's `sandbox`
// (UID 2000) would leave /workdir unwritable.
func prepareEphemeralWorkdir(ctx context.Context, c *container.Client, dir string) error {
	if err := c.Shell(ctx, "sudo", "mkdir", "-p", dir); err != nil {
		return err
	}
	uid, gid := spaceenv.Identity()
	owner := fmt.Sprintf("%d:%d", uid, gid)
	return c.Shell(ctx, "sudo", "chown", owner, dir)
}

// ensureAgentboxAbsent enforces create-only semantics (kubectl-run
// mapping): it errors if an agentbox with this name already has a
// declaration (YAML) OR a live/stopped container in the space. Callers
// hold LockConfig(space, name) so the check and the subsequent Save are
// atomic against a concurrent create.
func ensureAgentboxAbsent(ctx context.Context, space, name string) error {
	resolved, err := newBoxStateResolver().ResolveOne(ctx, space, name, boxstate.RuntimeSummary)
	if err != nil {
		return err
	}
	if resolved.Declaration == boxstate.DeclarationDeclared || resolved.Declaration == boxstate.DeclarationInvalid {
		path, perr := layout.BoxPath(space, name)
		if perr != nil {
			return perr
		}
		return asUserError(fmt.Errorf(
			"box %q already exists in space %q (declared at %s) — create never overwrites an existing box.\n"+
				"  attach to its agent session:    yoloctl box attach %s\n"+
				"  reconcile its manifest:         yoloctl box reconcile %s\n"+
				"  use another name, or remove it: yoloctl box destroy %s",
			name, space, path, name, name, name))
	}
	if resolved.Runtime == boxstate.RuntimeRunning ||
		resolved.Runtime == boxstate.RuntimeStopped ||
		resolved.Runtime == boxstate.RuntimeOrphaned {
		return asUserError(fmt.Errorf(
			"box %q already exists in space %q (runtime: %s) — create never reuses an existing box.\n"+
				"  attach to its agent session:    yoloctl box attach %s\n"+
				"  create it under a different name\n"+
				"  or remove it first:             yoloctl box destroy %s",
			name, space, resolved.Runtime, name, name))
	}
	return nil
}

// startExistingAgentboxContainer brings an already-created agentbox
// container back up via `nerdctl start`. Three steps that the create
// path runs as a unit but `nerdctl start` does not:
//
//  1. Recreate the sandbox SSH socket directory on the VM. The
//     source lives at /run/yoloctl/<name>/ (tmpfs) and evaporates
//     whenever the VM stops, so runc's bind-mount would otherwise
//     fail with "no such file or directory".
//  2. `nerdctl start` the container.
//  3. Wait for openshell-sandbox to accept connections and answer with its
//     SSH greeting. A socket file may remain from the previous container
//     run; its existence alone does not mean the new supervisor is ready.
//
// Shared between `box attach`'s revive path (ensureAgentSession)
// and the `yoloctl box start` command — both Start callers that
// may run after a VM restart need the same guards. The privileged launch
// guard re-reads the container here, so an operator request that saw a
// running box cannot start it after it exits on its own.
func startExistingAgentboxContainer(ctx context.Context, c *container.Client, cfg *agentbox.Config) error {
	return startExistingAgentboxContainerPrepared(ctx, c, cfg, nil)
}
func startExistingAgentboxContainerPrepared(ctx context.Context, c *container.Client, cfg *agentbox.Config, prepared recordedNativeStart) error {
	details, err := c.Inspect(ctx, cfg.Name)
	if err != nil {
		return err
	}
	if err := guardPrivilegedLaunch(ctx, cfg, launchStart, details, nil); err != nil {
		return err
	}
	if err := agentbox.VerifyChatRuntime(cfg, details); err != nil {
		return err
	}
	native := prepared != nil
	if prepared == nil {
		prepared, native, err = prepareNativeExistingStart(ctx, c, cfg, true)
		if err != nil {
			return err
		}
		defer func() { closeNativeStart(prepared) }()
	} else if err := prepared.Check(ctx, cfg); err != nil {
		return err
	}
	if native {
		if err := withdrawNativeStartForwards(ctx, c, cfg.Name); err != nil {
			return err
		}
	}
	if err := agentbox.PrepareChatRuntime(cfg); err != nil {
		return err
	}
	// A VM reboot empties /run: remount the capped root a bound container
	// expects before it starts, never an uncapped directory in its place.
	if err := agentbox.PrepareObservationRuntime(cfg, details); err != nil {
		return err
	}
	name := cfg.Name
	if err := cleanupStoppedSessionState(ctx, c, name); err != nil {
		return fmt.Errorf("clear stale session state for %s before start: %w", name, err)
	}
	if native {
		return prepared.Start(ctx, cfg)
	}
	if err := container.ValidateContainerID(details.ID); err != nil {
		return err
	}
	if err := c.Shell(ctx, "sudo", "mkdir", "-p", sandbox.SocketDirHost(name)); err != nil {
		return fmt.Errorf("recreate sandbox socket dir for %s before start: %w", name, err)
	}
	if err := c.Start(ctx, details.ID); err != nil {
		return err
	}
	return agentbox.WaitForSandboxSocket(ctx, c.Env(), name, agentbox.SandboxSocketTimeout)
}

// warnPostAutoupdateFailure nudges the user when the agent exits with an
// error immediately after this run autoupdated its CLI — the failure
// mode where a newer agent version rejects on-disk state an older one
// wrote (e.g. codex 0.138 strict-parsing a rules ledger 0.134
// tolerated). Advisory only; the nonzero exit is still returned.
func warnPostAutoupdateFailure(w io.Writer, agentboxName, agent string, exitCode int) {
	_, _ = fmt.Fprintf(w,
		"\nnotice: the agent in %s exited (status %d) right after an autoupdate. "+
			"A newer agent version can reject on-disk state written by an older "+
			"one; if it failed to start, recreate the box (keeps workdir "+
			"+ account state) or clear its persisted state:\n"+
			"  yoloctl box recreate %s\n",
		agentboxName, exitCode, agentboxName)
	if agent != "" {
		_, _ = fmt.Fprintf(w,
			"  yoloctl box inspect %s   # shows the account-derived state dir to inspect/clear\n", agentboxName)
	}
}

func newAgentboxStopCmd() *cobra.Command {
	var spaceName string
	cmd := &cobra.Command{
		Use:   "stop [BOX]",
		Short: "Gracefully stop a box (lets openshell-sandbox tear down its netns)",
		Long: `Sends SIGTERM so openshell-sandbox can clean up its network
namespace + veth pair before exit, then reaps any netns/veth an
earlier ungraceful teardown left behind.

  yoloctl box stop coder
  yoloctl box stop coder --space staging`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			name := args[0]
			if err := validateAgentboxName(name); err != nil {
				return err
			}
			return stopAgentboxRuntime(cmd.Context(), container.New(spaceName), spaceName, name, cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	return cmd
}

var (
	lifecycleStopHolderUnit             = session.StopHolderUnit
	lifecycleUnforwardAgentbox          = port.UnforwardAgentbox
	lifecycleStopAgentboxContainer      = stopManagedAgentboxContainer
	lifecycleReapOrphanedNetns          = sandbox.ReapOrphanedNetns
	lifecycleCleanupStoppedSessionState = cleanupStoppedSessionState
)

// stopAgentboxRuntime gracefully tears down an agentbox's runtime: it stops the
// VM-side session holder (clients see a clean disconnect; per spike F1 the
// holder never touches the agent — the container stop is what terminates it via
// SIGTERM to PID 1, openshell-sandbox), removes the box's port forwards (the
// bridge IP becomes unreachable once the container is gone), stops the
// container gracefully, clears now-stale session holder markers, and reaps any
// netns/veth a SIGKILL-escalated stop orphaned. Shared by `box stop` and
// `box restart` so the session-aware teardown stays identical. Only a genuine
// container-stop or port-cleanup failure is returned; holder / session-state
// / reap failures retain their existing warning behavior.
func stopAgentboxRuntime(ctx context.Context, c *container.Client, spaceName, name string, errw io.Writer) error {
	return stopAgentboxRuntimeParts(ctx, c, spaceName, name, errw, true, true)
}

// Inventory-backed space teardown skips stopping absent/stopped containers and
// reaps once after all boxes, while retaining holder, port and artifact cleanup.
func stopAgentboxRuntimeParts(ctx context.Context, c *container.Client, spaceName, name string, errw io.Writer, stopContainer, reap bool) error {
	return stopAgentboxRuntimePartsObserved(ctx, c, spaceName, name, errw, stopContainer, reap, false)
}

// runtimeAbsent is supplied only by the complete space runtime inventory, never
// inferred from a failed Inspect. Such a missing row still needs its retained
// holder, forwards and session-state cleanup.
func stopAgentboxRuntimePartsObserved(ctx context.Context, c *container.Client, spaceName, name string, errw io.Writer, stopContainer, reap, runtimeAbsent bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !runtimeAbsent {
		if native, err := lifecycleTryNativeStop(ctx, c, name, stopContainer); native || err != nil {
			return err
		}
	}
	if err := lifecycleStopHolderUnit(ctx, c, name); err != nil {
		_, _ = fmt.Fprintf(errw, "warning: session holder cleanup for %s: %v\n", name, err)
	}
	if err := lifecycleUnforwardAgentbox(ctx, c, name); err != nil {
		return fmt.Errorf("port forward cleanup for %s: %w", name, err)
	}
	if stopContainer {
		if err := lifecycleStopAgentboxContainer(ctx, c, name); err != nil {
			if isContainerGone(err) {
				if cleanErr := lifecycleCleanupStoppedSessionState(ctx, c, name); cleanErr != nil {
					_, _ = fmt.Fprintf(errw, "warning: session state cleanup for %s: %v\n", name, cleanErr)
				}
			}
			return err
		}
	}
	if err := lifecycleCleanupStoppedSessionState(ctx, c, name); err != nil {
		_, _ = fmt.Fprintf(errw, "warning: session state cleanup for %s: %v\n", name, err)
	}
	if reap {
		if err := lifecycleReapOrphanedNetns(ctx, spaceName, errw); err != nil {
			_, _ = fmt.Fprintf(errw, "warning: leaked netns cleanup for %s: %v\n", name, err)
		}
	}
	return nil
}

// Preflight while the original session and container still exist. The native
// space operation lock excludes other managed mutations; startAgentbox obtains
// its own box lock and repeats the preservation check after this lock is released.
func preflightAgentboxRestartWithIntent(ctx context.Context, c *container.Client, space, name string) (*agentbox.Config, error) {
	release, err := agentbox.LockConfig(space, name)
	if err != nil {
		return nil, fmt.Errorf("acquire reconcile lock: %w", err)
	}
	defer release()
	cfg, err := loadManagedAgentbox(space, name)
	if err != nil {
		return nil, err
	}
	// Refuse recreate-only drift before preflight's image work; the check
	// after preflight stays as the race guard.
	if err := refuseRecreateBeforeImageWork(ctx, space, cfg); err != nil {
		return nil, err
	}
	if err := preflightAgentboxRuntimeQualified(ctx, c, cfg, true, func(check context.Context, declared *agentbox.Config) (bool, error) {
		return qualifyNativeExistingStart(check, c, declared)
	}); err != nil {
		return nil, err
	}

	if err := agentbox.CheckStart(ctx, cfg, agentbox.LiveInspector{Client: c}); err != nil {
		return nil, err
	}
	return copyStartForwardIntent(cfg), nil
}

// newAgentboxRestartCmd reuses session-aware stop+start after preflight. Private
// container files survive; recreate-only drift requires explicit replacement.
func newAgentboxRestartCmd() *cobra.Command {
	var (
		spaceName     string
		noAutoupdate  bool
		noAttach      bool
		newSession    bool
		staleHolder   string
		startWorkload bool
	)
	cmd := &cobra.Command{
		Use:   "restart [BOX]",
		Short: "Restart a box (stop its runtime, then start it again)",
		Long: `Stops the box gracefully (SIGTERM to openshell-sandbox so it tears
down its network namespace, stopping the VM-side session holder first) and then
starts it again from its declaration — reconciling port forwards against the
fresh bridge IP. The box manifest and its runtime declaration are
preserved; this is the same machinery as ` + "`box stop` followed by `box start`" + `,
not a recreate (use ` + "`box recreate`" + ` to rebuild the container).
Changes requiring recreation are refused before stopping the existing box.

After the restart the agent CLI is autoupdated to registry-latest
(best-effort); pass --no-autoupdate to keep the version baked into the image.

  yoloctl box restart coder
  yoloctl box restart coder --space prod-untrusted`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			name := args[0]
			if err := validateAgentboxName(name); err != nil {
				return err
			}
			// Stop the runtime via the shared session-aware teardown, then
			// bring it back up via the shared start core. A "no such
			// container" on stop is benign for a restart (a configured box
			// with no runtime just materializes on start), so stop errors are
			// reported as a warning rather than aborting the start.
			c := container.New(spaceName)
			// The post-update offer's restart stops only the listed old-build
			// session, checked before anything else happens.
			if staleHolder != "" {
				if err := requireStaleSession(cmd.Context(), c, name, staleHolder); err != nil {
					return err
				}
			}
			if startWorkload {
				if err := requireRestartableWorkload(spaceName, name); err != nil {
					return err
				}
			}
			if err := prepareAutoAttach(cmd, spaceName, name, noAttach, true); err != nil {
				return err
			}
			// An operator restarts a privileged box only while it is a running
			// privileged launch; its context authorizes the start leg alone.
			restartCtx, err := authorizePrivilegedRestart(cmd.Context(), c, spaceName, name)
			if err != nil {
				return err
			}
			forwardIntent, err := preflightAgentboxRestartWithIntent(cmd.Context(), c, spaceName, name)
			if err != nil {
				return err
			}
			if err := stopAgentboxRuntime(cmd.Context(), c, spaceName, name, cmd.ErrOrStderr()); err != nil {
				if !isContainerGone(err) {
					return err
				}
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"notice: box %s has no running container to stop; starting it from its declaration\n", name)
			}
			var agentUpdated bool
			previous := cmd.Context()
			cmd.SetContext(restartCtx)
			err = startAgentboxWithForwardIntent(cmd, spaceName, name, noAutoupdate, noAttach, newSession, &agentUpdated, forwardIntent)
			cmd.SetContext(previous)
			if err != nil {
				return err
			}
			if startWorkload {
				return startRestartedWorkload(cmd.Context(), c, spaceName, name, cmd.ErrOrStderr())
			}
			return maybeAutoAttach(cmd, spaceName, name, noAttach, noAutoupdate, agentUpdated)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().BoolVar(&noAutoupdate, "no-autoupdate", false, "Skip the post-restart npm install -g <pkg>@latest that brings the agent CLI to current registry-latest. Use for air-gapped environments or when you want the version baked into the image.")
	addPassthroughFlag(cmd)
	cmd.Flags().BoolVar(&noAttach, "no-attach", false, "Skip the automatic attach to the box's session when the command finishes")
	cmd.Flags().BoolVar(&newSession, newSessionFlag, false, "Replace the agent conversation: after the stop, the next session starts a new one instead of resuming it")
	cmd.Flags().StringVar(&staleHolder, staleSessionFlag, "", "Restart only while the box runs this holder incarnation and it answers as another build (used after a controller update)")
	_ = cmd.Flags().MarkHidden(staleSessionFlag)
	cmd.Flags().BoolVar(&startWorkload, startWorkloadFlag, false, "After the restart, start the box's declared session_command workload again without attaching (used after a controller update)")
	_ = cmd.Flags().MarkHidden(startWorkloadFlag)
	return cmd
}

// isContainerGone reports whether a nerdctl error means the container
// doesn't exist (already removed / never created), so destroy can treat
// it as benign and continue cleaning up the declaration + VM dirs.
func isContainerGone(err error) bool {
	if err == nil {
		return true
	}
	if workerEffectsUncertain(err) {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") || strings.Contains(msg, "no such object") || strings.Contains(msg, "not found")
}

// removeAgentboxConfig deletes the per-box conversation record, then the
// YAML declaration. The record must go first and its failure stops here:
// ordinary boxes have no immutable ID, so a later box of the same name,
// account and agent would otherwise inherit the conversation. The kept
// declaration lets a repeated destroy finish. A manifest removal failure is
// a warning (the runtime teardown already succeeded); a missing file is fine.
func removeAgentboxConfig(space, name string, notice io.Writer) error {
	if err := removeSessionRecordFn(space, name); err != nil {
		return fmt.Errorf("box %s runtime was removed, but its conversation record could not be removed (%w); its declaration is kept. Inspect the cause, then run yoloctl box destroy %s --space %s again to finish", name, err, name, space)
	}
	if cfg, err := agentbox.Load(space, name); err == nil {
		if err := cfg.Delete(); err != nil && !errors.Is(err, fs.ErrNotExist) {
			_, _ = fmt.Fprintf(notice, "warning: removing box manifest %s: %v\n", name, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		// Couldn't load (parse/migration error) — try a direct unlink.
		if path, perr := layout.BoxPath(space, name); perr == nil {
			if rerr := manifestio.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
				_, _ = fmt.Fprintf(notice, "warning: removing box manifest %s: %v\n", name, rerr)
			}
		}
	}
	return nil
}

// removeSessionRecordFn is a seam for record-removal failure tests.
var removeSessionRecordFn = agentsession.Remove

// newAgentboxRecreateCmd tears down an agentbox's live container and
// brings it back up against the same persisted YAML. The agentbox
// config file, workdir bind source, agent-state bind source, and
// per-agentbox credentials stay untouched. Useful for:
//
//   - Recovering from a broken container state without losing the
//     YAML you've been editing.
//
// Does NOT rebuild the base or composed image: an existing composed tag is
// reused even after `yoloctl image refresh`. `box reconcile --rebuild`
// rebuilds it on the refreshed base and recreates the box. Autoupdate runs as part of the post-create path
// (same as `box create` / `box start`), so the agent CLI
// ends up at registry-latest unless --no-autoupdate is passed.
//
// Confirmation prompt by default — recreate is destructive enough
// (drops in-container changes outside bind-mounted paths) that a
// stray Tab-completion shouldn't silently nuke a running workload.
// --force skips the prompt.
func newAgentboxRecreateCmd() *cobra.Command {
	var (
		spaceName    string
		force        bool
		noAutoupdate bool
		noAttach     bool
	)
	cmd := &cobra.Command{
		Use:   "recreate [BOX]",
		Short: "Tear down and rebuild a box container from its persisted YAML",
		Long: `Stops the box, removes the container, then re-creates it from
its persisted declaration on the space data disk.
Preserved across recreate:

  - The box YAML itself.
  - /workdir (host workdir or VM-local box dir).
  - The selected account's agent-owned native state (the account-state bind source).

Lost across recreate: anything the agent wrote inside the container
outside the bind-mounted paths above (notably ` + "`/tmp` and `/home/sandbox/`" + `
subdirs that aren't mounted from the host).

By default the post-create autoupdate brings the agent CLI to
registry-latest. Pass --no-autoupdate to keep the version baked into
the image.

  yoloctl box recreate coder
  yoloctl box recreate coder --no-autoupdate
  yoloctl box recreate coder --force          # skip confirm

Recreate reuses the box's existing image. To rebuild it on a refreshed
base, run ` + "`yoloctl image refresh`" + ` and then
` + "`yoloctl box reconcile BOX --rebuild`" + ` instead.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := validateAgentboxName(name); err != nil {
				return err
			}
			var agentUpdated bool
			if err := recreateAgentbox(cmd, spaceName, name, force, noAutoupdate, noAttach, &agentUpdated); err != nil {
				return err
			}
			return maybeAutoAttach(cmd, spaceName, name, noAttach, noAutoupdate, agentUpdated)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Skip the confirmation prompt")
	cmd.Flags().BoolVar(&noAutoupdate, "no-autoupdate", false, "Skip the post-create npm install -g <pkg>@latest that brings the agent CLI to current registry-latest")
	addPassthroughFlag(cmd)
	cmd.Flags().BoolVar(&noAttach, "no-attach", false, "Skip the automatic attach to the box's session when the command finishes")
	return cmd
}

// recreateAgentbox performs `box recreate`'s destructive work — confirm,
// preflight, tear down + rebuild, autoupdate — under the per-box config
// lock, which is RELEASED when this returns: the caller's auto-attach can
// hold the terminal for hours, and holding the lock across it would block
// every reconcile/edit/set on the box. An unconfirmed recreate (declined, or
// no answer) changes nothing and is a user error, like the other confirmed
// commands; updated (optional) reports whether the post-create autoupdate
// actually refreshed the agent CLI.
func recreateAgentbox(cmd *cobra.Command, spaceName, name string, force, noAutoupdate, noAttach bool, updated *bool) error {
	lock, err := lockSessionBoxFn(spaceName, name)
	if err != nil {
		return fmt.Errorf("acquire reconcile lock: %w", err)
	}
	defer lock.Release()

	cfg, err := agentbox.Load(spaceName, name)
	if err != nil {
		return fmt.Errorf("load box manifest: %w", err)
	}

	errw := cmd.ErrOrStderr()
	state, _ := cmd.Context().Value(warmKey{}).(*imageWarmState)
	if !force && (state == nil || !state.recreateApproved) {
		proceed, err := promptYesNo(fmt.Sprintf(
			"this will stop+remove box %q and re-create it from %s.\n"+
				"workdir and account state are preserved; "+
				"any in-container state outside bind mounts is lost.\n"+
				"continue? [y/N]: ",
			name, cfg.Path()), cmd.InOrStdin(), errw)
		if err != nil {
			return err
		}
		if !proceed {
			return asUserError(errors.New("recreate not confirmed; nothing changed (answer y at the prompt, or rerun with --force)"))
		}
		if state != nil {
			state.recreateApproved = true
		}
	}

	// Environment injection is container-create-time. Recreate resolves
	// the selected account again; native state stays untouched in its
	// bind-mounted account leaf.

	c := container.New(spaceName)
	policy, err := agentbox.ComposePolicy(cfg)
	if err != nil {
		return fmt.Errorf("compose policy: %w", err)
	}
	if err := prepareAutoAttach(cmd, spaceName, name, noAttach, true); err != nil {
		return err
	}
	// Same VM-side preflight every create path runs — BEFORE the
	// teardown inside Recreate, so a workdir/agent-state mount that
	// isn't live yet bails with the restart hint instead of leaving
	// the box torn down.
	if err := preflightAgentboxRuntime(cmd.Context(), c, cfg, true); err != nil {
		return err
	}
	tracking, _, err := sessionTrackingFor(cfg)
	if err != nil {
		return err
	}
	applier := newManagedLiveApplier(c, errw)
	if err := applier.Recreate(cmd.Context(), cfg, policy); err != nil {
		return fmt.Errorf("recreate %s: %w", name, err)
	}
	// The old container, its holder and its agent are gone: a launched or
	// interrupted resume closes; the conversation stays current.
	if err := applySessionStart(lock, tracking, true, false, errw); err != nil {
		return err
	}

	if !noAutoupdate {
		if err := autoupdateAgentboxIfNeeded(cmd.Context(), c, name, cfg.Agent, errw, updated); err != nil {
			return fmt.Errorf("autoupdate %s: %w", name, err)
		}
	}
	return nil
}

func newAgentboxExecCmd() *cobra.Command {
	var spaceName string
	cmd := &cobra.Command{
		Use:   "exec [flags] BOX -- COMMAND [ARGS...]",
		Short: "Run a command inside a box via the sandbox SSH socket (kubectl-exec mapping)",
		Long: "Connects to openshell-sandbox's embedded SSH server over a Unix socket " +
			"bridged out of the space's VM, then runs COMMAND — with a PTY when stdin " +
			"and stdout are both terminals, so `yoloctl box exec BOX -- bash` gives an " +
			"interactive shell while `box attach` joins the agent session. Captured or " +
			"piped output (`out=$(yoloctl box exec BOX -- CMD)`) gets no PTY, so it " +
			"keeps its exact bytes and a separate stderr. " +
			"Uses system `ssh` with this binary as ProxyCommand. Requires `ssh` on PATH.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			agentbox := args[0]
			cmdArgs := args[1:]
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			if err := validateAgentboxName(agentbox); err != nil {
				return err
			}
			return asPassthroughExitError(execInAgentboxInteractive(cmd.Context(), spaceName, agentbox, cmdArgs))
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	return cmd
}

// newAgentboxListCmd builds the `box list` command (alias `ls`).
func newAgentboxListCmd() *cobra.Command {
	var (
		spaceName     string
		allSpaces     bool
		workdirFilter string
		runningOnly   bool
		runtimeOnly   bool
		stateFilter   string
		asJSON        bool
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List declared boxes and their runtime state",
		Long: `Lists boxes from their declaration-first state view. By default this
shows every declared box in the selected space, including configured boxes
whose container is missing and unavailable boxes whose space runtime is stopped
or absent. Runtime orphans are included when the space VM can be queried.

--workdir PATH filters to boxes bound to that host workdir. --running shows
running boxes only; --runtime shows boxes
with runtime materialized in the VM, including orphans; --state filters by the
display state.

  yoloctl box list
  yoloctl box list --workdir ~/src/myrepo
  yoloctl box list --running
  yoloctl box list --state unavailable
  yoloctl box list --all-spaces
  yoloctl box list --json --all-spaces   # machine-readable output`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if allSpaces && flagChanged(cmd, "space") {
				return asUserError(fmt.Errorf("--space and --all-spaces are mutually exclusive"))
			}
			// Canonicalize the --workdir filter once (host workdirs are
			// compared by canonical path).
			var wantWorkdir string
			if workdirFilter != "" {
				expanded, err := expandUserPath(workdirFilter)
				if err != nil {
					return asUserError(err)
				}
				canonical, _, err := agentbox.CanonicalizeWorkdir(expanded)
				if err != nil {
					return asUserError(err)
				}
				wantWorkdir = canonical
			}
			var boxes []boxstate.Box
			var err error
			if !allSpaces {
				if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
					return err
				}
				boxes, err = newBoxStateResolver().ResolveSpace(cmd.Context(), spaceName, boxstate.RuntimeSummary)
			} else {
				boxes, err = newBoxStateResolver().ResolveAll(cmd.Context(), boxstate.RuntimeSummary)
			}
			if err != nil {
				return err
			}
			if wantWorkdir != "" {
				boxes = filterBoxStatesByWorkdir(boxes, wantWorkdir)
			}
			if runningOnly {
				boxes = filterBoxStatesRunning(boxes)
			}
			if runtimeOnly {
				boxes = filterBoxStatesRuntime(boxes)
			}
			if stateFilter != "" {
				boxes = filterBoxStatesByState(boxes, stateFilter)
			}
			if asJSON {
				return writeBoxStatesJSON(cmd.OutOrStdout(), boxes)
			}
			return printBoxStates(cmd.OutOrStdout(), boxes, allSpaces)
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space to query (incompatible with --all-spaces)")
	cmd.Flags().BoolVar(&allSpaces, "all-spaces", false, "Walk every configured space and merge the declaration-first state view")
	cmd.Flags().StringVar(&workdirFilter, "workdir", "", "Only list boxes bound to this host workdir")
	cmd.Flags().BoolVar(&runningOnly, "running", false, "Only list boxes whose runtime container is running")
	cmd.Flags().BoolVar(&runtimeOnly, "runtime", false, "Only list boxes with VM runtime state, including orphans")
	cmd.Flags().StringVar(&stateFilter, "state", "", "Only list boxes with this display state")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of the table view")
	return cmd
}

func boxStateWorkdirField(b boxstate.Box) string {
	if b.Config == nil {
		return "-"
	}
	if b.Config.Workdir.IsHost() {
		return b.Config.Workdir.MountSource()
	}
	if b.Config.Workdir.Mode == agentbox.WorkdirModeEphemeral {
		return "ephemeral"
	}
	return "-"
}

func boxStateAgentField(b boxstate.Box) string {
	if b.Config != nil && b.Config.Agent != "" {
		return b.Config.Agent
	}
	if b.Container != nil {
		labels := parseLabels(b.Container.Labels)
		if agent := labels[agentbox.LabelAgent]; agent != "" {
			return agent
		}
	}
	return "-"
}

func boxStateImageField(b boxstate.Box) string {
	if b.Config != nil && b.Config.Image != "" {
		return displayImageRef(b.Config.Image)
	}
	if b.Container != nil {
		return displayImageRef(b.Container.Image)
	}
	return "-"
}

func boxStateSessionField(b boxstate.Box) string {
	switch b.Session.State {
	case boxstate.SessionRunning:
		return "running"
	case boxstate.SessionExited:
		return fmt.Sprintf("exited(%d)", b.Session.ExitCode)
	case boxstate.SessionLost:
		return "lost"
	case boxstate.SessionNone:
		return "none"
	default:
		return "-"
	}
}

func boxStateReasonField(b boxstate.Box) string {
	return boxstate.ReasonSummary(b.Reasons)
}

func filterBoxStatesByWorkdir(boxes []boxstate.Box, canonical string) []boxstate.Box {
	out := make([]boxstate.Box, 0, len(boxes))
	for _, b := range boxes {
		if b.Config != nil && b.Config.Workdir.IsHost() && b.Config.Workdir.MountSource() == canonical {
			out = append(out, b)
		}
	}
	return out
}

func filterBoxStatesRunning(boxes []boxstate.Box) []boxstate.Box {
	out := make([]boxstate.Box, 0, len(boxes))
	for _, b := range boxes {
		if b.Runtime == boxstate.RuntimeRunning {
			out = append(out, b)
		}
	}
	return out
}

func filterBoxStatesRuntime(boxes []boxstate.Box) []boxstate.Box {
	out := make([]boxstate.Box, 0, len(boxes))
	for _, b := range boxes {
		switch b.Runtime {
		case boxstate.RuntimeRunning, boxstate.RuntimeStopped, boxstate.RuntimeOrphaned:
			out = append(out, b)
		}
	}
	return out
}

func filterBoxStatesByState(boxes []boxstate.Box, state string) []boxstate.Box {
	out := make([]boxstate.Box, 0, len(boxes))
	for _, b := range boxes {
		if string(b.State) == state {
			out = append(out, b)
		}
	}
	return out
}

func printBoxStates(w io.Writer, boxes []boxstate.Box, includeSpace bool) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if includeSpace {
		_, _ = fmt.Fprintln(tw, "SPACE\tNAME\tSTATE\tAGENT\tWORKDIR\tSESSION\tIMAGE\tREASON")
		for _, b := range boxes {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				b.Space, b.Name, b.State, boxStateAgentField(b), boxStateWorkdirField(b),
				boxStateSessionField(b), boxStateImageField(b), boxStateReasonField(b))
		}
	} else {
		_, _ = fmt.Fprintln(tw, "NAME\tSTATE\tAGENT\tWORKDIR\tSESSION\tIMAGE\tREASON")
		for _, b := range boxes {
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				b.Name, b.State, boxStateAgentField(b), boxStateWorkdirField(b),
				boxStateSessionField(b), boxStateImageField(b), boxStateReasonField(b))
		}
	}
	return tw.Flush()
}

type boxStateReasonJSON struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type boxStateDimensionsJSON struct {
	Declaration string `json:"declaration"`
	Runtime     string `json:"runtime"`
	Session     string `json:"session"`
	Drift       string `json:"drift"`
}

type boxStateRowJSON struct {
	Space         string                 `json:"space"`
	Name          string                 `json:"name"`
	State         string                 `json:"state"`
	Dimensions    boxStateDimensionsJSON `json:"dimensions"`
	Agent         string                 `json:"agent,omitempty"`
	Workdir       string                 `json:"workdir,omitempty"`
	WorkdirMode   string                 `json:"workdir_mode,omitempty"`
	Image         string                 `json:"image,omitempty"`
	RuntimeStatus string                 `json:"runtime_status,omitempty"`
	Reasons       []boxStateReasonJSON   `json:"reasons,omitempty"`
	Conversation  *agentsession.Status   `json:"conversation,omitempty"`
}

func boxStateJSONRow(b boxstate.Box) boxStateRowJSON {
	row := boxStateRowJSON{
		Space: b.Space,
		Name:  b.Name,
		State: string(b.State),
		Dimensions: boxStateDimensionsJSON{
			Declaration: string(b.Declaration),
			Runtime:     string(b.Runtime),
			Session:     string(b.Session.State),
			Drift:       string(b.Drift),
		},
		Agent:         emptyDash(boxStateAgentField(b)),
		Workdir:       emptyDash(boxStateWorkdirField(b)),
		Image:         emptyDash(boxStateImageField(b)),
		RuntimeStatus: b.RuntimeText,
		Conversation:  b.Conversation,
	}
	if b.Config != nil {
		row.WorkdirMode = string(b.Config.Workdir.Mode)
	}
	for _, r := range b.Reasons {
		row.Reasons = append(row.Reasons, boxStateReasonJSON{Code: string(r.Code), Message: r.Message})
	}
	return row
}

func writeBoxStateJSON(w io.Writer, b boxstate.Box) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(boxStateJSONRow(b))
}

func writeBoxStatesJSON(w io.Writer, boxes []boxstate.Box) error {
	out := make([]boxStateRowJSON, 0, len(boxes))
	for _, b := range boxes {
		out = append(out, boxStateJSONRow(b))
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func emptyDash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

// agentboxRow is a single-space-tagged container result, used by the
// --all-spaces aggregation. Keeps the per-space context so the table /
// JSON view can show which space each row belongs to.
type agentboxRow struct {
	Space     string
	Container container.Container
}

// listAgentboxesAcrossSpaces queries `nerdctl ps` inside every Running
// Lima instance and returns the union, tagged with the source space.
// Best-effort: per-space failures (VM unreachable, containerd down)
// emit a stderr warning and continue, so a flaky one space doesn't
// hide agentboxes in another.
func listAgentboxesAcrossSpaces(ctx context.Context, stderr io.Writer) ([]agentboxRow, error) {
	instances, err := vmhost.New().List(ctx)
	if err != nil {
		return nil, err
	}
	var rows []agentboxRow
	for _, inst := range instances {
		if inst.Status != "Running" {
			continue
		}
		containers, err := container.New(inst.Name).List(ctx, agentbox.LabelAgentbox)
		if err != nil {
			_, _ = fmt.Fprintf(stderr,
				"warning: skipping space %s (list failed): %v\n", inst.Name, err)
			continue
		}
		for _, c := range containers {
			rows = append(rows, agentboxRow{Space: inst.Name, Container: c})
		}
	}
	return rows, nil
}

func qualifyNativeExistingStart(ctx context.Context, c *container.Client, cfg *agentbox.Config) (bool, error) {
	prepared, native, err := prepareNativeExistingStart(ctx, c, cfg, true)
	defer func() { closeNativeStart(prepared) }()
	return native, err
}

// Existing running native services remain observable after controller/helper
// updates without demanding a new launch artifact. Qualify only a stopped
// launch; both native cases skip unsafe legacy socket-directory preparation.
func qualifyNativeRuntimePreparation(ctx context.Context, c *container.Client, cfg *agentbox.Config, prepared *recordedNativeStart) (bool, error) {
	known, err := c.List(ctx, agentbox.LabelAgentbox+"="+cfg.Name)
	if err != nil {
		return false, err
	}
	if len(known) == 0 {
		return false, nil
	}
	details, err := c.Inspect(ctx, cfg.Name)
	if err != nil {
		return false, err
	}
	native, err := agentbox.NativeRuntimeRoute(details)
	if err != nil {
		return native, err
	}
	if !native {
		if !details.Running {
			return false, agentbox.CheckRetainedLegacyStart(ctx, details, c.CheckRetainedLegacyOpenShell)
		}
		return false, nil
	}
	if !details.Running {
		var err error
		*prepared, native, err = prepareNativeExistingStart(ctx, c, cfg, true)
		if err == nil && native {
			err = withdrawNativeStartForwards(ctx, c, cfg.Name)
		}
		return native, err
	}
	if err := agentbox.CheckStart(ctx, cfg, agentbox.LiveInspector{Client: c}); err != nil {
		return true, err
	}
	return true, nil
}
