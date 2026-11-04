package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/containerd/containerd/v2/pkg/namespaces"
	bk "github.com/moby/buildkit/client"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// Containerd's default-namespace interceptor checks incoming namespace metadata
// before deciding whether to set outgoing metadata. Forwarding our containerd
// header to BuildKit makes that worker interceptor skip the outgoing header on
// its Diff RPC. Keep this header confined to our direct containerd operations.
func buildkitContext(ctx context.Context) context.Context {
	md, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return ctx
	}
	md = md.Copy()
	md.Delete(namespaces.GRPCHeader)
	return metadata.NewOutgoingContext(ctx, md)
}
func newBuildkitConnection(ctx context.Context, address string, opts ...bk.ClientOpt) (*bk.Client, error) {
	opts = append(opts,
		bk.WithGRPCDialOption(grpc.WithChainUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) error {
			return invoke(buildkitContext(ctx), method, req, reply, cc, options...)
		})),
		bk.WithGRPCDialOption(grpc.WithChainStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, stream grpc.Streamer, options ...grpc.CallOption) (grpc.ClientStream, error) {
			return stream(buildkitContext(ctx), desc, cc, method, options...)
		})),
	)
	return bk.New(buildkitContext(ctx), address, opts...)
}

func (b *NativeImageBackend) readyBuilder(ctx context.Context) (*bk.Client, error) {
	notStarted := func(err error) (*bk.Client, error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &ImageBuildFailure{Cause: fmt.Errorf("image build not started: %w", err)}
	}
	if b.EnsureBuilder == nil {
		return notStarted(errors.New("builder startup unavailable"))
	}
	if err := b.EnsureBuilder(ctx); err != nil {
		return notStarted(err)
	}
	// This is an initialization/readiness budget, not a build timeout or retry.
	readyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	b.builderMu.Lock()
	if b.closed {
		b.builderMu.Unlock()
		return notStarted(errors.New("image backend is closed"))
	}
	if b.Builder == nil {
		if b.newBuilder == nil {
			b.builderMu.Unlock()
			return notStarted(errors.New("builder connection unavailable"))
		}
		client, err := b.newBuilder(buildkitContext(readyCtx))
		if err != nil {
			b.builderMu.Unlock()
			return notStarted(err)
		}
		b.Builder = client
	}
	client := b.Builder
	b.builderMu.Unlock()
	// Wait only issues Info RPCs and resets reconnect backoff. No Solve has
	// been sent yet; a failure here is a known not-started preparation.
	if err := client.Wait(buildkitContext(readyCtx)); err != nil {
		return notStarted(fmt.Errorf("BuildKit readiness: %w", err))
	}
	return client, nil
}
