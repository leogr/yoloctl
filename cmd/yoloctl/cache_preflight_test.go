package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/spf13/cobra"
)

func TestCacheOverlapRefusesBeforeRecreateEffects(t *testing.T) {
	c := &agentbox.Config{Name: "coder", Space: "default", Mounts: []agentbox.Mount{{Source: "/tmp/project", Target: "/home/sandbox/x/../.cache"}}}
	inner := &fakeManagedInner{}
	a := managedApplier{inner: inner,
		prepareRecreate: func(context.Context) error { t.Fatal("prepared replacement before refusing overlap"); return nil },
		cleanup: func(context.Context, *agentbox.Config) (bool, error) {
			t.Fatal("stopped holder before refusing overlap")
			return false, nil
		},
	}
	if err := a.Recreate(t.Context(), c, nil); err == nil || !strings.Contains(err.Error(), "overlaps the box cache") {
		t.Fatal("missing early overlap refusal", err)
	}
	if len(inner.calls) != 0 {
		t.Fatal("container mutation before refusal", inner.calls)
	}
}

func TestCacheOverlapRefusesBeforeImageAndRuntimePreparation(t *testing.T) {
	c := &agentbox.Config{Name: "coder", Mounts: []agentbox.Mount{{Source: "/tmp/project", Target: "/home//sandbox/.cache"}}}
	state := &imageWarmState{enabled: true, imagesEnabled: true}
	ctx := context.WithValue(t.Context(), warmKey{}, state)
	ctx = context.WithValue(ctx, warmGateKey{}, func() error { t.Fatal("image effects admitted before overlap check"); return nil })
	err := preflightAgentboxRuntimeQualified(ctx, container.New("default"), c, true, func(context.Context, *agentbox.Config) (bool, error) {
		t.Fatal("native preparation before overlap check")
		return false, nil
	})
	if err == nil || !strings.Contains(err.Error(), "overlaps the box cache") {
		t.Fatal("wrong early preflight result", err)
	}
}

func TestCacheOverlapPersistedRecreateEntryRefusesWithoutChangingDeclaration(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path := seedSetBox(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	before = append(before, []byte("\nmounts:\n  - source: /tmp/project\n    target: /home/sandbox/x/../.cache\n")...)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}
	// A legacy declaration remains readable for stop/destroy recovery.
	if _, err := agentbox.Load("default", "coder"); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	err = recreateAgentbox(cmd, "default", "coder", true, true, true, nil)
	if err == nil || !strings.Contains(err.Error(), "overlaps the box cache") {
		t.Fatal("persisted overlap reached later recreate preparation", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("recreate changed declaration", err)
	}
}
