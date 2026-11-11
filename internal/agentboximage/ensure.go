package agentboximage

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"

	"github.com/leogr/yoloctl/assets"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/leogr/yoloctl/pkg/prettylog"
)

// Client is the slice of container.Client surface agentboximage
// actually needs. Carved out so unit tests can wire a fake without
// pulling in limactl. Real callers pass a *container.Client.
type Client interface {
	ImagePresent(ctx context.Context, ref string) (bool, error)
	Build(ctx context.Context, opts container.BuildOptions) error
}

// Compile-time check that container.Client implements Client. Catches
// signature drift if container.Client's API ever changes.
var _ Client = (*container.Client)(nil)

// Options configures EnsureImage. Zero value is the "build if missing" path;
// the agentbox Create path calls it that way.
type Options struct {
	FeaturePlan ocifeature.Plan
	// Notice receives one-line progress messages ("notice: image X
	// not present...", "notice: image X rebuilding..."). io.Discard
	// or nil to silence; LogNotices sends them as Notice records to the
	// process Logger (slog.Default) instead.
	Notice io.Writer

	// Force reruns image installers, preserving their download-cache mounts
	// where the recipe supports it. Used by image refresh and box rebuild.
	Force bool
}

// EnsureImage guarantees ref exists in the space VM's local image
// cache. For RefAgentbox it builds the image from the embedded build context
// if missing. For unmanaged refs it is a no-op, leaving subsequent `nerdctl
// run` to pull from a registry or fail as it would normally.
//
// Idempotent unless opts.Force is set. An already-present image
// costs one `nerdctl image inspect` roundtrip; a build emits a
// "notice: building image..." line so users understand a multi-
// minute pause on first run.
//
// Force semantics: a fresh build argument invalidates the install steps while
// retaining BuildKit download-cache mounts. --no-cache would reset those too.
//
// Concurrency: two parallel calls for the same ref both proceed
// independently. `nerdctl build` tolerates concurrent invocation
// (last writer wins the tag, identical content). No explicit lock.
func EnsureImage(ctx context.Context, c Client, ref string, opts Options) error {
	if opts.Notice == nil {
		opts.Notice = io.Discard
	}
	if ref != RefAgentbox {
		// Not yoloctl-managed; defer to nerdctl run's pull semantics.
		return nil
	}

	if !opts.Force {
		present, err := c.ImagePresent(ctx, ref)
		if err != nil {
			return fmt.Errorf("check image %s: %w", ref, err)
		}
		if present {
			return nil
		}
	}
	if opts.Force {
		notice(ctx, opts.Notice, "rebuilding image %s from embedded context (rerunning installers, retaining downloads)", ref)
	} else {
		notice(ctx, opts.Notice, "image %s not present in space; building from embedded context (this may take several minutes on first run)", ref)
	}
	tarBuf, err := tarContext(assets.AgentboxContexts, agentboxContext)
	if err != nil {
		return fmt.Errorf("prepare context for %s: %w", ref, err)
	}
	if err := c.Build(ctx, container.BuildOptions{
		Tag:        ref,
		ContextTar: tarBuf,
		BuildArgs:  imageRefreshArgs(opts.Force),
	}); err != nil {
		return fmt.Errorf("build %s from %s: %w", ref, agentboxContext, err)
	}
	return nil
}

// The base and prepared composition stage declare this ARG before any RUN.
// Child stages inherit it. Its value is deliberately outside the composed tag
// hash: refresh replaces the same tag, while ordinary builds remain cacheable.
const imageRefreshArg = "YOLOCTL_IMAGE_REFRESH"

func imageRefreshArgs(force bool) map[string]string {
	if !force {
		return nil
	}
	return map[string]string{imageRefreshArg: rand.Text()}
}

// LogNotices is the Options.Notice value that reports build notices as
// Notice records through the process Logger (relayed to the host from a
// controller worker) rather than as text lines.
var LogNotices io.Writer = logNotices{}

type logNotices struct{}

func (logNotices) Write(p []byte) (int, error) { return len(p), nil }

func notice(ctx context.Context, w io.Writer, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if _, ok := w.(logNotices); ok {
		slog.Default().Log(ctx, prettylog.LevelNotice, msg, prettylog.Actor("builder"), prettylog.Executor("nerdctl"))
		return
	}
	_, _ = fmt.Fprintf(w, "notice: %s\n", msg)
}
