package controller

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	bk "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/client/llb"
	digest "github.com/opencontainers/go-digest"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

type imageSolver interface {
	Solve(context.Context, *llb.Definition, bk.SolveOpt, chan *bk.SolveStatus) (*bk.SolveResponse, error)
}

// Solve owns closing its status channel, including pre-solve errors. Keep the
// reader alive until that close even when the caller cancels: abandoning it
// early would strand the Solve status pump. Progress is memory-only fanout:
// BuildKit vertices become child steps of the job's running step, vertex logs
// their detail lines and warnings warn records.
func solveImageWithProgress(ctx context.Context, builder imageSolver, opt bk.SolveOpt) (*bk.SolveResponse, error) {
	steps := newSolveSteps(imageBuildProgress(ctx))
	statuses := make(chan *bk.SolveStatus)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for status := range statuses {
			steps.apply(status)
		}
	}()
	response, err := builder.Solve(buildkitContext(ctx), nil, opt, statuses)
	<-done
	steps.finish(err)
	return response, err
}

// imageProgressTarget is where one job's build progress goes.
type imageProgressTarget struct {
	step *prettylog.Step // the running build step; vertices are its children
	log  *slog.Logger    // the job Logger, for records that are not steps
}

func imageBuildProgress(ctx context.Context) imageProgressTarget {
	t, _ := ctx.Value(imageBuildStepKey{}).(imageProgressTarget)
	return t
}

type vertexStep struct {
	step  *prettylog.Step
	ended bool
}

// solveSteps maps BuildKit vertices to steps. It is used by the one status
// reader goroutine and then by finish, after that goroutine ended.
type solveSteps struct {
	target imageProgressTarget
	names  map[digest.Digest]string
	steps  map[digest.Digest]*vertexStep
	order  []digest.Digest
}

func newSolveSteps(target imageProgressTarget) *solveSteps {
	return &solveSteps{target: target, names: map[digest.Digest]string{}, steps: map[digest.Digest]*vertexStep{}}
}

func (s *solveSteps) vertex(d digest.Digest) *vertexStep {
	if v := s.steps[d]; v != nil {
		return v
	}
	if s.target.step == nil {
		return nil
	}
	name := s.names[d]
	if name == "" {
		name = d.String()
	}
	v := &vertexStep{step: s.target.step.Step(name)}
	s.steps[d] = v
	s.order = append(s.order, d)
	return v
}

// apply handles one status in three passes: vertices that started, then
// their logs, then completions. A status that carries a vertex's final
// output together with its completion therefore keeps that output; logs
// for a vertex that already ended are still added to its retained detail.
func (s *solveSteps) apply(status *bk.SolveStatus) {
	if status == nil {
		return
	}
	for _, v := range status.Vertexes {
		if v == nil {
			continue
		}
		if v.Name != "" {
			s.names[v.Digest] = v.Name
		}
		if v.Started != nil || v.Completed != nil {
			s.vertex(v.Digest)
		}
	}
	for _, l := range status.Logs {
		if l == nil || len(l.Data) == 0 {
			continue
		}
		vs := s.vertex(l.Vertex)
		switch {
		case vs == nil:
		case !vs.ended:
			_, _ = vs.step.Detail().Write(detailLines(l.Data))
		case s.target.log != nil:
			for _, line := range strings.Split(strings.TrimSuffix(string(detailLines(l.Data)), "\n"), "\n") {
				s.target.log.Debug(line, slog.String(prettylog.KeyStep, vs.step.ID()), slog.Bool(prettylog.KeyDetail, true))
			}
		}
	}
	for _, v := range status.Vertexes {
		if v == nil || v.Completed == nil {
			continue
		}
		vs := s.steps[v.Digest]
		if vs == nil || vs.ended {
			continue
		}
		vs.ended = true
		switch {
		case v.Error != "" && strings.Contains(v.Error, context.Canceled.Error()):
			vs.step.Skip("canceled")
		case v.Error != "":
			vs.step.Fail(errors.New(v.Error))
		case v.Cached:
			vs.step.Done(slog.Bool("cached", true))
		default:
			vs.step.Done()
		}
	}
	for _, w := range status.Warnings {
		if w == nil || s.target.log == nil {
			continue
		}
		msg := string(w.Short)
		for _, line := range w.Detail {
			msg += "\n" + string(line)
		}
		if w.URL != "" {
			msg += "\n" + w.URL
		}
		s.target.log.Warn(msg)
	}
}

// detailLines turns terminal progress (CR redraws) into whole lines, so a
// detail record never carries a bare carriage return.
func detailLines(data []byte) []byte {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	return []byte(strings.ReplaceAll(text, "\r", "\n"))
}

// finish ends vertices BuildKit left open: done after a successful solve,
// otherwise skipped as canceled by the failure.
func (s *solveSteps) finish(err error) {
	for _, d := range s.order {
		vs := s.steps[d]
		if vs.ended {
			continue
		}
		vs.ended = true
		if err == nil {
			vs.step.Done()
		} else {
			vs.step.Skip("canceled")
		}
	}
}
