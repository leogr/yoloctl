package main

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/imagejob"
)

type imagePublicationRecorder struct {
	writes []controller.Resource
	err    error
}

func (r *imagePublicationRecorder) ExpandEffects(_ context.Context, writes []controller.Resource) error {
	r.writes = append(r.writes, writes...)
	return r.err
}
func (*imagePublicationRecorder) RecordDocument(context.Context, controller.DocumentIntent) error {
	return nil
}
func (*imagePublicationRecorder) CompleteEffects(context.Context, int) error { return nil }

func TestConsumptionPublishesNewManagedBaseWithComposedImage(t *testing.T) {
	for _, mode := range []string{"consume", "manual", "held-base"} {
		t.Run(mode, func(t *testing.T) {
			p := agentboximage.PreparedContext{DesiredRef: "local/composed:one", BaseRef: agentboximage.RefAgentbox}
			base := controller.PinnedImage{JobID: "base-job", Digest: "sha256:" + strings.Repeat("a", 64)}
			composed := controller.PinnedImage{JobID: "composed-job", Digest: "sha256:" + strings.Repeat("b", 64)}
			writes := []controller.Resource{{Kind: controller.ResourceImages, Name: agentboximage.RefAgentbox}, {Kind: controller.ResourceImages, Name: p.DesiredRef}}
			recorder := &imagePublicationRecorder{}
			if mode == "held-base" {
				recorder.err = errors.New("base publication held")
			}
			var published []controller.ImageRequest
			state := &imageWarmState{entries: map[string]warmImage{
				agentboximage.RefAgentbox: {pin: base}, p.DesiredRef: {context: p, pin: composed},
			}, call: func(_ context.Context, request controller.ImageRequest) (controller.ImageReply, error) {
				if request.Action == "check-desired" {
					return controller.ImageReply{}, nil
				}
				if request.Action != "publish" {
					t.Fatalf("unexpected action %s", request.Action)
				}
				if !slices.Equal(recorder.writes, writes) {
					t.Fatalf("publication before complete scope: %v", recorder.writes)
				}
				published = append(published, request)
				return controller.ImageReply{}, nil
			}}
			ctx := controller.WithEffectRecorder(t.Context(), recorder)
			var err error
			if mode == "manual" {
				ctx = context.WithValue(ctx, warmKey{}, state)
				_, err = warmManualImage(ctx, p)
			} else {
				var ref string
				ref, _, err = state.consume(ctx, p)
				if err == nil && ref != composed.Digest {
					t.Fatalf("consumed %s", ref)
				}
			}
			if mode == "held-base" {
				if !errors.Is(err, recorder.err) || len(published) != 0 {
					t.Fatalf("held base was published: %v %v", published, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(published) != 2 || published[0].Reference != agentboximage.RefAgentbox || published[0].JobID != base.JobID || published[1].Reference != p.DesiredRef || published[1].JobID != composed.JobID {
				t.Fatalf("wrong public references or retained outputs: %v", published)
			}
		})
	}
}

func TestManualPreparationDoesNotPublishBeforeItsOwnApply(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	pin := controller.PinnedImage{JobID: strings.Repeat("b", 64), Reference: "yoloctl/prepared/" + strings.Repeat("b", 64), Digest: digest, Lease: "retained"}
	var acquired, published, gates int
	state := &imageWarmState{entries: map[string]warmImage{}, call: func(ctx context.Context, r controller.ImageRequest) (controller.ImageReply, error) {
		switch r.Action {
		case "preparing", "prepared":
			return controller.ImageReply{}, nil
		case "check-desired":
			return controller.ImageReply{}, nil
		case "resolve":
			return controller.ImageReply{Digest: digest, Platform: "linux/amd64"}, nil
		case "acquire":
			acquired++
			return controller.ImageReply{Pin: pin}, nil
		case "publish":
			published++
			if gates != 1 {
				t.Fatal("publication before apply gate")
			}
			return controller.ImageReply{}, nil
		default:
			return controller.ImageReply{}, errors.New("unexpected image action")
		}
	}}
	ctx := context.WithValue(t.Context(), warmKey{}, state)
	ctx = context.WithValue(ctx, warmGateKey{}, func() error { gates++; return nil })
	p := agentboximage.PreparedContext{DesiredRef: "local/manual:one", BaseRef: "ubuntu:24.04", Recipe: imagejob.Recipe{Builder: "manual/1", Dockerfile: []byte("FROM ubuntu:24.04\nRUN true")}}
	handled, err := warmManualImage(ctx, p)
	if !handled || !imageWarmPending(err) || acquired != 0 || published != 0 || gates != 0 {
		t.Fatal("prepare miss had effects", err)
	}
	if err := state.prepare(ctx, p); err != nil {
		t.Fatal(err)
	}
	if acquired != 1 || published != 0 || gates != 0 {
		t.Fatal("warming published")
	}
	if _, err := warmManualImage(ctx, p); err != nil {
		t.Fatal(err)
	}
	if acquired != 1 || published != 1 || gates != 1 {
		t.Fatal("manual command rebuilt or lacked publication")
	}
}
func TestAppliedRequestNeverRestartsItsHandlerForAMissingImage(t *testing.T) {
	state := &imageWarmState{applied: true, entries: map[string]warmImage{}}
	ctx := context.WithValue(t.Context(), warmKey{}, state)
	_, err := warmManualImage(ctx, agentboximage.PreparedContext{DesiredRef: "local/manual:one"})
	if err == nil || imageWarmPending(err) {
		t.Fatal("post-apply miss may replay handler", err)
	}
}

func TestPreparedConsumptionPassesImmutableTargetDigestToNerdctl(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	p := agentboximage.PreparedContext{DesiredRef: "local/desired:one", BaseRef: "ubuntu:24.04"}
	state := &imageWarmState{entries: map[string]warmImage{p.DesiredRef: {context: p, pin: controller.PinnedImage{JobID: "job", Reference: "yoloctl/prepared/job", Digest: digest}}}, call: func(context.Context, controller.ImageRequest) (controller.ImageReply, error) {
		return controller.ImageReply{}, nil
	}}
	ref, handled, err := state.consume(t.Context(), p)
	if err != nil || !handled || ref != digest {
		t.Fatal("consumption needs a mutable or unexported alias", ref, handled, err)
	}
}

func TestPrepareBindsOneRegistryIndexForMultiPlatformExternalSource(t *testing.T) {
	local := "sha256:" + strings.Repeat("a", 64)
	index := "sha256:" + strings.Repeat("b", 64)
	resolves := 0
	var got imagejob.Recipe
	state := &imageWarmState{entries: map[string]warmImage{}, call: func(_ context.Context, r controller.ImageRequest) (controller.ImageReply, error) {
		switch r.Action {
		case "preparing", "prepared":
			return controller.ImageReply{}, nil
		case "resolve":
			resolves++
			d := local
			if r.Remote {
				d = index
			}
			return controller.ImageReply{Digest: d, Platform: "linux/amd64"}, nil
		case "acquire":
			got = r.Recipe
			return controller.ImageReply{Pin: controller.PinnedImage{JobID: "test", Digest: index}}, nil
		default:
			return controller.ImageReply{}, errors.New("unexpected action")
		}
	}}
	p := agentboximage.PreparedContext{DesiredRef: "local/multi:one", BaseRef: "ubuntu:24.04", Recipe: imagejob.Recipe{Builder: "manual/1", Dockerfile: []byte("FROM --platform=linux/amd64 ubuntu:24.04 AS native\nFROM --platform=linux/arm64 ubuntu:24.04 AS foreign\n")}}
	if err := state.prepare(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if resolves != 2 || len(got.Images) != 2 {
		t.Fatal("dependencies were not resolved once per reference", resolves, got.Images)
	}
	for _, input := range got.Images {
		if input.Digest != index {
			t.Fatal("native-only local manifest used for multiple platforms", input)
		}
	}
}

func TestManagedBasePreparationResolvesSyntaxFrontendSeparately(t *testing.T) {
	baseDigest := "sha256:" + strings.Repeat("a", 64)
	frontendDigest := "sha256:" + strings.Repeat("b", 64)
	p, err := agentboximage.PrepareManagedBase(false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(p.Recipe.Dockerfile), "# syntax=docker/dockerfile:1") {
		t.Fatal("regression fixture lost the production syntax directive")
	}
	var got imagejob.Recipe
	state := &imageWarmState{entries: map[string]warmImage{}, call: func(_ context.Context, r controller.ImageRequest) (controller.ImageReply, error) {
		switch r.Action {
		case "preparing", "prepared":
			return controller.ImageReply{}, nil
		case "resolve":
			d := baseDigest
			if r.Reference == "docker.io/docker/dockerfile:1" {
				d = frontendDigest
			}
			return controller.ImageReply{Digest: d, Platform: "linux/amd64"}, nil
		case "acquire":
			got = r.Recipe
			return controller.ImageReply{Pin: controller.PinnedImage{JobID: "managed", Digest: baseDigest}}, nil
		default:
			return controller.ImageReply{}, errors.New("unexpected image action")
		}
	}}
	if err := state.prepare(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, input := range got.Images {
		if input.Reference == "docker.io/docker/dockerfile:1" {
			found = true
			if input.Digest != frontendDigest {
				t.Fatal("frontend inherited base digest", input)
			}
		}
	}
	if !found {
		t.Fatal("production managed frontend missing from bound inputs", got.Images)
	}
	before, _ := got.Identity()
	for i := range got.Images {
		if got.Images[i].Reference == "docker.io/docker/dockerfile:1" {
			got.Images[i].Digest = baseDigest
		}
	}
	after, _ := got.Identity()
	if before == after {
		t.Fatal("frontend content absent from job identity")
	}
}

func TestImagePreparationAnnouncesBeforeAnyResolution(t *testing.T) {
	var actions []string
	state := &imageWarmState{box: "worker", entries: map[string]warmImage{}, call: func(_ context.Context, r controller.ImageRequest) (controller.ImageReply, error) {
		actions = append(actions, r.Action)
		if len(actions) == 1 && (r.Action != "preparing" || r.Box != "worker" || r.Reference != "local/manual:one") {
			t.Fatalf("no initial preparation notice: %+v", r)
		}
		if r.Action == "resolve" {
			return controller.ImageReply{}, errors.New("resolution blocked")
		}
		return controller.ImageReply{}, nil
	}}
	err := state.prepare(t.Context(), agentboximage.PreparedContext{DesiredRef: "local/manual:one", BaseRef: "ubuntu:24.04", Recipe: imagejob.Recipe{Builder: "manual/1", Dockerfile: []byte("FROM ubuntu:24.04")}})
	if err == nil || len(actions) != 2 || actions[0] != "preparing" || actions[1] != "resolve" {
		t.Fatal(actions, err)
	}
}

func TestNestedBasePreparationRestoresComposedProgressBeforeSolve(t *testing.T) {
	previous := preparedBasePresent
	preparedBasePresent = func(context.Context, string) (bool, error) { return false, nil }
	t.Cleanup(func() { preparedBasePresent = previous })
	const composed = "local/composed:fixture"
	var announced string
	var notices []string
	acquisitions, finished := 0, 0
	state := &imageWarmState{box: "worker", entries: map[string]warmImage{}, call: func(_ context.Context, r controller.ImageRequest) (controller.ImageReply, error) {
		switch r.Action {
		case "preparing":
			announced = r.Reference
			notices = append(notices, announced)
		case "prepared":
			finished++
		case "resolve":
			return controller.ImageReply{Digest: "sha256:" + strings.Repeat("a", 64), Platform: "linux/amd64"}, nil
		case "acquire":
			acquisitions++
			if r.Recipe.Builder == "composed-fixture" && announced != composed {
				t.Fatalf("composed solve still reports completed base %s", announced)
			}
			return controller.ImageReply{Pin: controller.PinnedImage{JobID: strings.Repeat("b", 64), Reference: "yoloctl/prepared/" + strings.Repeat("b", 64), Digest: "sha256:" + strings.Repeat("a", 64)}}, nil
		default:
			t.Fatalf("unexpected action %q", r.Action)
		}
		return controller.ImageReply{}, nil
	}}
	p := agentboximage.PreparedContext{DesiredRef: composed, BaseRef: agentboximage.RefAgentbox, Recipe: imagejob.Recipe{Builder: "composed-fixture", Dockerfile: []byte("FROM " + agentboximage.RefAgentbox + "\nRUN echo fixture\n")}}
	if err := state.prepare(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if acquisitions != 2 || finished != 1 || len(notices) != 3 || notices[0] != composed || notices[1] != agentboximage.RefAgentbox || notices[2] != composed {
		t.Fatal("base/composed phase or outside-lock drain sequence", acquisitions, finished, notices)
	}
}

func TestPreparingRequestAbbreviatesFeatures(t *testing.T) {
	var names []string
	for i := range 65 {
		names = append(names, "f"+strconv.Itoa(i))
	}
	r := preparingRequest(agentboximage.PreparedContext{DesiredRef: "local/composed:x", Features: names}, "worker")
	if r.Action != "preparing" || r.Box != "worker" || len(r.Features) != controller.ProgressFeatures || r.MoreFeatures != 65-controller.ProgressFeatures {
		t.Fatalf("%+v", r)
	}
}
