package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/ocifeature"
	"github.com/spf13/cobra"
)

func TestOCIOptionsCompletionUsesSelectedMetadata(t *testing.T) {
	previous := featureMetadataCompletion
	t.Cleanup(func() { featureMetadataCompletion = previous })
	featureMetadataCompletion = func(_ context.Context, ref string) (ocifeature.Metadata, error) {
		return (ocifeature.Lock{Metadata: `{"id":"tool","version":"1","options":{"version":{"type":"string","default":"latest","proposals":["stable"]},"enabled":{"type":"boolean","default":true}}}`}).Definition()
	}
	for _, route := range [][]string{{"box", "create"}, {"box", "set"}, {"box", "copy"}, {"image", "build"}} {
		t.Run(strings.Join(route, " "), func(t *testing.T) {
			cmd := findByPath(newRootCmd(), route...)
			cmd.SetContext(t.Context())
			if err := cmd.ParseFlags([]string{"--feature", "ghcr.io/example/tool:1"}); err != nil {
				t.Fatal(err)
			}
			fn, ok := cmd.GetFlagCompletionFunc("feature-option")
			if !ok {
				t.Fatal("missing completion")
			}
			values, directive := fn(cmd, nil, "tool.")
			if !slices.Contains(values, "tool.version=") || directive&cobra.ShellCompDirectiveNoSpace == 0 {
				t.Fatal(values, directive)
			}
			values, _ = fn(cmd, nil, "tool.enabled=")
			if !slices.Contains(values, "tool.enabled=true") || !slices.Contains(values, "tool.enabled=false") {
				t.Fatal(values)
			}
			values, _ = fn(cmd, nil, "tool.version=s")
			if !slices.Contains(values, "tool.version=stable") {
				t.Fatal(values)
			}
		})
	}
}

func TestOCICopyOptionCompletionUsesSourceSpaceLock(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previous := featureMetadataCompletion
	t.Cleanup(func() { featureMetadataCompletion = previous })
	featureMetadataCompletion = func(_ context.Context, ref string) (ocifeature.Metadata, error) {
		t.Fatalf("completion fetched %s instead of using the source lock", ref)
		return ocifeature.Metadata{}, nil
	}
	ref := "registry.example/features/tool:1"
	for _, space := range []string{"source", "destination"} {
		cfg := &agentbox.Config{
			Name: "same-name", Space: space, Image: "ubuntu:24.04", Features: []string{ref},
			FeatureLocks: []ocifeature.Lock{{
				Ref: ref, Digest: "sha256:" + strings.Repeat("a", 64),
				Layer:    ocifeature.Descriptor{Digest: "sha256:" + strings.Repeat("b", 64), MediaType: "application/vnd.devcontainers.layer.v1+tar", Size: 123},
				Metadata: `{"id":"tool","version":"1","options":{"version":{"type":"string","default":"` + space + `"}}}`,
			}},
		}
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
	}
	cmd := findByPath(newRootCmd(), "box", "copy")
	cmd.SetContext(t.Context())
	if err := cmd.ParseFlags([]string{"--space", "destination", "--from-space", "source"}); err != nil {
		t.Fatal(err)
	}
	fn, ok := cmd.GetFlagCompletionFunc("feature-option")
	if !ok {
		t.Fatal("missing completion")
	}
	values, _ := fn(cmd, []string{"same-name", "new-box"}, "tool.version=")
	if !slices.Equal(values, []string{"tool.version=source"}) {
		t.Fatalf("copy completion used destination metadata: %v", values)
	}
}
