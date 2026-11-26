package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/leogr/yoloctl/internal/boxstate"
)

// preflightBoxExec runs in the controller worker before selecting any SSH
// transport. Inventory depth deliberately omits session probes: exec needs a
// running container, not an agent session, and must not dial to discover absence.
func preflightBoxExec(ctx context.Context, space, name string) error {
	box, err := newBoxStateResolver().ResolveOne(ctx, space, name, boxstate.RuntimeInventory)
	if err != nil {
		return fmt.Errorf("cannot check box %q in space %q before exec: %w", name, space, err)
	}
	switch box.Declaration {
	case boxstate.DeclarationMissing:
		if box.Runtime == boxstate.RuntimeOrphaned {
			return asUserError(fmt.Errorf("box %q has no declaration in space %q; refusing exec into an unmanaged container", name, space))
		}
		return asUserError(fmt.Errorf("box %q does not exist in space %q", name, space))
	case boxstate.DeclarationInvalid:
		return asUserError(fmt.Errorf("box %q declaration is invalid in space %q: %s", name, space, box.ConfigError))
	case boxstate.DeclarationDeclared:
	default:
		return fmt.Errorf("box %q declaration is unavailable in space %q", name, space)
	}
	switch box.Runtime {
	case boxstate.RuntimeRunning:
		return nil
	case boxstate.RuntimeMissing:
		return asUserError(fmt.Errorf("box %q has no container in space %q; start it with: yoloctl box start %s --space %s", name, space, name, space))
	case boxstate.RuntimeStopped:
		// Display classification calls every non-Up status stopped. Do not
		// turn unknown/transitional text into an affirmative stopped claim.
		status := strings.ToLower(strings.TrimSpace(box.RuntimeText))
		if status == "created" || status == "stopped" || strings.HasPrefix(status, "exited") {
			return asUserError(fmt.Errorf("box %q is stopped in space %q; start it with: yoloctl box start %s --space %s", name, space, name, space))
		}
	}
	return fmt.Errorf("box %q is not known to be running in space %q (runtime %s, status %q); inspect it with: yoloctl box show %s --space %s", name, space, box.Runtime, box.RuntimeText, name, space)
}
