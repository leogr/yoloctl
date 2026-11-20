package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/logging"
	"github.com/leogr/yoloctl/internal/managedchat"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/pkg/prettylog"
	"github.com/spf13/cobra"
)

func newSpaceUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade [SPACE]",
		Short: "Update a space to this build and review box changes",
		Long: `Selects this yoloctl build for the space controller and restarts
eligible sessions from the previous build. Reviews the changes needed by the
space's running boxes and asks before replacing any container. Replacement
discards files stored only in that container. Stopped boxes stay stopped, with
a follow-up command for known drift. Explicit declines are warnings. Actions
that could not be decided or completed return a nonzero exit.

An earlier-build OpenShell helper is a reason to offer recreation.

First, upgrade checks whether the space's VM matches this build: on
Firecracker, whether it uses this build's default VM image; on Lima, whether
its container stack uses this build's pins. Other Lima template changes are
not detectable and are reported as such. When the VM does not match, upgrade
shows why and asks before running space recreate (with --update-vm-image on
Firecracker); the box review then continues on the new VM.`,
		Args: cobra.MaximumNArgs(1),
		RunE: hostControllerLifecycle("upgrade"),
	}
}

type spaceUpgradeAction string

const (
	upgradeKeep     spaceUpgradeAction = ""
	upgradeManual   spaceUpgradeAction = "manual action"
	upgradeRestart  spaceUpgradeAction = "restart"
	upgradeRecreate spaceUpgradeAction = "recreate"
)

type spaceUpgradeRow struct {
	box               boxstate.Box
	drift             boxDriftView
	action            spaceUpgradeAction
	reason            string
	command           string
	confirm           bool
	legacyStop        bool
	restartIfDeclined bool
	kind              sessionKind
	restore           bool
	room              string
}

type spaceUpgradeDecision struct {
	row    spaceUpgradeRow
	action spaceUpgradeAction
}

type spaceUpgradeConsent int

const (
	upgradeUnanswered spaceUpgradeConsent = iota
	upgradeDeclined
	upgradeAccepted
)

type spaceUpgradeOps struct {
	sessionRestartOffer
	room     func(context.Context) (managedchat.Status, error)
	recreate func(context.Context, spaceUpgradeRow) error
	ensure   func(context.Context, string) error
	// vm is the space VM recreation decided before the controller update.
	vm spaceUpgradeVMResult
}

func finishRepairedSpaceUpgrade(cmd *cobra.Command, space string) error {
	boot, err := checkControllerSpace(cmd.Context(), space)
	if err != nil {
		return err
	}
	if err := checkCommandVMCompatibility(cmd.Context(), boot); err != nil {
		return err
	}
	return finishSpaceUpgrade(cmd, boot, spaceUpgradeVMResult{})
}

func finishSpaceUpgrade(cmd *cobra.Command, boot *spaceconfig.Bootstrap, vm spaceUpgradeVMResult) error {
	cliNotice(logging.Host, "Space %s has selected controller build %s. Checking its boxes.", boot.Name, boot.Bundle)
	offer := hostSessionRestartOffer(cmd, boot)
	offer.drift = func(ctx context.Context, names []string) ([]boxDriftView, error) {
		var view []boxDriftView
		err := controllerJSON(ctx, boot, boxDriftMethod, names, map[string][]string{upgradeDriftFlag: {"true"}}, &view, cmd.ErrOrStderr())
		return view, err
	}
	ops := spaceUpgradeOps{
		sessionRestartOffer: offer,
		room: func(ctx context.Context) (managedchat.Status, error) {
			var status managedchat.Status
			err := controllerJSON(ctx, boot, "chat.status", nil, nil, &status, cmd.ErrOrStderr())
			return status, err
		},
		recreate: func(ctx context.Context, row spaceUpgradeRow) error {
			req, err := upgradeRecreateRequest(boot.Name, row)
			if err != nil {
				return err
			}
			signals := make(chan int, 4)
			stopSignals := forwardCommandSignals(ctx, signals)
			defer stopSignals()
			return callHostController(ctx, boot, req, controller.ClientIO{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr(), Signals: signals})
		},
		ensure: func(ctx context.Context, room string) error {
			return ensureControllerChat(ctx, boot, cmd.ErrOrStderr(), room)
		},
		vm: vm,
	}
	err := runSpaceUpgrade(cmd.Context(), boot.Name, ops)
	warnHostDisk(cmd.Context(), boot)
	return err
}

func readSpaceUpgradePlan(ctx context.Context, space string, ops spaceUpgradeOps) ([]spaceUpgradeRow, error) {
	boxes, err := ops.inventory(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(boxes))
	seen := make(map[string]bool, len(boxes))
	for _, b := range boxes {
		if seen[b.Name] || validateAgentboxName(b.Name) != nil {
			return nil, errors.New("box inventory contains an invalid or duplicate name")
		}
		seen[b.Name] = true
		if b.Declaration == boxstate.DeclarationDeclared {
			names = append(names, b.Name)
		}
	}
	room, roomErr := ops.room(ctx)
	drift := make(map[string]boxDriftView, len(names))
	for batch := range slices.Chunk(names, maxDriftBoxes) {
		for _, d := range readUpgradeDrift(ctx, ops.drift, batch) {
			drift[d.Name] = d
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	rows := make([]spaceUpgradeRow, 0, len(boxes))
	for _, b := range boxes {
		rows = append(rows, classifySpaceUpgradeBox(space, b, drift[b.Name], room, roomErr))
	}
	slices.SortFunc(rows, func(a, b spaceUpgradeRow) int {
		if (a.kind == chatServerSession) != (b.kind == chatServerSession) {
			if a.kind == chatServerSession {
				return 1
			}
			return -1
		}
		return strings.Compare(a.box.Name, b.box.Name)
	})
	return rows, nil
}

func readUpgradeDrift(ctx context.Context, read func(context.Context, []string) ([]boxDriftView, error), names []string) []boxDriftView {
	view, err := read(ctx, names)
	if err == nil && len(view) == len(names) {
		for i, d := range view {
			if d.Name != names[i] {
				err = errors.New("unmatched box drift reply")
				break
			}
		}
		if err == nil {
			return view
		}
	}
	if err == nil {
		err = errors.New("incomplete box drift reply")
	}
	result := make([]boxDriftView, len(names))
	for i, name := range names {
		result[i] = boxDriftView{Name: name, Error: err.Error()}
	}
	return result
}

func classifySpaceUpgradeBox(space string, b boxstate.Box, d boxDriftView, status managedchat.Status, roomErr error) spaceUpgradeRow {
	r := spaceUpgradeRow{box: b, drift: d}
	inspect := "yoloctl box inspect " + b.Name + " --space " + space
	unavailable := func(reason string) spaceUpgradeRow {
		r.action, r.reason, r.command = upgradeManual, reason, inspect
		return r
	}
	if b.Declaration != boxstate.DeclarationDeclared || b.Config == nil {
		return unavailable(boxStateReasonField(b))
	}
	if d.Error != "" {
		return unavailable("drift evidence unavailable: " + d.Error)
	}
	if b.Runtime == boxstate.RuntimeMissing && d.Drift == boxstate.DriftNeedsCreate {
		r.reason = fmt.Sprintf("no existing container; start explicitly with yoloctl box start %s --space %s if wanted", b.Name, space)
		return r
	}
	if b.Runtime != boxstate.RuntimeRunning && b.Runtime != boxstate.RuntimeStopped || d.Upgrade == nil ||
		d.Upgrade.Running != (b.Runtime == boxstate.RuntimeRunning) || b.Container == nil ||
		b.Container.ID == "" || !strings.HasPrefix(d.Upgrade.Run.ID, b.Container.ID) {
		return unavailable("container identity or runtime state unavailable")
	}
	if d.Drift != boxstate.DriftInSync && d.Drift != boxstate.DriftNeedsRestart &&
		d.Drift != boxstate.DriftLiveOnly && d.Drift != boxstate.DriftNeedsRecreate {
		return unavailable("drift evidence unavailable: " + d.DriftError)
	}

	launcherRecreate := slices.ContainsFunc(d.Reasons, func(reason boxstate.Reason) bool {
		return reason.Code == boxstate.ReasonLauncherRecreate
	})
	needsRecreate := d.LegacyRuntime || d.Drift == boxstate.DriftNeedsRecreate || launcherRecreate
	helperReason := upgradeDriftReason(d, boxstate.ReasonHelperRecreate)
	if b.Runtime == boxstate.RuntimeStopped {
		switch {
		case needsRecreate:
			r.reason = "recreation deferred until you want to use this box again"
			if helperReason != "" {
				r.reason = helperReason + "; " + r.reason
			}
			r.command = upgradeRecreateCommand(space, b.Name)
		case d.Drift == boxstate.DriftNeedsRestart || d.Drift == boxstate.DriftLiveOnly:
			r.reason = "saved changes apply on next start"
			r.command = fmt.Sprintf("yoloctl box start %s --space %s", b.Name, space)
		default:
			r.reason = "no box change needed"
		}
		return r
	}
	workload := len(b.Config.SessionCommand) > 0
	chatCommand := slices.Equal(b.Config.SessionCommand, agentbox.ChatServerCommand())
	switch {
	case status.Room != nil && status.Room.Server.Name == b.Name:
		if status.Room.ID == "" || status.Room.Server.ID == "" || status.Room.Server.ID != b.Config.ID {
			return unavailable("managed chat server identity unavailable")
		}
		r.kind, r.room = chatServerSession, status.Room.ID
		r.restore = status.ServerReady || b.Session.State == boxstate.SessionRunning
	case workload:
		if roomErr != nil || chatCommand {
			return unavailable("managed workload identity unavailable")
		}
		r.kind, r.restore = workloadSession, b.Session.State == boxstate.SessionRunning
	}
	stale := b.Session.State == boxstate.SessionRunning && b.Session.Build == boxstate.SessionBuildOther
	stale = stale && b.Session.Holder != ""
	switch {
	case d.LegacyRuntime && stale:
		r.action, r.reason, r.legacyStop = upgradeRestart, "stop pre-native run; recreation requires separate consent", true
		r.command = upgradeRecreateCommand(space, b.Name)
	case needsRecreate:
		r.action, r.confirm = upgradeRecreate, true
		r.restartIfDeclined = stale && launcherRecreate && !d.LegacyRuntime && d.Drift != boxstate.DriftNeedsRecreate
		r.reason = helperReason
		if r.reason == "" {
			r.reason = upgradeDriftReason(d, boxstate.ReasonDriftNeedsRecreate, boxstate.ReasonLauncherRecreate)
		}
		if r.reason == "" {
			r.reason = "existing container requires explicit replacement"
		}
		r.command = upgradeRecreateCommand(space, b.Name)
	case stale:
		r.action, r.reason = upgradeRestart, "session uses the previous build"
		r.command = sessionRestartCommand(space, b.Name)
	case b.Session.State == boxstate.SessionRunning && b.Session.Build != boxstate.SessionBuildCurrent:
		return unavailable("could not identify the running session")
	case d.Drift == boxstate.DriftNeedsRestart:
		r.action, r.confirm = upgradeRestart, true
		r.reason = upgradeDriftReason(d, boxstate.ReasonDriftNeedsRestart)
		r.command = sessionRestartCommand(space, b.Name)
	case d.Drift == boxstate.DriftLiveOnly:
		r.action, r.reason = upgradeManual, "port forwards differ"
		r.command = "yoloctl box reconcile " + b.Name + " --space " + space
	default:
		r.reason = "no box change needed"
	}
	return r
}

func upgradeDriftReason(d boxDriftView, codes ...boxstate.ReasonCode) string {
	for _, r := range d.Reasons {
		if r.Message != "" && slices.Contains(codes, r.Code) {
			return r.Message
		}
	}
	return ""
}

func upgradeRecreateCommand(space, name string) string {
	return fmt.Sprintf("yoloctl box recreate %s --space %s --no-attach", name, space)
}

func runSpaceUpgrade(ctx context.Context, space string, ops spaceUpgradeOps) error {
	rows, err := readSpaceUpgradePlan(ctx, space, ops)
	if err != nil {
		return fmt.Errorf("controller updated, but box planning failed: %w", err)
	}
	for _, r := range rows {
		reportSpaceUpgradeRow(r)
	}

	var declined, outstanding []string
	defer func() {
		if len(ops.vm.declined) != 0 {
			cliLogLines(prettylog.LevelWarn, logging.Host, "Space VM left as you chose", ops.vm.declined)
		}
		if len(ops.vm.outstanding) != 0 {
			cliLogLines(prettylog.LevelWarn, logging.Host, "Outstanding space action", ops.vm.outstanding)
		}
		if len(declined) != 0 {
			cliLogLines(prettylog.LevelWarn, logging.Host, "Boxes left as you chose", declined)
		}
		if len(outstanding) != 0 {
			cliLogLines(prettylog.LevelWarn, logging.Host, "Outstanding box actions", outstanding)
		}
	}()
	approved := make([]spaceUpgradeDecision, 0, len(rows))
	input := bufio.NewReader(ops.in)
	for _, r := range rows {
		action := r.action
		switch {
		case action == upgradeKeep:
			continue
		case action == upgradeManual:
			outstanding = append(outstanding, r.outstanding(r.reason))
			continue
		case r.confirm:
			consent, reason := upgradeUnanswered, "needs terminal consent"
			if ops.interactive {
				var err error
				consent, err = confirmSpaceUpgrade(ctx, input, ops.errw, r)
				if err != nil {
					return fmt.Errorf("controller updated before consent. No box changes started: %w", err)
				}
				reason = "no yes or no answer received"
			}
			if consent != upgradeAccepted {
				if consent == upgradeDeclined {
					reason = "declined"
				}
				if r.restartIfDeclined {
					reason += ". The upgrade still restarts its stale session in the existing container if it matches the plan"
				}
				if consent == upgradeDeclined {
					declined = append(declined, r.outstanding(reason))
				} else {
					outstanding = append(outstanding, r.outstanding(reason))
				}
				if !r.restartIfDeclined {
					continue
				}
				action = upgradeRestart
			}
		}
		approved = append(approved, spaceUpgradeDecision{r, action})
	}

	completed := 0
	for i, decision := range approved {
		r, action := decision.row, decision.action
		err := ctx.Err()
		if err == nil {
			err = recheckSpaceUpgradeRow(ctx, space, ops, r)
		}
		if err != nil {
			if ctx.Err() != nil {
				return spaceUpgradeFailure(approved[i:], ctx.Err(), completed)
			}
			r.command = fmt.Sprintf("yoloctl box inspect %s --space %s", r.box.Name, space)
			outstanding = append(outstanding, r.outstanding(err.Error()))
			continue
		}
		if action == upgradeRestart {
			holder := ""
			if r.box.Session.Build == boxstate.SessionBuildOther {
				holder = r.box.Session.Holder
			}
			err = ops.restart(ctx, r.box.Name, holder, r.restore && r.kind == workloadSession)
		} else {
			err = ops.recreate(ctx, r)
		}
		if err != nil {
			var exit *controller.ExitError
			legacyRefused := action == upgradeRestart && errors.As(err, &exit) && exit.State == "" && exit.Remedy == remedyLegacyRecreate && ctx.Err() == nil
			reportSpaceUpgradeRecovery(space, r, legacyRefused)
			if legacyRefused {
				outstanding = append(outstanding, fmt.Sprintf("%s (pre-native runtime stopped, separate recreation consent required): %s", r.box.Name, upgradeRecreateCommand(space, r.box.Name)))
				continue
			}
			return spaceUpgradeFailure(approved[i:], err, completed)
		}
		if r.restore && r.kind == chatServerSession {
			if err := ops.ensure(ctx, r.room); err != nil {
				return spaceUpgradeFailure(approved[i:], fmt.Errorf("restore chat server (recover with yoloctl chat ensure --space %s): %w", space, err), completed)
			}
			cliNotice(logging.Host, "Chat server ready. Reconnect chat clients and re-arm their delivery consumers.")
		}
		completed++
		if r.kind == agentSession {
			if note := conversationNote(r.box); note != "" {
				cliNotice(logging.Host, "Box %s: %s", r.box.Name, note)
			}
		}
	}
	if len(outstanding) != 0 || len(ops.vm.outstanding) != 0 {
		return asUserError(fmt.Errorf("controller updated; %d box actions completed, %d outstanding", completed, len(outstanding)+len(ops.vm.outstanding)))
	}
	if len(ops.vm.declined) != 0 && len(declined) == 0 {
		cliNotice(logging.Host, "Space upgraded. %d box actions completed. The VM recreate was left as you chose.", completed)
		return nil
	}
	if len(declined) != 0 {
		cliNotice(logging.Host, "Space upgraded. %d box actions completed. %s left as you chose.", completed, boxCount(len(declined)))
	} else {
		cliNotice(logging.Host, "Controller updated and %d box actions completed.", completed)
	}
	return nil
}

func (r spaceUpgradeRow) outstanding(reason string) string {
	return fmt.Sprintf("%s (%s): %s", r.box.Name, reason, r.command)
}

func reportSpaceUpgradeRecovery(space string, row spaceUpgradeRow, recreateFirst bool) {
	if !row.restore || row.kind == agentSession {
		return
	}
	session := staleSession{name: row.box.Name, kind: row.kind}
	recovery := []string{session.recovery(space)}
	if recreateFirst {
		recovery = append([]string{upgradeRecreateCommand(space, row.box.Name)}, recovery...)
	}
	affected := "The managed workload of box " + row.box.Name
	if row.kind == chatServerSession {
		affected = "The chat room served by box " + row.box.Name
	}
	cliLogLines(prettylog.LevelWarn, logging.Host, affected+" may be down. Recover it with the following commands.", recovery)
}

func reportSpaceUpgradeRow(r spaceUpgradeRow) {
	action := string(r.action)
	if action == "" {
		action = "leave unchanged"
	}
	details := spaceUpgradeEffects(r)
	if r.action == upgradeKeep && r.command != "" {
		details = append(details, r.command)
	}
	cliLogLines(prettylog.LevelNotice, logging.Host, fmt.Sprintf("Box %s (%s): %s. %s", r.box.Name, r.box.Runtime, action, r.reason), details)
}

func spaceUpgradeEffects(r spaceUpgradeRow) []string {
	var details []string
	switch r.action {
	case upgradeRecreate:
		details = append(details, "Recreation ends any current session and replaces the container. Container-only files are lost. Workdir, account state and separate cache are kept.")
	case upgradeRestart:
		details = append(details, "Restart ends the current session.")
	}
	if r.restartIfDeclined {
		details = append(details, "If you decline replacement, the upgrade will still restart the stale session in its existing container if it still matches the plan. The replacement remains deferred.")
	}
	if r.drift.Upgrade != nil && (r.action == upgradeRestart || r.action == upgradeRecreate) {
		desired := agentbox.RuntimeModeConfined
		if r.box.Config.Privileged {
			desired = agentbox.RuntimeModePrivileged
		}
		if desired == agentbox.RuntimeModePrivileged || r.drift.Upgrade.Mode == agentbox.RuntimeModePrivileged {
			details = append(details, fmt.Sprintf("Current mode %s, saved mode %s. A privileged launch runs as space-VM root.", r.drift.Upgrade.Mode, desired))
		}
	}
	if (r.action == upgradeRestart || r.action == upgradeRecreate) && r.kind == chatServerSession {
		details = append(details, "This is the managed chat server. Connected clients must reconnect and re-arm their delivery consumers.")
	}
	return details
}

func confirmSpaceUpgrade(ctx context.Context, input *bufio.Reader, out io.Writer, r spaceUpgradeRow) (spaceUpgradeConsent, error) {
	if err := ctx.Err(); err != nil {
		return upgradeUnanswered, err
	}
	resume := suspendCLISteps()
	defer resume()
	if reason := upgradeDriftReason(r.drift, boxstate.ReasonHelperRecreate); reason != "" {
		if _, err := fmt.Fprintln(out, reason); err != nil {
			return upgradeUnanswered, err
		}
	}
	for _, line := range spaceUpgradeEffects(r) {
		if _, err := fmt.Fprintln(out, line); err != nil {
			return upgradeUnanswered, err
		}
	}
	if _, err := fmt.Fprintf(out, "%s box %s? [y/N]: ", r.action, r.box.Name); err != nil {
		return upgradeUnanswered, err
	}
	line, err := input.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return upgradeUnanswered, ctx.Err()
		}
		return upgradeUnanswered, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return upgradeAccepted, ctx.Err()
	case "", "n", "no":
		return upgradeDeclined, ctx.Err()
	default:
		return upgradeUnanswered, ctx.Err()
	}
}

func spaceUpgradeFailure(remaining []spaceUpgradeDecision, err error, completed int) error {
	names := make([]string, 0, len(remaining)-1)
	for _, r := range remaining[1:] {
		names = append(names, r.row.box.Name)
	}
	if len(names) > 0 {
		cliLogLines(prettylog.LevelWarn, logging.Host, "Box actions not attempted", names)
	}
	return fmt.Errorf("controller updated; %d box actions completed before %s: %w", completed, remaining[0].row.box.Name, err)
}

func recheckSpaceUpgradeRow(ctx context.Context, space string, ops spaceUpgradeOps, planned spaceUpgradeRow) error {
	boxes, err := ops.inventory(ctx)
	if err != nil {
		return err
	}
	for _, b := range boxes {
		if b.Name != planned.box.Name {
			continue
		}
		room, roomErr := ops.room(ctx)
		d := readUpgradeDrift(ctx, ops.drift, []string{b.Name})[0]
		fresh := classifySpaceUpgradeBox(space, b, d, room, roomErr)
		if fresh.action == planned.action && fresh.reason == planned.reason && fresh.confirm == planned.confirm &&
			fresh.kind == planned.kind && fresh.restore == planned.restore && fresh.room == planned.room &&
			fresh.legacyStop == planned.legacyStop && fresh.restartIfDeclined == planned.restartIfDeclined && d.LegacyRuntime == planned.drift.LegacyRuntime && d.Drift == planned.drift.Drift &&
			fresh.box.Session.Holder == planned.box.Session.Holder && fresh.box.Session.State == planned.box.Session.State &&
			fresh.box.Session.Build == planned.box.Session.Build && d.Upgrade != nil && *d.Upgrade == *planned.drift.Upgrade {
			return nil
		}
		return errors.New("box state or action changed since the plan")
	}
	return errors.New("box left the inventory")
}
