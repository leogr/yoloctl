package main

import (
	"context"
	"fmt"
	"os"

	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/spf13/cobra"
)

type tabRequestsKey struct{}

// Claim once before any external helpers run. Descendants inherit neither the
// descriptor (ViewFile marks it close-on-exec) nor its environment variable.
func claimTabRequests(ctx context.Context) (context.Context, func(), error) {
	f, err := termio.InheritedTabRequests()
	if err != nil || f == nil {
		return ctx, func() {}, err
	}
	_ = os.Unsetenv(termio.TabRequestFDEnv)
	return context.WithValue(ctx, tabRequestsKey{}, f), func() { _ = f.Close() }, nil
}

func tabRequests(ctx context.Context) *os.File {
	f, _ := ctx.Value(tabRequestsKey{}).(*os.File)
	return f
}

// publishControllerResumeTarget records an explicit ordinary attach action for
// an eligible host lifecycle request. It is not evidence of remote success or
// of reaching the auto-attach tail; the original outcome remains unchanged.
func publishControllerResumeTarget(cmd *cobra.Command, target controllerTarget, req controller.Request) error {
	if target.Operator || !req.Terminal || tabRequests(cmd.Context()) == nil ||
		flagTrue(req.Flags, "no-attach") || flagTrue(req.Flags, "manifest-only") || flagTrue(req.Flags, "box-use") {
		return nil
	}
	count := 1
	switch req.Method {
	case "box.create", "box.start", "box.restart", "box.recreate":
	case "box.copy":
		count = 2
	default:
		return nil
	}
	if len(req.Args) != count || layout.ValidateExistingSpaceName(req.Space) != nil || layout.ValidateName("agentbox", req.Args[count-1]) != nil {
		return nil // never publish managedBoxName's display fallback
	}
	return publishResumeTarget(cmd, req.Space, req.Args[count-1], flagTrue(req.Flags, "no-autoupdate"), flagTrue(req.Flags, "passthrough"))
}

// publishResumeTarget writes only names and applicable attach options to
// the originating pane's context-owned channel, never a command or argv.
func publishResumeTarget(cmd *cobra.Command, space, box string, noAutoupdate, passthrough bool) error {
	r := termio.TabRequest{Op: "resume-target", Space: space, Box: box, NoAutoupdate: noAutoupdate, Passthrough: passthrough}
	kind := termio.ResumeTargetSpace
	if box != "" {
		kind = termio.ResumeTargetBox
	}
	if err := r.ValidateResumeTarget(kind); err != nil {
		return err
	}
	f := tabRequests(cmd.Context())
	if f == nil {
		return nil
	}
	if err := termio.WriteTabRequest(f, r); err != nil {
		return fmt.Errorf("record pane resume target: %w", err)
	}
	return nil
}
