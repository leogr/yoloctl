package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/imagejob"
)

type nativeCreationImagePreparationKey struct{}

// Only trusted creation/replacement callers select mandatory image retention.
// Recorded-generation Start/revive keeps its existing image-warming contract.
func withNativeCreationImagePreparation(ctx context.Context) context.Context {
	return context.WithValue(ctx, nativeCreationImagePreparationKey{}, true)
}

func nativeCreationImagePreparation(ctx context.Context) bool {
	marked, _ := ctx.Value(nativeCreationImagePreparationKey{}).(bool)
	return marked
}

// This snapshot is copied from a local-only controller read and remains bound
// to the original desired context. It supplies no lease or apply authority.
type nativeLocalImage struct {
	source   string
	platform string
	target   imagejob.LocalTarget
}

func (s *imageWarmState) prepareNativeCreationImage(ctx context.Context, p agentboximage.PreparedContext) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.call(ctx, controller.ImageRequest{Action: "preparing", Reference: p.DesiredRef, Box: s.box}); err != nil {
		return err
	}
	if err := s.prepareNativeCreationImageInputs(ctx, p); err != nil {
		return err
	}
	_, err := s.call(ctx, controller.ImageRequest{Action: "prepared"})
	return err
}

func (s *imageWarmState) prepareNativeCreationImageInputs(ctx context.Context, p agentboximage.PreparedContext) error {
	// Refresh explicitly chooses the existing build/pull path even when a
	// complete image is locally present. It never joins a local-retain job.
	if !s.force && !p.Recipe.Force {
		local, err := s.call(ctx, controller.ImageRequest{Action: "resolve-local", Reference: p.DesiredRef})
		if err != nil {
			return err // Failed/partial reads and missing content never become pulls.
		}
		if local.LocalMissing {
			if local.LocalTarget != nil {
				return errors.New("local image absence reply also supplied a target")
			}
		} else {
			if local.LocalTarget == nil {
				return errors.New("local image observation lacks its exact target")
			}
			recipe, err := imagejob.NewLocalRetain(p.DesiredRef, local.Platform, *local.LocalTarget)
			if err != nil {
				return err
			}
			result, err := s.call(ctx, controller.ImageRequest{Action: "acquire", Recipe: recipe})
			if err != nil {
				return err // Never retry an attempted retention as a pull or build.
			}
			entry := warmImage{context: p, pin: result.Pin, local: &nativeLocalImage{
				source: recipe.BaseReference, platform: recipe.Platform, target: *recipe.LocalTarget,
			}}
			if err := s.validateNativeLocalImage(ctx, entry); err != nil {
				return err
			}
			s.entries[p.DesiredRef] = entry
			return nil
		}
	}

	if len(p.Recipe.Dockerfile) == 0 && p.DesiredRef != agentboximage.RefAgentbox {
		// An absent custom base is a real admitted pull of that same source.
		// The legacy empty-recipe shortcut would substitute the managed base.
		source, err := imagejob.CanonicalReference(p.DesiredRef)
		if err != nil {
			return err
		}
		base, err := imagejob.CanonicalReference(p.BaseRef)
		if err != nil || base != source {
			return errors.New("base-only native preparation differs from its desired source")
		}
		pull := agentboximage.PreparedContext{DesiredRef: p.DesiredRef, BaseRef: source,
			Recipe: imagejob.Recipe{Builder: "pull/1", Dockerfile: []byte("FROM " + source), Force: s.force || p.Recipe.Force}}
		if err := s.prepareImages(ctx, pull); err != nil {
			return err
		}
		entry := s.entries[p.DesiredRef]
		entry.context = p // Consumption compares the caller's original context.
		s.entries[p.DesiredRef] = entry
		return nil
	}
	return s.prepareImages(ctx, p)
}

func (s *imageWarmState) validateNativeLocalImage(ctx context.Context, entry warmImage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	local := entry.local
	if s.force || entry.context.Recipe.Force {
		return errors.New("explicit image refresh cannot consume local retention")
	}
	if local == nil || entry.pin.JobID == "" || entry.pin.Reference == "" || entry.pin.Lease == "" || entry.pin.Digest != local.target.Digest {
		return errors.New("native local image lacks its owned retained target")
	}
	source, err := imagejob.CanonicalReference(entry.context.DesiredRef)
	if err != nil || source != local.source {
		return errors.New("native local image source differs from its original desired context")
	}
	if _, err := s.call(ctx, controller.ImageRequest{Action: "validate", JobID: entry.pin.JobID}); err != nil {
		return err
	}
	current, err := s.call(ctx, controller.ImageRequest{Action: "resolve-local", Reference: local.source})
	if err != nil {
		return err
	}
	if current.LocalMissing || current.LocalTarget == nil {
		return fmt.Errorf("local image %q changed after retention; run the command again", local.source)
	}
	observed, err := imagejob.NewLocalRetain(local.source, current.Platform, *current.LocalTarget)
	if err != nil {
		return err
	}
	if *current.LocalTarget != local.target || observed.Platform != local.platform {
		return fmt.Errorf("local image %q changed after retention; run the command again", local.source)
	}
	return nil
}
