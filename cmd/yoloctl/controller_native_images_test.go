package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/imagejob"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

type nativeWorkerImageFixture struct {
	t            *testing.T
	state        *imageWarmState
	target       imagejob.LocalTarget
	platform     string
	missing      bool
	localErr     error
	acquireErr   error
	validateErr  error
	afterAcquire func()
	actions      []string
	recipes      []imagejob.Recipe
	publications []string
	executing    bool
	allowBuild   bool
}

func newNativeWorkerImageFixture(t *testing.T) *nativeWorkerImageFixture {
	t.Helper()
	f := &nativeWorkerImageFixture{t: t, platform: "linux/amd64", target: imagejob.LocalTarget{
		Digest: "sha256:" + strings.Repeat("a", 64), Size: 1234, MediaType: ocispec.MediaTypeImageIndex,
	}}
	f.state = &imageWarmState{box: "fixture", entries: map[string]warmImage{}, call: f.call}
	return f
}

func (f *nativeWorkerImageFixture) call(ctx context.Context, r controller.ImageRequest) (controller.ImageReply, error) {
	f.t.Helper()
	if err := ctx.Err(); err != nil {
		return controller.ImageReply{}, err
	}
	f.actions = append(f.actions, r.Action)
	switch r.Action {
	case "preparing", "prepared":
		if f.executing {
			f.t.Fatal("image preparation or progress drain ran inside mutation guards")
		}
		return controller.ImageReply{}, nil
	case "resolve-local":
		if r.Remote {
			f.t.Fatal("local lookup requested a remote resolver")
		}
		if f.localErr != nil {
			return controller.ImageReply{}, f.localErr
		}
		if f.missing {
			return controller.ImageReply{LocalMissing: true}, nil
		}
		target := f.target
		return controller.ImageReply{LocalTarget: &target, Platform: f.platform}, nil
	case "acquire":
		if f.executing {
			f.t.Fatal("image acquisition ran inside mutation guards")
		}
		if _, err := r.Recipe.Identity(); err != nil {
			f.t.Fatal("worker submitted an invalid recipe", err)
		}
		if !f.allowBuild && r.Recipe.Builder != imagejob.LocalRetainBuilder {
			f.t.Fatal("cached native image triggered a build or pull", r.Recipe.Builder)
		}
		f.recipes = append(f.recipes, imagejob.Copy(r.Recipe))
		if f.acquireErr != nil {
			return controller.ImageReply{}, f.acquireErr
		}
		if f.afterAcquire != nil {
			f.afterAcquire()
		}
		id := strings.Repeat("b", 64)
		return controller.ImageReply{Pin: controller.PinnedImage{
			JobID: id, Reference: "yoloctl/prepared/" + id, Digest: f.target.Digest, Lease: "yoloctl-image-" + id,
		}}, nil
	case "validate":
		if r.JobID != strings.Repeat("b", 64) {
			f.t.Fatal("worker validated another execution's pin", r.JobID)
		}
		return controller.ImageReply{}, f.validateErr
	case "resolve":
		if !f.allowBuild {
			f.t.Fatal("cached native image invoked the registry-capable resolver")
		}
		if f.executing {
			f.t.Fatal("cold image resolution ran inside mutation guards")
		}
		return controller.ImageReply{Digest: f.target.Digest, Platform: f.platform}, nil
	case "publish":
		if !f.allowBuild {
			f.t.Fatal("local retention retagged its public source", r.Reference)
		}
		if !f.executing {
			f.t.Fatal("new image published before apply")
		}
		f.publications = append(f.publications, r.Reference)
		return controller.ImageReply{}, nil
	default:
		f.t.Fatalf("unexpected native worker image action %q", r.Action)
		return controller.ImageReply{}, errors.New("unexpected image action")
	}
}

func TestNativeWorkerRetainsCachedComposedAndCustomImagesWithoutPublicPublication(t *testing.T) {
	for _, test := range []struct {
		name       string
		base       string
		candidates []string
	}{
		{"composed", "local/custom:one", []string{"go"}},
		{"custom-base", "local/custom:one", nil},
		{"managed-base", agentboximage.RefAgentbox, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newNativeWorkerImageFixture(t)
			p, err := agentboximage.PrepareComposed(t.Context(), test.base, test.candidates, agentboximage.Options{})
			if err != nil {
				t.Fatal(err)
			}
			passes, consumed := 0, 0
			err = runPreparedControllerWorker(t.Context(), controller.WorkerInput{}, f.state, func(ctx context.Context, _ controller.WorkerInput) error {
				f.executing = true
				defer func() { f.executing = false }()
				passes++
				ctx = withNativeCreationImagePreparation(ctx)
				if err := requireWarmImage(ctx, nil, p); err != nil {
					return err
				}
				// Actual prepared-image adapter, supplied by the real worker driver.
				// Two consumptions model replacement preflight followed by Create.
				for range 2 {
					ref, err := agentboximage.RequirePreparedImage(ctx, nil, test.base, test.candidates, agentboximage.Options{})
					if err != nil || ref != f.target.Digest {
						t.Fatal("native consumer lost its retained OCI target", ref, err)
					}
					consumed++
				}
				return nil
			})
			if err != nil || passes != 2 || consumed != 2 || len(f.recipes) != 1 || len(f.publications) != 0 {
				t.Fatal("local worker route rebuilt, published or lost its pin", err, passes, consumed, f.actions)
			}
			recipe := f.recipes[0]
			source, _ := imagejob.CanonicalReference(p.DesiredRef)
			if recipe.Builder != imagejob.LocalRetainBuilder || recipe.BaseReference != source || recipe.LocalTarget == nil || *recipe.LocalTarget != f.target || recipe.Platform != f.platform {
				t.Fatal("worker retained another source or descriptor", recipe)
			}
			if !slices.Equal(f.actions[:5], []string{"preparing", "resolve-local", "acquire", "validate", "resolve-local"}) || f.actions[5] != "prepared" {
				t.Fatal("retention lacked outside-lock preparation and validation", f.actions)
			}
		})
	}
}

func TestNativeWorkerOnlyGenuineCustomBaseAbsenceChoosesAdmittedColdPull(t *testing.T) {
	for _, mode := range []string{"absent", "missing-graph", "failed-read", "lost-retain-reply"} {
		t.Run(mode, func(t *testing.T) {
			f := newNativeWorkerImageFixture(t)
			cause := errors.New(mode)
			switch mode {
			case "absent":
				f.missing, f.allowBuild = true, true
			case "missing-graph", "failed-read":
				f.localErr = cause
			case "lost-retain-reply":
				f.acquireErr = &controller.EffectsUncertain{Cause: cause}
			}
			base := "local/custom:one"
			p, err := agentboximage.PrepareComposed(t.Context(), base, nil, agentboximage.Options{})
			if err != nil {
				t.Fatal(err)
			}
			ctx := controller.WithEffectRecorder(t.Context(), &imagePublicationRecorder{})
			passes := 0
			err = runPreparedControllerWorker(ctx, controller.WorkerInput{}, f.state, func(ctx context.Context, _ controller.WorkerInput) error {
				f.executing = true
				defer func() { f.executing = false }()
				passes++
				ctx = withNativeCreationImagePreparation(ctx)
				if err := requireWarmImage(ctx, nil, p); err != nil {
					return err
				}
				ref, err := agentboximage.RequirePreparedImage(ctx, nil, base, nil, agentboximage.Options{})
				if err == nil && ref != f.target.Digest {
					t.Fatal("cold custom consumer used another image", ref)
				}
				return err
			})
			if mode != "absent" {
				if !errors.Is(err, cause) || passes != 1 || len(f.publications) != 0 {
					t.Fatal("failed local preparation became another action", err, passes, f.actions)
				}
				for _, recipe := range f.recipes {
					if recipe.Builder != imagejob.LocalRetainBuilder {
						t.Fatal("failed retention retried as a build or pull", recipe.Builder)
					}
				}
				return
			}
			if err != nil || passes != 2 || len(f.recipes) != 1 || f.recipes[0].Builder != "pull/1" || f.recipes[0].BaseReference != "docker.io/local/custom:one" || !slices.Equal(f.publications, []string{base}) {
				t.Fatal("cold custom base was not its own admitted pull", err, passes, f.recipes, f.publications)
			}
			if !sameContext(f.state.entries[p.DesiredRef].context, p) {
				t.Fatal("cold preparation replaced the caller's desired context")
			}
		})
	}
}

func TestNativeWorkerForceKeepsIntentionalBuildAndPullPaths(t *testing.T) {
	for _, candidates := range [][]string{nil, {"go"}} {
		t.Run(map[bool]string{true: "custom-pull", false: "composed-build"}[len(candidates) == 0], func(t *testing.T) {
			f := newNativeWorkerImageFixture(t)
			f.allowBuild, f.state.force = true, true
			base := "local/custom:one"
			p, err := agentboximage.PrepareComposed(t.Context(), base, candidates, agentboximage.Options{Force: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx := controller.WithEffectRecorder(t.Context(), &imagePublicationRecorder{})
			err = runPreparedControllerWorker(ctx, controller.WorkerInput{}, f.state, func(ctx context.Context, _ controller.WorkerInput) error {
				f.executing = true
				defer func() { f.executing = false }()
				ctx = withNativeCreationImagePreparation(ctx)
				if err := requireWarmImage(ctx, nil, p); err != nil {
					return err
				}
				_, err := agentboximage.RequirePreparedImage(ctx, nil, base, candidates, agentboximage.Options{})
				return err
			})
			if err != nil || len(f.recipes) != 1 || !f.recipes[0].Force || f.recipes[0].Builder == imagejob.LocalRetainBuilder || len(f.publications) != 1 || slices.Contains(f.actions, "resolve-local") {
				t.Fatal("explicit refresh was satisfied by cache retention", err, f.actions, f.recipes)
			}
		})
	}
}

func TestNativeWorkerRejectsSourceDriftAndLostPinsWithoutReselection(t *testing.T) {
	for _, change := range []string{"digest", "size", "media-type", "platform", "source-missing", "pin-ownership"} {
		t.Run(change, func(t *testing.T) {
			f := newNativeWorkerImageFixture(t)
			base := "local/custom:one"
			p, _ := agentboximage.PrepareComposed(t.Context(), base, nil, agentboximage.Options{})
			passes := 0
			err := runPreparedControllerWorker(t.Context(), controller.WorkerInput{}, f.state, func(ctx context.Context, _ controller.WorkerInput) error {
				f.executing = true
				defer func() { f.executing = false }()
				passes++
				ctx = withNativeCreationImagePreparation(ctx)
				if err := requireWarmImage(ctx, nil, p); err != nil {
					return err
				}
				switch change {
				case "digest":
					f.target.Digest = "sha256:" + strings.Repeat("c", 64)
				case "size":
					f.target.Size++
				case "media-type":
					f.target.MediaType = ocispec.MediaTypeImageManifest
				case "platform":
					f.platform = "linux/arm64"
				case "source-missing":
					f.missing = true
				case "pin-ownership":
					f.validateErr = errors.New("execution no longer owns image pin")
				}
				ref, err := agentboximage.RequirePreparedImage(ctx, nil, base, nil, agentboximage.Options{})
				if err == nil || ref != "" {
					t.Fatal("changed or unowned retained image was consumed", ref, err)
				}
				return err
			})
			if err == nil || passes != 2 || len(f.recipes) != 1 || len(f.publications) != 0 {
				t.Fatal("failed current validation reselected or republished source", err, passes, f.actions)
			}
		})
	}
}

func TestNativeWorkerCancellationAfterAcquireStopsPreparationSuffix(t *testing.T) {
	f := newNativeWorkerImageFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	f.afterAcquire = cancel
	p, _ := agentboximage.PrepareComposed(t.Context(), "local/custom:one", nil, agentboximage.Options{})
	err := runPreparedControllerWorker(ctx, controller.WorkerInput{}, f.state, func(ctx context.Context, _ controller.WorkerInput) error {
		return requireWarmImage(withNativeCreationImagePreparation(ctx), nil, p)
	})
	if !errors.Is(err, context.Canceled) || len(f.recipes) != 1 || len(f.state.entries) != 0 || slices.Contains(f.actions, "prepared") || slices.Contains(f.actions, "validate") {
		t.Fatal("canceled retention continued or invented a consumable pin", err, f.actions)
	}
}

func TestNativeWorkerReentryKeepsRejectedPinWithoutPreparingAgain(t *testing.T) {
	f := newNativeWorkerImageFixture(t)
	p, _ := agentboximage.PrepareComposed(t.Context(), "local/custom:one", nil, agentboximage.Options{})
	cause := errors.New("retained lease is unavailable")
	passes := 0
	err := runPreparedControllerWorker(t.Context(), controller.WorkerInput{}, f.state, func(ctx context.Context, _ controller.WorkerInput) error {
		passes++
		if passes == 2 {
			f.validateErr = cause
		}
		return requireWarmImage(withNativeCreationImagePreparation(ctx), nil, p)
	})
	if !errors.Is(err, cause) || passes != 2 || len(f.recipes) != 1 || len(f.state.entries) != 1 {
		t.Fatal("failed pin validation deleted/reselected retained input", err, passes, f.actions)
	}
}

func TestNativeWorkerRejectsNonTargetLocalDescriptorsBeforeAcquisition(t *testing.T) {
	for _, mediaType := range []string{ocispec.MediaTypeImageConfig, ocispec.MediaTypeImageLayer, ""} {
		t.Run(mediaType, func(t *testing.T) {
			f := newNativeWorkerImageFixture(t)
			f.target.MediaType = mediaType
			p, _ := agentboximage.PrepareComposed(t.Context(), "local/custom:one", nil, agentboximage.Options{})
			err := runPreparedControllerWorker(t.Context(), controller.WorkerInput{}, f.state, func(ctx context.Context, _ controller.WorkerInput) error {
				return requireWarmImage(withNativeCreationImagePreparation(ctx), nil, p)
			})
			if err == nil || len(f.recipes) != 0 || slices.Contains(f.actions, "resolve") {
				t.Fatal("configuration/layer was retained as an image target", err, f.actions)
			}
		})
	}
}

func TestNewComposedPublicationDoesNotRepublishLocallyRetainedManagedBase(t *testing.T) {
	f := newNativeWorkerImageFixture(t)
	f.allowBuild, f.executing = true, true
	p := agentboximage.PreparedContext{DesiredRef: "local/composed:one", BaseRef: agentboximage.RefAgentbox}
	f.state.entries[agentboximage.RefAgentbox] = warmImage{local: &nativeLocalImage{source: agentboximage.RefAgentbox}}
	f.state.entries[p.DesiredRef] = warmImage{context: p, pin: controller.PinnedImage{JobID: "composed-job", Digest: f.target.Digest}}
	ctx := controller.WithEffectRecorder(t.Context(), &imagePublicationRecorder{})
	ref, handled, err := f.state.consume(ctx, p)
	if err != nil || !handled || ref != f.target.Digest || !slices.Equal(f.publications, []string{p.DesiredRef}) {
		t.Fatal("new composition republished a retained public source", ref, handled, err, f.publications)
	}
}

func TestNativeImageMarkerIsRequiredOnlyForNewCreationAndCannotConsumeUnleasedImage(t *testing.T) {
	p, _ := agentboximage.PrepareComposed(t.Context(), "local/custom:one", nil, agentboximage.Options{})
	f := newNativeWorkerImageFixture(t)
	ctx := context.WithValue(t.Context(), warmKey{}, f.state)
	if err := requireWarmImage(ctx, nil, p); err != nil || len(f.actions) != 0 {
		t.Fatal("unmarked recorded-generation path acquired new image requirements", err, f.actions)
	}
	marked := withNativeCreationImagePreparation(ctx)
	var needed *imageWarmNeeded
	if err := requireWarmImage(marked, nil, p); !errors.As(err, &needed) || !needed.retain || len(f.actions) != 0 {
		t.Fatal("marked cache miss failed to unwind before image effects", err, f.actions)
	}
	if _, handled, err := f.state.consume(marked, p); !handled || err == nil {
		t.Fatal("marked consumption fell back to an unleased image", handled, err)
	}
	if err := requireWarmImage(withNativeCreationImagePreparation(t.Context()), nil, p); err == nil {
		t.Fatal("native image preparation accepted an absent controller worker")
	}
	f.state.entries[p.DesiredRef] = warmImage{context: p,
		pin:   controller.PinnedImage{JobID: "unleased", Reference: "yoloctl/prepared/unleased", Digest: f.target.Digest},
		local: &nativeLocalImage{source: "docker.io/local/custom:one", platform: f.platform, target: f.target}}
	if ref, _, err := f.state.consume(marked, p); err == nil || ref != "" {
		t.Fatal("local metadata replaced retained lease ownership", ref, err)
	}
	delete(f.state.entries, p.DesiredRef)
	f.state.applied = true
	if err := requireWarmImage(marked, nil, p); err == nil || imageWarmPending(err) {
		t.Fatal("post-apply image miss may replay the handler", err)
	}
}
