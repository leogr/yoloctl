package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/leogr/yoloctl/internal/agentsession"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/spf13/cobra"
)

// Session states of the box session view. Unknown is an undecidable probe,
// never shown as not-running.
const (
	boxSessionRunning    = "running"
	boxSessionNotRunning = "not-running"
	boxSessionLost       = "lost"
	boxSessionUnknown    = "unknown"
)

// boxSessionView is box session --json. Conversation is the same
// agentsession.Status that box show and box list carry.
type boxSessionView struct {
	SchemaVersion int                  `json:"schema_version"`
	Space         string               `json:"space"`
	Box           string               `json:"box"`
	Session       boxSessionState      `json:"session"`
	Conversation  *agentsession.Status `json:"conversation,omitempty"`
}

type boxSessionState struct {
	// State is running, not-running, lost (marker without holder) or unknown.
	State string `json:"state"`
	// Tracked reports whether a running holder belongs to the recorded launch.
	Tracked bool   `json:"tracked"`
	Error   string `json:"error,omitempty"`
}

func newAgentboxSessionCmd() *cobra.Command {
	var (
		spaceName string
		asJSON    bool
	)
	cmd := &cobra.Command{
		Use:   "session BOX",
		Short: "Show a box's agent conversation record (read-only)",
		Long: `Shows the controller-owned conversation record of a box: its policy, the
assigned conversation ID and how it was assigned (chosen, learned or
switched), the conversation the agent last reported when it differs (the next
start normally resumes it), whether the agent reports every in-app switch, the latest
launch and resume request, agent-reported observations and the conversations
it replaced. It reads only that record, the
box declaration and the session holder's state. It never reads transcripts or
account state, and it changes nothing: no resume, launch or record update.

A holder probe that cannot decide shows the session as unknown, never as not
running. Untracked, manual, unsupported and stale states are explained by a
note. A missing declaration or an unreadable record is an error.

For Claude, a later managed start checks for the saved conversation file before
resuming. Confirmed absence selects a new ID and records the replacement reason;
unavailable storage refuses the start. This view displays that recorded decision
without checking or reading the saved files.

  yoloctl box session coder
  yoloctl box session coder --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := resolveSpaceFlag(cmd, &spaceName); err != nil {
				return err
			}
			name := args[0]
			if err := validateAgentboxName(name); err != nil {
				return err
			}
			view, err := readBoxSession(cmd.Context(), container.New(spaceName), spaceName, name)
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(view); err != nil {
					return err
				}
			} else {
				writeBoxSession(cmd.OutOrStdout(), view)
			}
			if c := view.Conversation; c != nil && strings.HasPrefix(c.Note, "record unavailable:") {
				return asUserError(fmt.Errorf("conversation record of box %s is unavailable: %s", name, strings.TrimPrefix(c.Note, "record unavailable: ")))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&spaceName, "space", defaultSpaceName, "Space hosting the box")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the view as JSON")
	return cmd
}

// readBoxSession resolves the declaration, probes the holder with errors
// kept and describes the record. It takes no lock and writes nothing.
func readBoxSession(ctx context.Context, c *container.Client, space, name string) (*boxSessionView, error) {
	resolved, err := newBoxStateResolver().ResolveOne(ctx, space, name, boxstate.RuntimeInventory)
	if err != nil {
		return nil, err
	}
	switch resolved.Declaration {
	case boxstate.DeclarationMissing:
		return nil, asUserError(fmt.Errorf("no box %q in space %q", name, space))
	case boxstate.DeclarationInvalid:
		return nil, asUserError(fmt.Errorf("box %q declaration is invalid: %s", name, resolved.ConfigError))
	}
	view := &boxSessionView{SchemaVersion: 1, Space: space, Box: name, Session: boxSessionState{State: boxSessionNotRunning}}
	token := ""
	switch resolved.Runtime {
	case boxstate.RuntimeRunning:
		view.Session, token = probeBoxSession(ctx, c, name)
	case boxstate.RuntimeUnavailable, boxstate.RuntimeUnknown:
		view.Session = boxSessionState{State: boxSessionUnknown, Error: "box runtime is " + string(resolved.Runtime)}
	}
	if resolved.Config == nil || !resolved.Config.HasAgent() {
		return view, nil
	}
	profile, err := lookupAgentProfile(resolved.Config.Agent)
	if err != nil {
		return nil, err
	}
	// A running holder whose marker could not be read is neither tracked nor
	// untracked: its session error says so instead.
	known := view.Session.State == boxSessionRunning && view.Session.Error == ""
	view.Conversation = agentsession.Describe(resolved.Config, profile, known, token)
	view.Session.Tracked = view.Conversation.LiveLaunch()
	return view, nil
}

// probeBoxSession classifies the holder of a running box with the
// error-preserving probes and returns its marker's launch token.
func probeBoxSession(ctx context.Context, c *container.Client, name string) (boxSessionState, string) {
	active, err := holderUnitStateFn(ctx, c, name)
	if err != nil {
		return boxSessionState{State: boxSessionUnknown, Error: err.Error()}, ""
	}
	m, present, err := probeVMMarkerFn(ctx, c, name)
	switch {
	case err != nil && active:
		return boxSessionState{State: boxSessionRunning, Error: err.Error()}, ""
	case err != nil:
		return boxSessionState{State: boxSessionUnknown, Error: err.Error()}, ""
	case active:
		return boxSessionState{State: boxSessionRunning}, m.LaunchToken
	case present:
		return boxSessionState{State: boxSessionLost, Error: "the holder left a marker but no longer runs"}, ""
	}
	return boxSessionState{State: boxSessionNotRunning}, ""
}

func writeBoxSession(w io.Writer, v *boxSessionView) {
	_, _ = fmt.Fprintf(w, "Box:          %s (space %s)\n", v.Box, v.Space)
	session := v.Session.State
	switch {
	case v.Session.State == boxSessionRunning && v.Session.Tracked:
		session += " (tracked launch)"
	case v.Session.State == boxSessionRunning && v.Session.Error == "" && v.Conversation != nil && v.Conversation.Tracked:
		session += " (untracked)"
	}
	if v.Session.Error != "" {
		session += " — " + v.Session.Error
	}
	_, _ = fmt.Fprintf(w, "Session:      %s\n", session)
	c := v.Conversation
	if c == nil {
		_, _ = fmt.Fprintln(w, "Conversation: none (agentless box)")
		return
	}
	_, _ = fmt.Fprintf(w, "Agent:        %s\n", c.Provider)
	_, _ = fmt.Fprintf(w, "Policy:       %s\n", c.Policy)
	switch {
	case c.ID != "" && c.AssignedAt != nil:
		_, _ = fmt.Fprintf(w, "Conversation: %s  assigned %s (%s)\n", c.ID, c.AssignedAt.UTC().Format(time.RFC3339), c.AssignedSource)
	case c.Tracked && c.LearnsID():
		_, _ = fmt.Fprintln(w, "Conversation: none yet (learned from the agent's first report)")
	case c.Tracked:
		_, _ = fmt.Fprintln(w, "Conversation: none yet (the next session assigns one)")
	}
	if cur := c.Current; cur != nil && c.Switched() {
		_, _ = fmt.Fprintf(w, "Current:      %s · last observed %s at %s · next start resumes it\n", cur.ID, sourceLabel(cur.Source), cur.At.UTC().Format(time.RFC3339))
	}
	switch {
	case !c.Tracked:
	case c.Selection == agentsession.SelectionReported:
		_, _ = fmt.Fprintln(w, "Selection:    reported by the agent")
	default:
		_, _ = fmt.Fprintf(w, "Selection:    not reported by %s: an in-app switch to an already loaded conversation is not visible to yoloctl\n", c.Provider)
	}
	if l := c.Launch; l != nil {
		parts := []string{fmt.Sprintf("generation %d", l.Generation), l.Kind, l.State}
		if l.Holder != "" {
			parts = append(parts, "holder "+l.Holder)
		}
		if l.Operation != "" {
			parts = append(parts, "operation "+l.Operation)
		}
		line := strings.Join(parts, " · ")
		if l.Detail != "" {
			line += " (" + l.Detail + ")"
		}
		_, _ = fmt.Fprintf(w, "Launch:       %s\n", line)
	}
	if r := c.Resume; r != nil {
		line := fmt.Sprintf("requested %s · generation %d · %s", r.Requested, r.Generation, r.Outcome)
		if r.Detail != "" {
			line += " (" + r.Detail + ")"
		}
		_, _ = fmt.Fprintf(w, "Resume:       %s\n", line)
	}
	if o := c.Observed; o != nil && (o.ID != "" || o.Health != "") {
		for i, line := range o.Lines() {
			lead := "Observed:    "
			if i > 0 {
				lead = "             "
			}
			_, _ = fmt.Fprintf(w, "%s %s\n", lead, line)
		}
	}
	for i, p := range c.Previous {
		lead := "Previous:    "
		if i > 0 {
			lead = "             "
		}
		_, _ = fmt.Fprintf(w, "%s %s  %s  %s\n", lead, p.ID, p.EndedAt.UTC().Format(time.RFC3339), p.Text())
	}
	if c.Note != "" {
		_, _ = fmt.Fprintf(w, "Note:         %s\n", c.Note)
	}
}
