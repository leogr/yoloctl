package boxstate

import (
	"context"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/session"
)

type deferredInventoryDetailsKey struct{}
type holderStatsAttemptKey struct{}

// WithHolderStatsAttempt limits only the next individual holder stats read.
// A recurring sampler can bound one try while its required inventory remains
// on the caller context.
func WithHolderStatsAttempt(ctx context.Context, attempt time.Duration) context.Context {
	return context.WithValue(ctx, holderStatsAttemptKey{}, attempt)
}

func readHolderBuild(ctx context.Context, name string) SessionBuild {
	if attempt, ok := ctx.Value(holderStatsAttemptKey{}).(time.Duration); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, attempt)
		defer cancel()
	}
	_, err := session.ReadStats(ctx, session.SocketPath(name))
	return sessionBuild(err)
}

func defersInventoryDetails(ctx context.Context) bool {
	return ctx.Value(deferredInventoryDetailsKey{}) == true
}

var sessionSummaryAllowance = time.Minute

// ResolveOneView keeps required details for one box when an optional
// holder or live report source is unavailable.
func (r Resolver) ResolveOneView(ctx context.Context, space, name string, depth Depth) (Box, error) {
	baseCtx := context.WithValue(ctx, deferredInventoryDetailsKey{}, true)
	box, err := r.ResolveOne(baseCtx, space, name, depth)
	if err != nil {
		return Box{}, err
	}
	boxes, err := r.enrichSessionSummary(ctx, []Box{box})
	if err != nil {
		return Box{}, err
	}
	return boxes[0], nil
}

// ResolveSpaceSummary enriches required inventory with optional live session
// fields under a shared budget. An unavailable holder does not erase its row.
func (r Resolver) ResolveSpaceSummary(ctx context.Context, space string) ([]Box, error) {
	baseCtx := context.WithValue(ctx, deferredInventoryDetailsKey{}, true)
	boxes, err := r.ResolveSpace(baseCtx, space, RuntimeSummary)
	if err != nil {
		return nil, err
	}
	return r.enrichSessionSummary(ctx, boxes)
}

// ResolveAllSummary shares the optional allowance across native spaces.
func (r Resolver) ResolveAllSummary(ctx context.Context) ([]Box, error) {
	baseCtx := context.WithValue(ctx, deferredInventoryDetailsKey{}, true)
	boxes, err := r.ResolveAll(baseCtx, RuntimeSummary)
	if err != nil {
		return nil, err
	}
	return r.enrichSessionSummary(ctx, boxes)
}

func (r Resolver) enrichSessionSummary(ctx context.Context, boxes []Box) ([]Box, error) {
	optional, cancel := context.WithTimeout(ctx, sessionSummaryAllowance)
	defer cancel()

	// Use the same small fan-out as holder telemetry. Keep results separate
	// while conversation projection consumes the resolved rows in order.
	const holderReaders = 8
	pending := make(chan int, len(boxes))
	builds := make([]SessionBuild, len(boxes))
	for i, box := range boxes {
		if r.HolderBuild != nil && box.Session.State == SessionRunning && box.Session.Build == SessionBuildUnknown {
			pending <- i
		}
	}
	close(pending)
	var readers sync.WaitGroup
	for range min(holderReaders, len(pending)) {
		readers.Go(func() {
			for i := range pending {
				if optional.Err() != nil {
					return
				}
				builds[i] = r.HolderBuild(optional, boxes[i].Name)
			}
		})
	}
	if r.Conversation != nil {
		for i, box := range boxes {
			if box.Config != nil && box.Runtime != RuntimeUnavailable {
				boxes[i].Conversation = r.Conversation(optional, box)
			}
		}
	}
	readers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i, build := range builds {
		if build != "" {
			boxes[i].Session.Build = build
		}
	}
	return boxes, nil
}
