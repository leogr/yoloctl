package controller

import (
	"context"

	cd "github.com/containerd/containerd/v2/client"
	bk "github.com/moby/buildkit/client"
)

func NewNativeImageBackend(ctx context.Context) (*NativeImageBackend, error) {
	c, err := cd.New("/run/containerd/containerd.sock", cd.WithDefaultNamespace("default"))
	if err != nil {
		return nil, err
	}
	return &NativeImageBackend{Containerd: c, newBuilder: func(ctx context.Context) (*bk.Client, error) {
		return newBuildkitConnection(ctx, "unix:///run/buildkit/buildkitd.sock")
	}}, nil
}
