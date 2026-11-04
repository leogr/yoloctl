package main

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/internal/controller"
)

type startupImageBackend struct{}

func (*startupImageBackend) Configure(func(context.Context, controller.ImageRecipe) error, func(context.Context) error) {
}
func (*startupImageBackend) Close()                                            {}
func (*startupImageBackend) Sweep(context.Context, []controller.Receipt) error { return nil }
func (*startupImageBackend) Preflight(ctx context.Context, _ controller.ImageRecipe) error {
	return ctx.Err()
}
func (*startupImageBackend) Build(ctx context.Context, _ controller.ImageRecipe, p controller.ImagePhase) (controller.PinnedImage, error) {
	if ctx.Err() != nil {
		return controller.PinnedImage{}, ctx.Err()
	}
	return controller.PinnedImage{JobID: p.JobID, Reference: p.PreparedRef, Lease: "lease-" + p.JobID, Digest: "sha256:" + strings.Repeat("a", 64)}, nil
}
func (*startupImageBackend) Validate(context.Context, controller.PinnedImage) error { return nil }
func (*startupImageBackend) Release(context.Context, controller.PinnedImage) error  { return nil }
func (*startupImageBackend) Resolve(context.Context, string) (string, string, error) {
	return "", "", nil
}
func (*startupImageBackend) ResolveRemote(context.Context, string) (string, string, error) {
	return "", "", nil
}
func (*startupImageBackend) Publish(context.Context, controller.PinnedImage, string) error {
	return nil
}
func TestRepairedControllerImageManagerUsesServiceLifetime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native controller incarnation is Linux-only")
	}
	service, cancel := context.WithCancel(t.Context())
	defer cancel()
	original := newControllerImageBackend
	t.Cleanup(func() { newControllerImageBackend = original })
	initialized := make(chan context.Context, 1)
	newControllerImageBackend = func(ctx context.Context) (controllerImageBackend, error) {
		initialized <- ctx
		return &startupImageBackend{}, nil
	}
	server := &controller.Server{Hello: controller.Hello{Space: "work", Generation: "generation"}, DiskID: strings.Repeat("a", 32), Receipts: controller.Receipts{Dir: t.TempDir()}}
	var stop func()
	server.Degrade(errors.New("simulated startup storage failure"), func(init context.Context) error {
		var err error
		stop, err = startControllerImages(service, init, server)
		return err
	})
	done := make(chan struct{})
	go func() { server.RunRecovery(service); close(done) }()
	init := <-initialized
	select {
	case <-init.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("repair pass did not end")
	}
	recipe := controller.ImageRecipe{Builder: "test", Platform: "linux/amd64", BaseDigest: "sha256:" + strings.Repeat("b", 64), Dockerfile: []byte("FROM scratch")}
	result, err := server.Images.Acquire(service, recipe)
	if err != nil {
		t.Fatal("repair cancelled the retained manager", err)
	}
	result.Release()
	cancel()
	<-done
	if stop != nil {
		stop()
	}
}
