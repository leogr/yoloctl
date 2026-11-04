package controller

import (
	"context"
	"errors"
	"time"

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
	read, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	target, platform, err := resolver.ResolveLocal(read, request.Reference)
	if err != nil {
		if read.Err() != nil {
			return ImageReply{}, errors.Join(err, read.Err())
		}
		if errdefs.IsNotFound(err) {
			return ImageReply{LocalMissing: true}, nil
		}
		return ImageReply{}, err
	}
	if err := read.Err(); err != nil {
		return ImageReply{}, err
	}
	if _, err := imagejob.NewLocalRetain(request.Reference, platform, target); err != nil {
		return ImageReply{}, err
	}
	return ImageReply{LocalTarget: &target, Digest: target.Digest, Platform: platform}, nil
}
