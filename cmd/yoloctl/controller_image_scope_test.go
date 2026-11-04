package main

import (
	"context"
	"slices"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/spf13/cobra"
)

func TestAstraSetIntentReadsRequestedImage(t *testing.T) {
	cfg, ctr := runtimeAccountFixture(t)
	newBoxStateResolver = runtimeAccountResolver(t, []container.Container{ctr}, nil)
	desired := *cfg
	desired.Image = "example/new-base:latest"
	ref, _, err := agentbox.ResolvedImage(&desired)
	if err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.Flags().String("image", desired.Image, "")
	cmd.Flags().String("account", cfg.Account, "")
	intent := spaceOperationIntent(cmd, "box set", "work", []string{cfg.Name})
	if intent.Scope.Global {
		t.Fatal("unexpected global fixture")
	}
	if !slices.Contains(intent.Scope.Reads, controller.Resource{Kind: controller.ResourceImages, Name: ref}) {
		t.Fatalf("requested image %q missing from apply reads: %+v", ref, intent.Scope)
	}
}

func TestDesiredImageDependenciesCoverNewDestinationEditAndChat(t *testing.T) {
	cfg, ctr := runtimeAccountFixture(t)
	newBoxStateResolver = runtimeAccountResolver(t, []container.Container{ctr}, nil)
	desired := *cfg
	desired.Image = "example/new:latest"
	ref, _, err := agentbox.ResolvedImage(&desired)
	if err != nil {
		t.Fatal(err)
	}
	state := &imageWarmState{desired: &desired}
	ctx := context.WithValue(t.Context(), warmKey{}, state)
	for _, path := range []string{"box create", "box copy", "box set"} {
		cmd := &cobra.Command{}
		cmd.SetContext(ctx)
		args := []string{cfg.Name}
		if path == "box copy" {
			args = []string{cfg.Name, "newbox"}
		}
		intent := spaceOperationIntent(cmd, path, "work", args)
		if intent.Scope == nil || intent.Scope.Global || !slices.Contains(intent.Scope.Reads, controller.Resource{Kind: controller.ResourceImages, Name: ref}) {
			t.Fatal(path, intent)
		}
	}
	for _, intent := range []controller.EffectIntent{boxEditIntent(ctx, "work", &desired), addDesiredImageIntent(ctx, chatIntent("", cfg.Name))} {
		if intent.Scope == nil || intent.Scope.Global || !slices.Contains(intent.Scope.Reads, controller.Resource{Kind: controller.ResourceImages, Name: ref}) {
			t.Fatal(intent)
		}
	}
}
