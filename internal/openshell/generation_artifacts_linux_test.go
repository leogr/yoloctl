package openshell

import (
	"context"
	"errors"
	"testing"
)

func TestNativeGenerationArtifactPreflightRequiresAdmittedIdentityAndUsableCheckpoint(t *testing.T) {
	var missing *NativeGeneration
	if err := missing.QualifyNativeArtifacts(t.Context()); err == nil {
		t.Fatal("missing checkpoint qualified artifacts")
	}
	_, g := generationFixture(t)
	if err := g.QualifyNativeArtifacts(t.Context()); err == nil {
		t.Fatal("ordinary fixture gained native artifact installation authority")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := g.QualifyNativeArtifacts(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled artifact preflight lost known refusal", err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g.QualifyNativeArtifacts(t.Context()); err == nil {
		t.Fatal("closed checkpoint qualified artifacts")
	}
}
