package openshell

import (
	"context"
	"errors"
)

// QualifyNativeArtifacts verifies or prepares the recorded immutable runtime
// pair and separate static helper BEFORE an admitted caller destroys a session
// or stops a supported container. It never selects credentials or starts a
// workload. The original private Rego stays inside this checkpoint.
func (g *NativeGeneration) QualifyNativeArtifacts(ctx context.Context) error {
	if err := g.usable(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := nativeGenerationIdentity(g.current, g.release); err != nil {
		return err
	}
	if err := g.release.checkExistingBoxHelper(g.current.Launch.Binding.Box); err != nil {
		return err
	}
	if err := g.verifyRoot(); err != nil {
		return err
	}
	if err := verifyGenerationSelection(g.root, g.current, g.owners); err != nil {
		return err
	}
	if sha256Hex(g.rego) != g.release.document.Pins.RegoSHA256 {
		return errors.New("generation artifact preflight has mismatched recorded Rego")
	}
	// The existing box's recorded pair is normally already installed and is
	// only verified here; an earlier build's release is not refused.
	if _, err := installNativeRelease(ctx, g.release, g.rego, g.current.Launch.Binding.Box); err != nil {
		return err
	}
	if _, err := installNativeHelper(ctx, g.release, g.current.Launch.Binding.Box); err != nil {
		return err
	}
	if err := g.verifyRoot(); err != nil {
		return err
	}
	return verifyGenerationSelection(g.root, g.current, g.owners)
}
