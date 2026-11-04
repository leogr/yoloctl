package controller

import (
	"context"
	"fmt"
	"io"

	bk "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/util/progress/progressui"
)

type imageSolver interface {
	Solve(context.Context, *llb.Definition, bk.SolveOpt, chan *bk.SolveStatus) (*bk.SolveResponse, error)
}

// Solve owns closing its status channel, including pre-solve errors. Keep the
// renderer alive until that close even when the caller cancels: abandoning a
// reader early would strand the Solve status pump. Output is memory-only fanout.
func solveImageWithProgress(ctx context.Context, builder imageSolver, opt bk.SolveOpt) (*bk.SolveResponse, error) {
	out := imageBuildOutput(ctx)
	display, err := progressui.NewDisplay(out, progressui.PlainMode)
	if err != nil {
		return nil, err
	}
	statuses := make(chan *bk.SolveStatus)
	done := make(chan struct{})
	go func() {
		defer close(done)
		warnings, _ := display.UpdateFrom(context.WithoutCancel(ctx), statuses)
		for _, warning := range warnings {
			_, _ = fmt.Fprintf(out, "WARNING: %s\n", warning.Short)
			for _, line := range warning.Detail {
				_, _ = fmt.Fprintf(out, "%s\n", line)
			}
			if warning.URL != "" {
				_, _ = io.WriteString(out, warning.URL+"\n")
			}
		}
	}()
	response, err := builder.Solve(buildkitContext(ctx), nil, opt, statuses)
	<-done
	return response, err
}
