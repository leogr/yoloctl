package agentbox

import (
	"context"
	"errors"
	"testing"

	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/openshell"
	"github.com/leogr/yoloctl/internal/sandbox"
)

func TestRetainedLegacyStartKeepsMigrationRefusalAndNativePrivilegedRoutes(t *testing.T) {
	legacy := &container.Details{Command: sandbox.EntrypointArgs("/workdir", false, "sleep", "infinity")}
	calls := 0
	refused := func(context.Context) error { calls++; return container.ErrLegacyOpenShellUnavailable }
	if err := CheckRetainedLegacyStart(t.Context(), legacy, refused); !errors.Is(err, container.ErrLegacyOpenShellUnavailable) || calls != 1 {
		t.Fatal("missing legacy bytes reached launch", err, calls)
	}
	allowed := func(context.Context) error { calls++; return nil }
	if err := CheckRetainedLegacyStart(t.Context(), legacy, allowed); err != nil || calls != 2 {
		t.Fatal("retained historical bytes refused", err, calls)
	}
	native := &container.Details{Command: []string{sandbox.EntrypointShimPath, openshell.HelperBinary, openshell.HelperCommand}}
	if err := CheckRetainedLegacyStart(t.Context(), native, refused); err != nil || calls != 2 {
		t.Fatal("native start depended on retired runtime", err)
	}
	privileged := *legacy
	yes := true
	privileged.Privileged = &yes
	if err := CheckRetainedLegacyStart(t.Context(), &privileged, refused); err != nil || calls != 2 {
		t.Fatal("privileged start depended on retired runtime", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := CheckRetainedLegacyStart(ctx, legacy, allowed); !errors.Is(err, context.Canceled) || calls != 2 {
		t.Fatal("cancelled check invoked runtime reader", err)
	}
	if err := CheckRetainedLegacyStart(t.Context(), legacy, nil); !errors.Is(err, container.ErrLegacyOpenShellUnavailable) {
		t.Fatal("missing legacy observation permitted start", err)
	}
}

func TestPrivilegedRetirementPreservesExtraAndSimilarMounts(t *testing.T) {
	c := privilegedConfig()
	extra := []Mount{{Source: "/workdir/project", Target: "/project", ReadOnly: true}, {Source: "/workdir/custom", Target: sandbox.BinaryDirContainerPath + "-other", ReadOnly: true}}
	c.Mounts = extra
	opts, err := buildRunOptions(c, "img", &EndpointBinary{SHA256: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range opts.Mounts {
		if mount.Source == sandbox.BinaryDirHostPath || mount.Source == sandbox.PolicyHostRegoPath {
			t.Fatal("privileged launch still requires retired VM runtime", mount)
		}
	}
	for _, wanted := range extra {
		found := false
		for _, mount := range opts.Mounts {
			if mount.Source == wanted.Source && mount.Destination == wanted.Target && mount.ReadOnly == wanted.ReadOnly {
				found = true
			}
		}
		if !found {
			t.Fatal("unrelated configured mount removed", wanted)
		}
	}
}
