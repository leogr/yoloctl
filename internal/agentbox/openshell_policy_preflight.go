package agentbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/leogr/yoloctl/internal/container"
)

// PreflightPolicyRestart qualifies the ACTUAL desired policy before a managed
// caller ends its holder/session. It creates no per-container state. The
// actual Restart repeats this check and captures its own original checkpoint.
func (a LiveApplier) PreflightPolicyRestart(ctx context.Context, c *Config, policy []byte) error {
	_, err := a.preflightPolicyRestart(ctx, c, policy)
	return err
}
func (a LiveApplier) preflightPolicyRestart(ctx context.Context, c *Config, policy []byte) (bool, error) {
	return a.qualifyNativeRuntimeRoute(ctx, c, func(check context.Context) error { return a.preflightNativePolicyRestart(check, c, policy) })
}
func (a LiveApplier) qualifyNativeRuntimeRoute(ctx context.Context, c *Config, qualify func(context.Context) error) (bool, error) {
	if a.Client == nil || c == nil {
		return false, errors.New("policy restart qualification requires its client and declaration")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	known, err := a.Client.List(ctx, LabelAgentbox+"="+c.Name)
	if err != nil {
		return false, err
	}
	if len(known) == 0 {
		return false, nil
	}
	details, err := a.Client.Inspect(ctx, c.Name)
	if err != nil {
		return false, err
	}
	native, err := preflightPolicyRestartRoute(ctx, details, qualify)
	if err != nil || native {
		return native, err
	}
	return false, CheckRetainedLegacyStart(ctx, details, a.Client.CheckRetainedLegacyOpenShell)
}
func preflightPolicyRestartRoute(ctx context.Context, details *container.Details, native func(context.Context) error) (bool, error) {
	if details == nil || native == nil {
		return false, errors.New("policy restart qualification lacks inspected route evidence")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	nativeRoute, err := NativeRuntimeRoute(details)
	if err != nil || !nativeRoute {
		return nativeRoute, err
	}
	return true, native(ctx)
}

// NativeRuntimeRoute is observed classification only. It supplies no storage,
// launch, process, receipt or admission authority; effect callers must perform
// their independent current verification and native SDK qualification.
func NativeRuntimeRoute(details *container.Details) (bool, error) {
	if details == nil {
		return false, errors.New("native runtime route lacks inspected evidence")
	}
	if !hasOpenShellRuntimeSignals(details) {
		return false, nil
	}
	if ev := ClassifyRuntime(details); ev.Mode != RuntimeModeConfined {
		if ev.AnyPrivileged {
			return true, errors.New("native runtime evidence reports a privileged container")
		}
		// As on the old path, runtime records that do not fully match (for
		// example binds an engine reports differently) are a named warning,
		// never a refusal; the native lifecycle still handles the box.
		nativeRouteNotice(details.ID, fmt.Errorf("box %s: mixed or incomplete native runtime evidence (%s); continuing", details.Name, ev.Reason))
	}
	return true, nil
}

// nativeRouteNotice prints a route warning once per container and message;
// tests replace it.
var nativeRouteNotice = func() func(string, error) {
	var mu sync.Mutex
	seen := map[string]bool{}
	return func(id string, err error) {
		key := id + "\x00" + err.Error()
		mu.Lock()
		defer mu.Unlock()
		if len(seen) > 256 {
			clear(seen)
		}
		if !seen[key] {
			seen[key] = true
			_, _ = fmt.Fprintf(os.Stderr, "warning: %v\n", err)
		}
	}
}()
