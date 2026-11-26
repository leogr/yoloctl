package controller

import (
	"context"
	"errors"

	"github.com/containerd/errdefs"
	"github.com/leogr/yoloctl/internal/imagejob"
)

type localImageResolver interface {
	ResolveLocal(context.Context, string) (imagejob.LocalTarget, string, error)
}

// Only the trusted image backend supplies this read. An unsupported adapter,
// transport failure or incomplete local graph never falls back to Resolve's
// registry behavior. Genuine source absence alone is a separate wire result.
func resolveLocalImageCheckpoint(ctx context.Context, backend any, request ImageRequest) (ImageReply, error) {
	if request.Remote {
		return ImageReply{}, errors.New("local image resolution cannot request a remote lookup")
	}
	resolver, ok := backend.(localImageResolver)
	if !ok {
		return ImageReply{}, errors.New("controller backend does not support qualified local image retention")
	}
	// The read follows the checkpoint's worker context: the client's
	// cancellation and the controller's drain end it, a slow store does not.
	target, platform, err := resolver.ResolveLocal(ctx, request.Reference)
	if err != nil {
		if ctx.Err() != nil {
			return ImageReply{}, errors.Join(err, ctx.Err())
		}
		if errdefs.IsNotFound(err) {
			return ImageReply{LocalMissing: true}, nil
		}
		return ImageReply{}, err
	}
	if err := ctx.Err(); err != nil {
		return ImageReply{}, err
	}
	if _, err := imagejob.NewLocalRetain(request.Reference, platform, target); err != nil {
		return ImageReply{}, err
	}
	return ImageReply{LocalTarget: &target, Digest: target.Digest, Platform: platform}, nil
}

// localImageCheckpointContext is the context of one local image read
// checkpoint (resolve-local, validate). It follows the worker context, except
// while the setup is actually owned (after effect permission, before handoff):
// then it outlives its viewer, so nothing but one fail-safe ends the read.
func localImageCheckpointContext(ctx context.Context, ownedSetup bool) (context.Context, context.CancelFunc) {
	if ownedSetup {
		return context.WithTimeout(ctx, controllerFailSafe)
	}
	return ctx, func() {}
}

// localImageRead serves the two local image read checkpoints of an execution
// (resolve-local, and validate of its retained pin) under the context that
// actual setup ownership selects.
func (s *Server) localImageRead(ctx context.Context, owner *setupOwner, r ImageRequest, pin *RetainedImage) (ImageReply, error) {
	read, done := localImageCheckpointContext(ctx, owner.owned())
	defer done()
	if r.Action == "validate" {
		return ImageReply{}, s.Images.backend.Validate(read, pin.PinnedImage)
	}
	return resolveLocalImageCheckpoint(read, s.Images.backend, r)
}
