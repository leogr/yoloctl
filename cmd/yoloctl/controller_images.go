package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/agentboximage"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/imagejob"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/storagegc"
	"github.com/leogr/yoloctl/internal/termio"
)

// Read-only seam for the managed-base cache check during outside-lock warming.
var preparedBasePresent = func(ctx context.Context, ref string) (bool, error) {
	return container.New(spaceenv.Current().Space).ImagePresent(ctx, ref)
}

type warmKey struct{}
type warmGateKey struct{}
type imageWarmState struct {
	box              string
	probes           map[string]npmProbeEntry
	probeEnabled     bool
	snapshot         string
	imagesEnabled    bool
	call             controller.ImageCall
	entries          map[string]warmImage
	force            bool
	applied          bool
	recreateApproved bool
	enabled          bool
	desired          *agentbox.Config
}
type warmImage struct {
	context agentboximage.PreparedContext
	pin     controller.PinnedImage
	local   *nativeLocalImage
}
type imageWarmNeeded struct {
	prepared agentboximage.PreparedContext
	retain   bool
}

func (e *imageWarmNeeded) Error() string { return "image preparation required before apply" }
func sameContext(a, b agentboximage.PreparedContext) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func imageWarmPending(err error) bool { var e *imageWarmNeeded; return errors.As(err, &e) }
func warmingPath(path string) bool {
	switch path {
	case "box create", "box copy", "box recreate", "box restart", "box set", "box start", "box reconcile", "box attach", "attach", "port forward", "port unforward", "image build", "image refresh", "image pull":
		return true
	}
	return false
}

// Called under the ordinary native locks, after read-only refusal checks and
// before any declaration or runtime effect. A miss unwinds every guard; the
// worker driver prepares outside the locks, then re-enters current validation.
func warmBoxBeforeEffects(ctx context.Context, c *container.Client, cfg *agentbox.Config, persisted bool) error {
	state, _ := ctx.Value(warmKey{}).(*imageWarmState)
	if state == nil || !state.enabled {
		return nil
	}
	state.box = cfg.Name
	if err := cfg.Validate(); err != nil {
		return err
	}
	if _, err := agentbox.ComposePolicy(cfg); err != nil {
		return err
	}
	if err := agentbox.ValidateChatDeclaration(cfg); err != nil {
		return err
	}
	if _, _, err := sessionTrackingFor(cfg); err != nil {
		return err
	}
	if !currentControllerPrincipal(ctx).Admin() && cfg.Privileged {
		if !persisted {
			if err := guardOperatorPrivilegedCreate(ctx, cfg); err != nil {
				return err
			}
		} else {
			details, inspectErr := c.Inspect(ctx, cfg.Name)
			if err := guardPrivilegedLaunch(ctx, cfg, launchRecreate, details, inspectErr); err != nil {
				return err
			}
		}
	}
	validation := *cfg
	validation.Mounts = append([]agentbox.Mount(nil), cfg.Mounts...)
	if _, err := validateAgentboxRuntime(ctx, c, &validation, persisted); err != nil {
		return err
	}
	candidates, err := agentbox.RuntimeFeatures(cfg)
	if err != nil {
		return err
	}
	plan, err := cfg.FeaturePlan()
	if err != nil {
		return err
	}
	prep, err := agentboximage.PrepareComposed(ctx, cfg.Image, candidates, agentboximage.Options{FeaturePlan: plan, Force: state.force})
	if err != nil {
		return err
	}
	if !state.applied {
		if _, err := state.call(ctx, controller.ImageRequest{Action: "check-desired", Reference: prep.DesiredRef}); err != nil {
			return err
		}
	}
	if err := state.requireNpmProbe(ctx, cfg.Agent); err != nil {
		return err
	}
	if err := func() error {
		if !state.imagesEnabled && !nativeCreationImagePreparation(ctx) {
			return nil
		}
		return requireWarmImage(ctx, c, prep)
	}(); err != nil {
		return err
	}
	state.desired = cfg
	if gate, ok := ctx.Value(warmGateKey{}).(func() error); ok {
		if err := gate(); err != nil {
			return err
		}
	}
	state.applied = true
	return nil
}
func requireWarmImage(ctx context.Context, c *container.Client, p agentboximage.PreparedContext) error {
	state, _ := ctx.Value(warmKey{}).(*imageWarmState)
	if state == nil {
		if nativeCreationImagePreparation(ctx) {
			return errors.New("native image preparation requires its controller worker")
		}
		return nil
	}
	if entry, ok := state.entries[p.DesiredRef]; ok && sameContext(entry.context, p) {
		if entry.local != nil {
			return state.validateNativeLocalImage(ctx, entry)
		}
		if _, err := state.call(ctx, controller.ImageRequest{Action: "validate", JobID: entry.pin.JobID}); err == nil {
			return nil
		} else if nativeCreationImagePreparation(ctx) {
			return err
		}
		delete(state.entries, p.DesiredRef)
	}
	if nativeCreationImagePreparation(ctx) {
		if state.applied {
			return errors.New("native image recipe changed after apply began; run the command again")
		}
		return &imageWarmNeeded{prepared: p, retain: true}
	}
	// Unmanaged no-install bases retain the explicitly locked cold pull path.
	if len(p.Recipe.Dockerfile) == 0 && p.DesiredRef != agentboximage.RefAgentbox {
		return nil
	}
	if !p.Recipe.Force {
		present, err := c.ImagePresent(ctx, p.DesiredRef)
		if err != nil {
			return err
		}
		if present {
			return nil
		}
	}
	if state.applied {
		return errors.New("image recipe changed after apply began; run the command again")
	}
	return &imageWarmNeeded{prepared: p}
}

// Finish progress while the preparation-needed stack has unwound all native
// guards, before the worker re-enters its apply pass. A slow client may wait
// here without holding the space/entity locks or blocking another image job.
func (s *imageWarmState) prepare(ctx context.Context, p agentboximage.PreparedContext) error {
	if err := s.prepareImages(ctx, p); err != nil {
		return err
	}
	_, err := s.call(ctx, controller.ImageRequest{Action: "prepared"})
	return err
}
func (s *imageWarmState) prepareImages(ctx context.Context, p agentboximage.PreparedContext) error {
	if _, err := s.call(ctx, preparingRequest(p, s.box)); err != nil {
		return err
	}
	if len(p.Recipe.Dockerfile) == 0 {
		base, err := agentboximage.PrepareManagedBase(s.force)
		if err != nil {
			return err
		}
		if err = s.prepareImages(ctx, base); err != nil {
			return err
		}
		entry := s.entries[base.DesiredRef]
		entry.context = p
		s.entries[p.DesiredRef] = entry
		return nil
	}
	// Managed base and composed image are separate input nodes; never occupy
	// a composed slot while waiting for the base's slot.
	baseRef := p.BaseRef
	if baseRef == agentboximage.RefAgentbox {
		present, err := preparedBasePresent(ctx, baseRef)
		if err != nil {
			return err
		}
		if !present {
			base, err := agentboximage.PrepareManagedBase(false)
			if err != nil {
				return err
			}
			if err = s.prepareImages(ctx, base); err != nil {
				return err
			}
			baseRef = s.entries[base.DesiredRef].pin.Reference
			// The recursive base changed the request's quiet-phase label. Restore
			// the composed destination before its resolution and solve can wait.
			if _, err := s.call(ctx, preparingRequest(p, s.box)); err != nil {
				return err
			}
		}
	}
	resolved, err := s.call(ctx, controller.ImageRequest{Action: "resolve", Reference: baseRef, Remote: p.Recipe.Builder == "pull/1"})
	if err != nil {
		return err
	}
	recipe, err := p.BindBase(resolved.Digest, resolved.Platform)
	if err != nil {
		return err
	}
	recipe.BaseReference = baseRef
	inputs, err := imagejob.DockerfileImages(recipe)
	if err != nil {
		return err
	}
	baseCanonical, err := imagejob.CanonicalReference(p.BaseRef)
	if err != nil {
		return err
	}
	uses := map[string]int{}
	for _, input := range inputs {
		uses[input.Reference]++
	}
	resolvedDeps := map[string]controller.ImageReply{}
	for i, input := range inputs {
		if input.Reference == baseCanonical && input.Platform == resolved.Platform && uses[input.Reference] == 1 {
			inputs[i].Source = baseRef
			inputs[i].Digest = resolved.Digest
			continue
		}
		// A registry index binds every platform of a multi-platform source to
		// one immutable locator. A native-only local manifest cannot stand in
		// for an explicitly foreign FROM platform.
		dep, ok := resolvedDeps[input.Reference]
		if !ok {
			dep, err = s.call(ctx, controller.ImageRequest{Action: "resolve", Reference: input.Reference, Remote: uses[input.Reference] > 1 || input.Platform != resolved.Platform})
			if err != nil {
				return err
			}
			resolvedDeps[input.Reference] = dep
		}
		inputs[i].Digest = dep.Digest
	}

	recipe.Images = inputs

	result, err := s.call(ctx, controller.ImageRequest{Action: "acquire", Recipe: recipe})
	if err != nil {
		return err
	}
	s.entries[p.DesiredRef] = warmImage{context: p, pin: result.Pin}
	return nil
}
func (s *imageWarmState) consume(ctx context.Context, p agentboximage.PreparedContext) (string, bool, error) {
	entry, ok := s.entries[p.DesiredRef]
	if !ok {
		if nativeCreationImagePreparation(ctx) {
			return "", true, errors.New("native image consumption lacks its retained preparation")
		}
		return "", false, nil
	}
	if entry.local != nil {
		if !sameContext(entry.context, p) {
			return "", true, errors.New("local image recipe changed before consumption")
		}
		if err := s.validateNativeLocalImage(ctx, entry); err != nil {
			return "", true, err
		}
		return entry.pin.Digest, true, nil
	}
	// Force is consumed once; Create must never build a second refresh.
	compare := p
	compare.Recipe.Force = entry.context.Recipe.Force
	compare.Recipe.NoCache = entry.context.Recipe.NoCache
	if !sameContext(entry.context, compare) {
		return "", true, errors.New("prepared image recipe changed before consumption")
	}
	if err := s.publishPrepared(ctx, p, entry); err != nil {
		return "", true, err
	}
	// nerdctl v2.3.5 resolves a bare OCI target digest through its image
	// walker. A name@digest needs a separate alias, deliberately not exported.
	return entry.pin.Digest, true, nil
}
func runPreparedControllerWorker(ctx context.Context, work controller.WorkerInput, state *imageWarmState, execute func(context.Context, controller.WorkerInput) error) error {
	// The inherited view belongs to the whole request. A preparation miss
	// returns from execute before retrying, but must retain this channel and
	// any queued input for the next pass and eventual session attachment.
	if work.Request.View {
		view, err := termio.ViewFile(5)
		if err != nil {
			return err
		}
		defer func() { _ = view.Close() }()
		ctx = context.WithValue(ctx, controllerViewKey{}, view)
	}
	ctx = context.WithValue(ctx, warmKey{}, state)
	ctx = agentboximage.WithPreparedImage(ctx, state.consume)
	imageLoops, probeLoops := 0, 0
	for {
		err := execute(ctx, work)
		var image *imageWarmNeeded
		var probe *npmProbeNeeded
		if errors.As(err, &image) {
			if imageLoops >= 2 {
				return errors.New("image inputs changed repeatedly before apply; retry the command")
			}
			imageLoops++
			prepare := state.prepare
			if image.retain {
				prepare = state.prepareNativeCreationImage
			}
			if err := prepare(ctx, image.prepared); err != nil {
				return err
			}
			continue
		}
		if errors.As(err, &probe) {
			if probeLoops >= 2 {
				return errors.New("npm profile changed repeatedly before apply; retry the command")
			}
			probeLoops++
			if err := state.prepareNpmProbe(ctx, probe); err != nil {
				return err
			}
			continue
		}
		return err
	}
}

type controllerImageBackend interface {
	controller.ImageBackend
	controller.ImageService
	Configure(func(context.Context, controller.ImageRecipe) error, func(context.Context) error)
	Sweep(context.Context, []controller.Receipt) error
	Close()
}

var newControllerImageBackend = func(ctx context.Context) (controllerImageBackend, error) {
	return controller.NewNativeImageBackend(ctx)
}

func startControllerImages(serviceCtx, ctx context.Context, s *controller.Server) (func(), error) {
	backend, err := newControllerImageBackend(ctx)
	if err != nil {
		return nil, err
	}
	client := container.New(s.Hello.Space)
	// Controller preparation cleanup is a separate brief native transaction;
	// no space-lock holder ever waits for a slot or a build.
	backend.Configure(func(ctx context.Context, r controller.ImageRecipe) error {
		disk, err := storagegc.RootDisk()
		if err != nil {
			return err
		}
		run := func(ctx context.Context, permit func() error) error {
			refs := s.Images.RetainedReferences()
			refs = append(refs, r.BaseReference)
			return runImagePreflightProcess(ctx, refs, disk.Low(), s.Receipts.Dir, permit)
		}
		if disk.Low() {
			return s.Images.Cleanup(ctx, run)
		}
		return run(ctx, nil)
	}, client.EnsureBuilder)
	incarnation, err := imageVMIncarnation(s.DiskID)
	if err != nil {
		backend.Close()
		return nil, err
	}
	jobs, err := controller.NewImageJobs(serviceCtx, s, backend, incarnation, runtime.NumCPU())
	if err != nil {
		backend.Close()
		return nil, err
	}
	records, err := s.Receipts.List()
	if err != nil {
		backend.Close()
		return nil, err
	}
	if err := backend.Sweep(ctx, records); err != nil {
		backend.Close()
		return nil, err
	}
	s.Images = jobs
	var once sync.Once
	return func() { once.Do(backend.Close) }, nil
}

func warmManualImage(ctx context.Context, p agentboximage.PreparedContext) (bool, error) {
	if err := controller.ValidatePublicImageReference(p.DesiredRef); err != nil {
		return true, err
	}
	state, _ := ctx.Value(warmKey{}).(*imageWarmState)
	if state == nil {
		return false, nil
	}
	if !state.applied {
		if _, err := state.call(ctx, controller.ImageRequest{Action: "check-desired", Reference: p.DesiredRef}); err != nil {
			return true, err
		}
	}
	entry, ok := state.entries[p.DesiredRef]
	if !ok || !sameContext(entry.context, p) {
		if state.applied {
			return true, errors.New("image changed after apply permission")
		}
		return true, &imageWarmNeeded{prepared: p}
	}
	if gate, ok := ctx.Value(warmGateKey{}).(func() error); ok {
		if err := gate(); err != nil {
			return true, err
		}
	}
	state.applied = true
	// Publication is separately permitted under the native space lock.
	return true, state.publishPrepared(ctx, p, entry)
}

func runImagePreflightProcess(ctx context.Context, refs []string, cleanup bool, receipts string, permit func() error) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(imagePreflightInput{References: refs, Cleanup: cleanup, ReceiptsDir: receipts, CleanupID: controller.ImageCleanupID(ctx)})
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, executable, "controller-internal", "image-preflight")
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C.UTF-8", spaceenv.ContextEnv + "=" + spaceenv.ContextPath}
	cmd.Stdin = bytes.NewReader(raw)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return runImagePreflightCommand(ctx, cmd, permit)
}

func gateWithoutImageBuild(ctx context.Context) error {
	if gate, ok := ctx.Value(warmGateKey{}).(func() error); ok {
		return gate()
	}
	return nil
}

// The child announces its held native guard over private inherited pipes;
// stdout/stderr and input JSON can never grant cleanup permission.
func runImagePreflightCommand(ctx context.Context, cmd *exec.Cmd, permit func() error) error {
	if permit == nil {
		return waitStorageGCProcess(ctx, cmd)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = readyR.Close() }()
	defer func() { _ = readyW.Close() }()
	permitR, permitW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = permitR.Close() }()
	defer func() { _ = permitW.Close() }()
	cmd.ExtraFiles = []*os.File{readyW, permitR}
	done := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { _ = readyR.Close(); _ = permitW.Close() })
	defer stop()
	go func() {
		defer func() { _ = permitW.Close() }()
		var signal [1]byte
		if _, err := io.ReadFull(readyR, signal[:]); err != nil {
			done <- nil
			return
		}
		if signal[0] != 1 {
			done <- errors.New("invalid cleanup lock checkpoint")
			return
		}
		if err := permit(); err != nil {
			done <- err
			return
		}
		_, err := permitW.Write([]byte{1})
		done <- err
	}()
	err = waitStorageGCProcessStarted(ctx, cmd, func() { _ = readyW.Close(); _ = permitR.Close() })
	// Start failure or a child exiting before its checkpoint must unblock read.
	_ = readyW.Close()
	_ = readyR.Close()
	_ = permitW.Close()
	return errors.Join(err, <-done)
}

// Keep every warm output private until this consumer's ordinary apply. A
// composed job may have required a newly built managed base; retain and publish
// that base under its canonical name as well, before releasing either pin.
func (s *imageWarmState) publishPrepared(ctx context.Context, p agentboximage.PreparedContext, entry warmImage) error {
	if entry.local != nil {
		return s.validateNativeLocalImage(ctx, entry)
	}
	type publication struct {
		reference string
		pin       controller.PinnedImage
	}
	var outputs []publication
	if p.BaseRef == agentboximage.RefAgentbox && p.DesiredRef != agentboximage.RefAgentbox {
		if base, ok := s.entries[agentboximage.RefAgentbox]; ok && base.local == nil {
			outputs = append(outputs, publication{agentboximage.RefAgentbox, base.pin})
		}
	}
	outputs = append(outputs, publication{p.DesiredRef, entry.pin})
	writes := make([]controller.Resource, 0, len(outputs))
	for _, output := range outputs {
		writes = append(writes, controller.Resource{Kind: controller.ResourceImages, Name: output.reference})
	}
	// Expand the whole footprint before the first publication, including when
	// either destination is held. Workers still cannot publish directly.
	if err := controller.ExpandEffects(ctx, writes...); err != nil {
		return err
	}
	for _, output := range outputs {
		if _, err := s.call(ctx, controller.ImageRequest{Action: "publish", JobID: output.pin.JobID, Reference: output.reference}); err != nil {
			return err
		}
	}
	return nil
}

// preparingRequest announces an image: its reference, box and, for a
// composed image, the first feature names with the count of the others.
func preparingRequest(p agentboximage.PreparedContext, box string) controller.ImageRequest {
	names, more := controller.ProgressFeatureNames(p.Features)
	return controller.ImageRequest{Action: "preparing", Reference: p.DesiredRef, Box: box, Features: names, MoreFeatures: more}
}
