package agentbox

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/openshell"
)

func TestNativeCreationRequiresImmutablePreparedTarget(t *testing.T) {
	for _, image := range []string{"", "local/image:latest", "name@sha256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("g", 64), "sha256:a"} {
		if nativeCreationImage(image) == nil {
			t.Fatalf("mutable/malformed native image accepted: %q", image)
		}
	}
	if err := nativeCreationImage("sha256:" + strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCreationSelectionIsPrivateAndInvalidQualificationFailsBeforeEffects(t *testing.T) {
	rego := []byte("reviewed rego")
	a := (LiveApplier{}).WithNativeCreationRelease(openshell.QualifiedRelease{}, rego)
	rego[0] = '!'
	if string(a.nativeCreation.rego) != "reviewed rego" {
		t.Fatal("release selection retained mutable caller bytes")
	}
	c := &Config{Name: "fixture", Space: "trial", Workdir: NewEphemeralWorkdir("fixture")}
	if _, err := a.PreflightNativeCreate(t.Context(), c, nil); err == nil {
		t.Fatal("missing client/qualification was accepted")
	}
	if err := a.PreflightRecreate(t.Context(), c, nil); err == nil {
		t.Fatal("explicit invalid native selection fell back to legacy preflight")
	}
	// The existing privileged route never adopts this ordinary release.
	c.Privileged = true
	if err := a.PreflightRecreate(t.Context(), c, nil); err != nil {
		t.Fatal("ordinary qualification changed privileged route", err)
	}
	c.Privileged = false
	if err := (LiveApplier{}).PreflightRecreate(t.Context(), c, nil); err != nil {
		t.Fatal("unbound legacy preparation changed", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a.Client, _ = nativeImageClientFixture(t, "[]", 97)
	if _, err := a.PreflightNativeCreate(ctx, c, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled preflight continued", err)
	}
}
