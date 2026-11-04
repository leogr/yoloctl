package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/boxendpoint"
	"github.com/leogr/yoloctl/internal/container"
)

// Privileged boxes (privileged: true) are started by the host. A box operator
// otherwise manages them like any box: it reads, declares, edits, copies, stops
// and destroys them, and execs in, attaches to and uses a running one, gaining
// its space-VM root authority. Only a launch is withheld: an operator request
// never creates a privileged container or starts a stopped one, explicitly or
// through attach, use, reconcile, set, edit or chat. Restarting or recreating a
// box that the same request proved to be a running privileged launch on the
// current endpoint protocol continues that host-started runtime. Desired and
// actual privilege both count and uncertain evidence counts as privileged.
// Each seam that creates or starts a container calls guardPrivilegedLaunch
// after its last runtime observation and before its own teardown, image build
// or start; host launches print the operator-access warning there. A command
// may do authorized non-launch work first, such as an explicit rebuild's image
// build.

// privilegedFlagHelp describes --privileged on box create, copy and set.
const privilegedFlagHelp = "Select a privileged development box: a privileged container with full VM devices and module loading, served by yoloctl's own endpoint instead of openshell-sandbox, where the sandbox user has sudo. " +
	agentbox.PrivilegedAuthority + ". Unlike a confined box, closing a session or losing its holder ends that session's process group. " +
	"An existing box switches modes only through an explicit box recreate; --privileged=false selects a confined launch."

// privilegedLaunch is the kind of container launch a seam performs.
type privilegedLaunch int

const (
	launchCreate   privilegedLaunch = iota // a new container from the declaration
	launchRecreate                         // the existing container replaced from the declaration
	launchRestart                          // the existing container stopped and started again
	launchStart                            // the existing stopped container started
)

// privilegedLaunchError refuses an operator's privileged launch. A command
// that saved a declaration first reports that with savedDeclarationError.
type privilegedLaunchError struct {
	space, box, reason string
}

func (e *privilegedLaunchError) Error() string {
	return fmt.Sprintf("box operators cannot start privileged box %s: %s; only the host starts it (yoloctl box start %s --space %s), after which operators may use, restart or recreate it while it runs",
		e.box, e.reason, e.box, e.space)
}

// privilegedLaunchWarnings receives the host launch warning, on the same
// stream as the other authority warnings; tests replace it.
var privilegedLaunchWarnings io.Writer = os.Stderr

// guardPrivilegedLaunch authorizes one launch of cfg's container. details and
// inspectErr are the seam's observation of the existing container; both are
// nil for a new box.
func guardPrivilegedLaunch(ctx context.Context, cfg *agentbox.Config, launch privilegedLaunch, details *container.Details, inspectErr error) error {
	why := privilegedLaunchReason(cfg, launch, details, inspectErr)
	if why == "" {
		return nil
	}
	if currentControllerPrincipal(ctx).Admin() {
		_, _ = fmt.Fprintf(privilegedLaunchWarnings, "warning: launching box %s as privileged (%s): %s\n", cfg.Name, why, agentbox.PrivilegedAuthority)
		return nil
	}
	if inspectErr == nil && continuesPrivilegedRuntime(ctx, cfg, launch, details) {
		return nil
	}
	return asUserError(&privilegedLaunchError{space: cfg.Space, box: cfg.Name, reason: why + launchGap(launch, details, inspectErr)})
}

// launchGap says why a privileged launch continues no host-started runtime.
func launchGap(launch privilegedLaunch, details *container.Details, inspectErr error) string {
	switch {
	case inspectErr != nil:
		return ""
	case launch == launchCreate:
		return "; creating it would start a new container"
	case details == nil || !details.Running:
		return "; it is not running"
	case !currentPrivilegedLaunch(details):
		return "; its running container is not a verified privileged launch on endpoint protocol " + boxendpoint.ProtocolVersion
	}
	return ""
}

// privilegedLaunchReason says why a launch may run privileged, or "" when it
// is proved confined. A new container follows the declaration; starting an
// existing one also runs whatever that container was created as.
func privilegedLaunchReason(cfg *agentbox.Config, launch privilegedLaunch, details *container.Details, inspectErr error) string {
	switch {
	case cfg.Privileged:
		return "it is declared privileged"
	case launch == launchCreate || launch == launchRecreate:
		return ""
	case inspectErr != nil:
		return "its runtime cannot be inspected"
	case details == nil:
		return "" // no existing container to start
	}
	evidence := agentbox.ClassifyRuntime(details)
	if evidence.AnyPrivileged {
		return "its existing container is privileged"
	}
	if evidence.Mode == agentbox.RuntimeModeUnknown {
		return "its runtime launch mode cannot be verified (" + evidence.Reason + ")"
	}
	return ""
}

// continuesPrivilegedRuntime reports whether an operator launch continues a
// host-started runtime within the same request: a managed restart/recreate of
// a running privileged launch, or the start leg of an explicit box restart
// authorized for exactly this container before it was stopped. Creating, or
// starting a container that merely stopped, never continues anything.
func continuesPrivilegedRuntime(ctx context.Context, cfg *agentbox.Config, launch privilegedLaunch, details *container.Details) bool {
	switch launch {
	case launchRestart, launchRecreate:
		return runningPrivilegedLaunch(details)
	case launchStart:
		restart, ok := ctx.Value(privilegedRestartKey{}).(privilegedRestart)
		return ok && details != nil && restart == privilegedRestart{space: cfg.Space, box: cfg.Name, id: details.ID} && currentPrivilegedLaunch(details)
	}
	return false
}

// currentPrivilegedLaunch reports whether details are a consistent privileged
// launch on the current endpoint protocol; older or conflicting evidence
// never authorizes a continuation.
func currentPrivilegedLaunch(details *container.Details) bool {
	if details == nil {
		return false
	}
	evidence := agentbox.ClassifyRuntime(details)
	return evidence.Mode == agentbox.RuntimeModePrivileged && evidence.Protocol == boxendpoint.ProtocolVersion
}

func runningPrivilegedLaunch(details *container.Details) bool {
	return details != nil && details.Running && currentPrivilegedLaunch(details)
}

type privilegedRestartKey struct{}

// privilegedRestart binds an explicit box restart's start leg to the
// container the request observed running before stopping it.
type privilegedRestart struct {
	space, box, id string
}

// authorizePrivilegedRestart runs before an explicit box restart stops
// anything. An operator may restart a privileged box only while it is a
// running privileged launch; the returned context then authorizes starting
// that same container once, and callers keep it to the start leg.
func authorizePrivilegedRestart(ctx context.Context, runtime operatorChatRuntime, space, name string) (context.Context, error) {
	if currentControllerPrincipal(ctx).Admin() {
		return ctx, nil
	}
	cfg, err := loadManagedAgentbox(space, name)
	if err != nil {
		return ctx, err
	}
	details, inspectErr := inspectOperatorChatTarget(ctx, name, runtime)
	why := privilegedLaunchReason(cfg, launchRestart, details, inspectErr)
	if why == "" {
		return ctx, nil
	}
	if inspectErr != nil || !runningPrivilegedLaunch(details) {
		return ctx, asUserError(&privilegedLaunchError{space: space, box: name, reason: why + launchGap(launchRestart, details, inspectErr)})
	}
	return context.WithValue(ctx, privilegedRestartKey{}, privilegedRestart{space: space, box: name, id: details.ID}), nil
}

// guardOperatorPrivilegedCreate refuses an operator create or copy that would
// launch a new privileged box. --manifest-only saves the same declaration
// without a container, for the host to start.
func guardOperatorPrivilegedCreate(ctx context.Context, cfg *agentbox.Config) error {
	if currentControllerPrincipal(ctx).Admin() {
		return nil
	}
	if why := privilegedLaunchReason(cfg, launchCreate, nil, nil); why != "" {
		return asUserError(fmt.Errorf("box operators cannot create and start privileged box %s: %s; nothing was saved. Save only its declaration with --manifest-only; then the host starts it: yoloctl box start %s --space %s",
			cfg.Name, why, cfg.Name, cfg.Space))
	}
	return nil
}

// savedDeclarationError reports a privileged launch refused after a command
// saved the declaration, so the saved change is not mistaken for a rollback.
func savedDeclarationError(cfg *agentbox.Config, err error) error {
	var refused *privilegedLaunchError
	if errors.As(err, &refused) {
		return asUserError(fmt.Errorf("box %s declaration is saved but not applied: %w", cfg.Name, err))
	}
	return err
}

// warnBoxAuthority states every space-wide authority a saved declaration
// grants.
func warnBoxAuthority(cfg *agentbox.Config) {
	warnBoxOperator(cfg)
	if cfg.Privileged {
		_, _ = fmt.Fprintf(os.Stderr, "warning: box %s is privileged: %s\n", cfg.Name, agentbox.PrivilegedAuthority)
	}
}

// privilegedNotice is the one-line runtime summary for show and inspect.
func privilegedNotice(evidence agentbox.RuntimeEvidence) string {
	var b strings.Builder
	b.WriteString("⚠ PRIVILEGED — space-VM root, no OpenShell confinement; space operators can use, restart and recreate it while it runs, only the host starts it")
	switch evidence.Mode {
	case agentbox.RuntimeModePrivileged:
		b.WriteString(" (runtime verified: " + evidence.Launcher + ")")
	case agentbox.RuntimeModeConfined:
		b.WriteString(" (declared; the running container is still confined — explicit box recreate required)")
	default:
		b.WriteString(" (runtime unverified)")
	}
	return b.String()
}
