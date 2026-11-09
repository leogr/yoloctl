package tui

import (
	"github.com/leogr/yoloctl/internal/termio"
	"github.com/leogr/yoloctl/internal/tui/panes"
)

// resumeTargetKind admits metadata only for command classes that can enter
// managed attachment. Target names come from the CLI, never argv guessing.
func resumeTargetKind(args []string) termio.ResumeTargetKind {
	if hasHelp(args) || len(args) < 2 || args[0] != "box" {
		return termio.ResumeTargetNone
	}
	switch args[1] {
	case "create", "copy", "start", "restart", "recreate":
		if !boxLifecycleAttachSuppressed(args[1], args[2:]) {
			return termio.ResumeTargetBox
		}
	}
	return termio.ResumeTargetNone
}

func reattachArgsForPane(p *panes.Pane) ([]string, string, bool) {
	if p == nil {
		return nil, "", false
	}
	// Never replace guarded direct attach argv with an unguarded target.
	if args, box, ok := reattachArgs(p.Args); ok {
		return args, box, true
	}
	if p.Session == nil || resumeTargetKind(p.Args) != termio.ResumeTargetBox {
		return nil, "", false
	}
	r, ok := p.Session.ResumeTarget()
	if !ok || r.ValidateResumeTarget(termio.ResumeTargetBox) != nil {
		return nil, "", false
	}
	args := []string{"box", "attach", r.Box, "--space", r.Space}
	if r.NoAutoupdate {
		args = append(args, "--no-autoupdate")
	}
	if r.Passthrough {
		args = append(args, "--passthrough")
	}
	return args, r.Box, true
}
