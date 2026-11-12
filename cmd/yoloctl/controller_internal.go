package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/leogr/yoloctl/internal/agentbox"
	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/boxstate"
	"github.com/leogr/yoloctl/internal/container"
	"github.com/leogr/yoloctl/internal/controller"
	"github.com/leogr/yoloctl/internal/operator"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/spacelock"
	"github.com/leogr/yoloctl/internal/workercheckpoint"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sys/unix"
)

// These are operation identities, not shell commands. New commands require an
// explicit entry and an explicit operator authorization decision.
var controllerOperations = map[string]controller.Operation{
	"chat.artifact.upload":  {Mutates: true, EffectGated: true, Footprint: controller.FootprintChat},
	"chat.artifact.read":    {}, // host-only, read-only: one confined artifact download
	"chat.channel.list":     {BoxAllowed: true},
	"chat.channel.catalog":  {BoxAllowed: true},
	"chat.channel.create":   {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintChat},
	"chat.channel.add":      {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintChat},
	"chat.channel.remove":   {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintChat},
	"chat.channel.delete":   {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintChat},
	"chat.stream":           {Mutates: true, EffectGated: true, Footprint: controller.FootprintChat},
	"chat.status":           {BoxAllowed: true},
	"chat.ensure":           {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintChat | controller.FootprintBox | controller.FootprintImages | controller.FootprintPorts},
	"chat.enroll":           {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintChat | controller.FootprintBox | controller.FootprintImages | controller.FootprintPorts},
	"chat.revoke":           {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintChat | controller.FootprintBox | controller.FootprintImages | controller.FootprintPorts},
	"chat.connect":          {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintChat | controller.FootprintBox},
	"chat.disconnect":       {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintChat | controller.FootprintBox},
	"chat.nickname":         {Mutates: true, EffectGated: true, Footprint: controller.FootprintChat | controller.FootprintBox},
	"chat.attach-target":    {},
	"chat.state.get":        {},
	"chat.state.ensure":     {Mutates: true, EffectGated: true, Footprint: controller.FootprintChat},
	"chat.state.enroll":     {Mutates: true, EffectGated: true, Footprint: controller.FootprintChat | controller.FootprintBox},
	"chat.state.revoke":     {Mutates: true, EffectGated: true, Footprint: controller.FootprintChat},
	"control.complete":      {BoxAllowed: true},
	"box.create":            {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"box.copy":              {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"box.start":             {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"box.stop":              {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"box.restart":           {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"box.destroy":           {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"box.recreate":          {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"box.list":              {BoxAllowed: true},
	"box.show":              {BoxAllowed: true},
	"box.session":           {BoxAllowed: true},
	"box.inspect":           {BoxAllowed: true},
	"box.exec":              {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox},
	"box.attach":            {BoxAllowed: true, Mutates: true, EffectGated: true, OwnedSetup: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"box.logs":              {BoxAllowed: true},
	"box.reconcile":         {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"box.diff":              {BoxAllowed: true},
	"box.edit.get":          {BoxAllowed: true},
	"box.edit.apply":        {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"box.copy-source":       {BoxAllowed: true},
	"box.inventory":         {BoxAllowed: true},
	"box.drift":             {},
	"box.set":               {BoxAllowed: true, Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"account.create":        {Mutates: true, EffectGated: true, Footprint: controller.FootprintAccount},
	"account.list":          {},
	"account.show":          {},
	"account.edit.get":      {},
	"account.edit.apply":    {Mutates: true, EffectGated: true, Footprint: controller.FootprintAccount},
	"account.destroy":       {Mutates: true, EffectGated: true, Footprint: controller.FootprintAccount | controller.FootprintPorts | controller.FootprintBox | controller.FootprintChat},
	"account.secret.set":    {Mutates: true, EffectGated: true, Footprint: controller.FootprintAccount},
	"account.secret.unset":  {Mutates: true, EffectGated: true, Footprint: controller.FootprintAccount},
	"account.secret.list":   {},
	"account.auth.import":   {Mutates: true, EffectGated: true, Footprint: controller.FootprintAccount},
	"account.auth.status":   {},
	"account.observe":       {Mutates: true, EffectGated: true, Footprint: controller.FootprintAccount},
	"port.forward":          {Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"port.unforward":        {Mutates: true, EffectGated: true, Footprint: controller.FootprintBox | controller.FootprintPorts | controller.FootprintChat | controller.FootprintImages},
	"port.list":             {},
	"image.build":           {Mutates: true, EffectGated: true, Footprint: controller.FootprintImages},
	"image.pull":            {Mutates: true, EffectGated: true, Footprint: controller.FootprintImages},
	"image.refresh":         {Mutates: true, EffectGated: true, Footprint: controller.FootprintImages},
	"image.prune":           {Mutates: true, EffectGated: true, Footprint: controller.FootprintImages},
	"image.list":            {},
	"image.inventory":       {},
	"agent.update":          {Mutates: true, EffectGated: true, Footprint: controller.FootprintBox},
	"agent.status":          {},
	"space.settings":        {},
	"space.inventory":       {},
	"space.telemetry":       {},
	"space.storage":         {},
	"space.validate-grants": {},
}

type controllerReleaseKey struct{}
type controllerSessionKey struct{}
type controllerRequestKey struct{}

// controllerRequestID is the controller request this worker serves, or "".
func controllerRequestID(ctx context.Context) string {
	id, _ := ctx.Value(controllerRequestKey{}).(string)
	return id
}

// runControllerInternal is entered before the public CLI and is only usable
// by privileged Linux VM infrastructure. No public RPC exposes these actions.
func runControllerInternal(ctx context.Context, args []string) error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("controller infrastructure requires VM root")
	}
	if len(args) != 1 {
		return errors.New("one controller infrastructure action is required")
	}
	// Only this fixed serve entry consumes the launch ABI. Unknown old-image
	// descriptors remain untouched; optional proof failure preserves startup.
	// Capture precedes LoadRuntime/other descriptor allocation. Keep the
	// candidate separate from Server.PIDAnchor until composed admission review.
	if args[0] == "serve" {
		candidate := captureControllerLaunchCandidate(consumeEarlyControllerLaunch)
		defer func() { _ = candidate.Close() }()
		ctx = context.WithValue(ctx, controllerLaunchCandidateKey{}, candidate)
	}
	switch args[0] {
	case "restore-store":
		return restoreNativeSpace(ctx, os.Stdin)
	case "deploy":
		var deployment controllerDeployment
		if err := decodeBoundedJSON(os.Stdin, &deployment, 64<<20); err != nil {
			return err
		}
		return deployController(ctx, deployment)
	case "install-service":
		if err := configureBuildkitGC(ctx); err != nil {
			return err
		}
		if err := atomicfile.Write("/etc/systemd/system/"+controller.ServiceName, []byte(controller.ServiceUnit), 0o644); err != nil {
			return err
		}
		if err := exec.CommandContext(ctx, "systemctl", "daemon-reload").Run(); err != nil {
			return err
		}
		return exec.CommandContext(ctx, "systemctl", "enable", controller.ServiceName).Run()
	case "bootstrap":
		var c spaceenv.Context
		if err := decodeBoundedJSON(os.Stdin, &c, controller.MaxDocument); err != nil {
			return err
		}
		if err := c.Validate(); err != nil {
			return err
		}
		bundle, err := controller.ExecutableIdentity()
		if err != nil || bundle != c.Bundle {
			return errors.New("bootstrap and installed controller builds differ")
		}
		if err := os.MkdirAll("/etc/yoloctl", 0o755); err != nil {
			return err
		}
		raw, err := json.Marshal(c)
		if err != nil {
			return err
		}
		return safepath.WriteFile("/etc", "yoloctl/controller.json", raw, 0o600)
	case "connect":
		return connectController(ctx)
	}
	// Exact worker intake claims infrastructure before runtime child execution.
	if args[0] == "worker" {
		return runControllerWorker(ctx)
	}
	if err := controller.LoadRuntime(ctx); err != nil {
		return err
	}
	switch args[0] {
	case "image-preflight":
		return imagePreflightWorker(ctx)
	case "export-store":
		return exportNativeSpace(ctx, os.Stdout)
	case "store-state":
		state, err := controller.InspectStore(spaceenv.Current().DiskMount)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(state)
	case "validate-grants":
		var mounts []spaceconfig.Mount
		if err := decodeBoundedJSON(os.Stdin, &mounts, controller.MaxDocument); err != nil {
			return err
		}
		catalogs, _, cleanup, err := controller.CatalogSnapshot(spaceenv.Current().CatalogDir, spaceenv.RunRoot)
		if err != nil {
			return err
		}
		defer cleanup()
		spaceenv.UseCatalogSnapshot(catalogs)
		return validateControllerGrants(mounts)
	case "stop-workloads":
		space := spaceenv.Current().Space
		boxes, err := resolveTeardownBoxes(ctx, space)
		if err != nil {
			return err
		}
		return stopSpaceBoxes(ctx, space, boxes, os.Stderr)
	case "initialize":
		var bundle controller.StoreBundle
		if err := decodeBoundedJSON(os.Stdin, &bundle, 64<<20); err != nil {
			return err
		}
		c := spaceenv.Current()
		boot := &spaceconfig.Bootstrap{Name: c.Space, DiskID: c.DiskID, DiskUUID: c.DiskUUID}
		for _, m := range c.Mounts {
			boot.Mounts = append(boot.Mounts, spaceconfig.Mount{Source: m.Source, Writable: m.Writable})
		}
		digest, err := controller.PublishStore(c.DiskMount, boot, bundle)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(os.Stdout, digest)
		return err
	case "activate":
		if err := controller.VerifyStoreDirectories(spaceenv.Current().DiskMount); err != nil {
			return err
		}
		c := spaceenv.Current()
		if _, err := spaceconfig.Load(c.Space); err != nil {
			return err
		}
		if err := controller.ActivateStore(c.DiskMount); err != nil {
			return err
		}
		err := safepath.RemoveFile(filepath.Join(c.DiskMount, ".controller"), "maintenance")
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	case "serve":
		return serveController(ctx)
	case "verify":
		return runControllerVerifier(ctx)
	case "collect-storage":
		return collectControllerStorage(ctx)
	case "observe-codex":
		return collectCodexObservation(ctx)
	default:
		return errors.New("unknown controller infrastructure action")
	}
}

func decodeBoundedJSON(r io.Reader, dst any, limit int64) error {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return errors.New("controller document exceeds size limit")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("trailing controller document data")
	}
	return nil
}

func connectController(ctx context.Context) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(spaceenv.RunRoot, "admin.sock"))
	if err != nil {
		return fmt.Errorf("connect to space controller: %w", err)
	}
	defer func() { _ = conn.Close() }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	go func() {
		_, _ = io.Copy(conn, os.Stdin)
		if c, ok := conn.(*net.UnixConn); ok {
			_ = c.CloseWrite()
		}
	}()
	_, err = io.Copy(os.Stdout, conn)
	return err
}

func serveController(ctx context.Context) (result error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := spaceenv.Current()
	if err := controller.VerifyStoreDirectories(c.DiskMount); err != nil {
		return err
	}
	if _, err := spaceconfig.Load(c.Space); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(spaceenv.RunRoot), 0o755); err != nil {
		return err
	}
	if _, err := controller.PrivateDir(filepath.Dir(spaceenv.RunRoot), filepath.Base(spaceenv.RunRoot)); err != nil {
		return err
	}
	fd, err := unix.Open(filepath.Join(spaceenv.RunRoot, "service.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("another space controller already owns this VM")
	}
	meta, err := controller.PrivateDir(c.DiskMount, ".controller")
	if err != nil {
		return err
	}
	activationObservation, err := readOriginalControllerActivation(meta)
	if err != nil {
		return errors.New("space configuration is not activated")
	}
	if activationObservation != nil {
		defer func() { _ = activationObservation.Close() }()
	}
	// Everything above is store authority: an invalid disk, store or
	// activation refuses to serve. Receipt and recovery metadata below are
	// repairable: their failure starts a read-only server that retries them.
	verifyStorage := func(ctx context.Context) error {
		if err := controller.VerifyRuntimeStorage(ctx); err != nil {
			return err
		}
		if err := controller.VerifyStoreDirectories(c.DiskMount); err != nil {
			return err
		}
		_, err := controller.PrivateDir(meta, "receipts")
		return err
	}
	server := &controller.Server{
		Hello:      controller.Hello{Protocol: controller.ProtocolVersion, Bundle: c.Bundle, Space: c.Space, Generation: c.Generation},
		Operations: controllerOperations, Execute: controller.RunWorker,
		Receipts:    controller.Receipts{Dir: filepath.Join(meta, "receipts")},
		Maintenance: func() error { return safepath.WriteFile(meta, "maintenance", []byte(c.Generation), 0o600) },
		ResumeAdmission: func() error {
			err := safepath.RemoveFile(meta, "maintenance")
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		},
		CheckPrincipal: checkOperatorPrincipal,
		DiskID:         c.DiskID,
		Verify: func(ctx context.Context, r controller.Receipt) ([]controller.DocumentIntent, error) {
			return controller.RunVerifier(ctx, c.Space, r)
		},
		VerifyStorage: verifyStorage,
	}
	// Optional retained source preparation is separate from server publication.
	// This NEW source lease owns only observer FDs; singleton/source keep their
	// original lexical owners. Its extra qualification error preserves startup.
	sourceLease, sourceLeaseErr := captureControllerServiceSource(ctx, c, fd, activationObservation)
	_ = sourceLeaseErr // explicit source unavailable; no authority fallback
	if sourceLease != nil {
		defer func() { _ = sourceLease.Close() }()
	}
	// Server.PIDAnchor intentionally remains UNSET in this preparation hunk.
	var imageStopMu sync.Mutex
	var stopImages func()
	defer func() {
		imageStopMu.Lock()
		defer imageStopMu.Unlock()
		if stopImages != nil {
			stopImages()
		}
	}()
	serviceCtx := ctx
	prepareRecovery := func(ctx context.Context) error {
		if err := verifyStorage(ctx); err != nil {
			return err
		}
		// Old workers are already gone (guest supervisor cgroup cleanup), and no
		// mutation is admitted before this succeeds. Nothing is replayed.
		interrupted, err := server.Receipts.Recover()
		if err != nil {
			return err
		}
		if interrupted {
			// An unknown runtime inventory keeps the space-wide hold.
			if _, err := newBoxStateResolver().ResolveSpace(ctx, c.Space, boxstate.RuntimeInventory); err != nil {
				return fmt.Errorf("inspect interrupted controller operations: %w", err)
			}
			_, _ = fmt.Fprintln(os.Stderr, "Interrupted operations have uncertain outcomes; the controller checks their recorded evidence and holds what it cannot prove.")
		}
		key, err := controller.LoadRecoveryKey(meta)
		if err != nil {
			return err
		}
		imageStopMu.Lock()
		if stopImages == nil {
			stopImages, err = startControllerImages(serviceCtx, ctx, server)
		}
		imageStopMu.Unlock()
		if err != nil {
			return err
		}
		return server.ActivateRecovery(key)
	}
	if err := prepareRecovery(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Controller is read-only until receipt storage recovers:", err)
		server.Degrade(err, prepareRecovery)
	}
	go server.RunRecovery(ctx)
	if err := installOperatorHelper(); err != nil {
		return err
	}
	if err := restoreOperatorEndpoints(ctx); err != nil {
		return err
	}
	if _, err := safepath.ReadFile(meta, "maintenance", 4096); err == nil {
		if err := server.Drain(ctx); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stopStorageGC := startControllerStorageGC(ctx, server)
	defer stopStorageGC()
	stopCodexObserve := startControllerCodexObserve(ctx, server)
	defer stopCodexObserve()
	operatorDone := make(chan error, 1)
	go func() { operatorDone <- serveOperatorEndpoints(ctx, server); cancel() }()
	defer func() {
		cancel()
		if err := <-operatorDone; err != nil {
			result = err
		}
	}()
	listener, err := controller.Listen(filepath.Join(spaceenv.RunRoot, "admin.sock"), 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	go func() { <-ctx.Done(); _ = listener.Close() }()
	var clients sync.WaitGroup
	defer func() { cancel(); clients.Wait() }()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		clients.Add(1)
		go func() {
			defer clients.Done()
			server.ServeConn(ctx, conn, controller.Principal{})
		}()
	}
}

func runControllerWorker(ctx context.Context) error {
	early, err := acquireEarlyControllerWorker(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = early.Close() }() // optional errors cannot rewrite ordinary result
	work := early.work
	if err := controller.VerifyStoreDirectories(spaceenv.Current().DiskMount); err != nil {
		return err
	}
	if err := controller.Authorize(work.Principal, work.Request, spaceenv.Current().Space, controllerOperations); err != nil {
		return err
	}
	// Reuse the phase writer ALREADY raw-claimed before LoadRuntime. Exact
	// collector/final-flush behavior is the accepted ae33 replay, not a second claim.
	if phaseWriter := early.phase; phaseWriter != nil {
		collector := operator.NewNativePhaseCollector()
		ctx = operator.WithNativePhaseCollector(ctx, collector)
		defer func() {
			if raw, phaseErr := collector.MarshalFinal(); phaseErr == nil {
				phaseWriter.Flush(raw)
			}
		}()
	}
	// Retain optional opaque observation through Finish/actual worker return.
	// No SDK accessor or Server.PIDAnchor assignment is installed by this value.
	ctx = context.WithValue(ctx, workerLaunchCandidateKey{}, early.launch)
	ctx = withControllerPrincipal(ctx, work.Principal)
	ctx = context.WithValue(ctx, controllerSessionKey{}, work.Request.Session)
	ctx = context.WithValue(ctx, controllerRequestKey{}, work.Request.ID)
	released, err := early.TakeCheckpoint()
	if err != nil {
		return err
	}
	// NewWorkerCheckpoints transfers SAME original File to ONE physical owner.
	// No direct released.Close remains after this transfer.
	checkpoints := controller.NewWorkerCheckpoints(released)
	defer func() { _ = checkpoints.Close() }()
	// Failure leaves optional source unavailable; ordinary operation/results
	// retain their existing path. This does not install Server.PIDAnchor/SDK.
	bindControllerCheckpoint(ctx, early.launch, checkpoints.SharedChannel())
	ctx = spacelock.WithObserver(ctx, func(ctx context.Context, event spacelock.Event) {
		action := "released"
		if event.Held {
			action = "acquired"
		}
		_, _ = checkpoints.Advisory(ctx, controller.AdvisoryNotice{Action: action, Space: event.Space, Exclusive: event.Exclusive, Token: event.Token})
	})
	ctx = context.WithValue(ctx, controllerAdvisoryKey{}, checkpoints.Advisory)
	ctx = context.WithValue(ctx, controllerReleaseKey{}, checkpoints.Release)
	state := &imageWarmState{call: checkpoints.Image, entries: map[string]warmImage{}, enabled: warmingPath(strings.ReplaceAll(work.Request.Method, ".", " ")) || work.Request.Method == "box.edit.apply" || strings.HasPrefix(work.Request.Method, "chat.")}
	state.imagesEnabled = work.Request.Method != "box.restart"
	state.probeEnabled = npmProbeMethod(work.Request.Method) && (len(work.Request.Flags["no-autoupdate"]) == 0 || work.Request.Flags["no-autoupdate"][0] != "true")
	operation := controllerOperations[work.Request.Method]
	if operation.EffectGated {
		checkpoints.Gate()
		ctx = withWorkerNativeIncarnation(ctx, operation, checkpoints)
		ctx = controller.WithEffectGate(ctx, func(ctx context.Context, intent controller.EffectIntent) error {
			if err := checkpoints.BeginEffects(ctx, intent); err != nil {
				return err
			}
			state.applied = true
			return nil
		})
	}
	completion := &workerCompletion{}
	if operation.Mutates {
		ctx = controller.WithEffectRecorder(ctx, checkpoints)
		ctx = context.WithValue(ctx, workerCompletionKey{}, completion)
	}

	state.force = work.Request.Method == "box.reconcile" && len(work.Request.Flags["rebuild"]) > 0 && work.Request.Flags["rebuild"][0] == "true"
	err = runPreparedControllerWorker(ctx, work, state, executeControllerWorker)
	finish := checkpoints.Finish
	if workerEffectsUncertain(err) {
		finish = checkpoints.FinishUncertain
	}
	// The space-operation wrapper records completion before releasing its
	// locks; every other mutation records it once its handler has returned.
	if operation.Mutates && !completion.owned {
		recordWorkerCompletion(ctx, err)
	}
	if finishErr := finish(); finishErr != nil && err == nil {
		return finishErr
	}
	return err
}

// The component bridge is installed only in this admitted effect-gated worker.
// The callback still passes through the private FD's actual phase/receipt/box
// checks; a context value itself supplies no effect or publication permission.
func withWorkerNativeIncarnation(ctx context.Context, operation controller.Operation, checkpoints *controller.WorkerCheckpoints) context.Context {
	if checkpoints == nil || !operation.Mutates || !operation.EffectGated {
		return ctx
	}
	ctx = operator.WithNativeListenerRetirer(ctx, checkpoints.RetireOperatorListener)
	return operator.WithNativeIncarnationAllocator(ctx, checkpoints.NativeIncarnation)
}

type workerCompletionKey struct{}

// workerCompletion is claimed by the space-operation wrapper, which records
// completion itself (or deliberately withholds it).
type workerCompletion struct{ owned bool }

func claimWorkerCompletion(ctx context.Context) bool {
	c, ok := ctx.Value(workerCompletionKey{}).(*workerCompletion)
	if ok {
		c.owned = true
	}
	return ok
}

// recordWorkerCompletion durably records an execution that returned normally
// with a known result. A canceled execution or uncertain effects record
// nothing; a panic never reaches here. A normal error is a known failed
// result, never relabeled success. A failed record leaves the outcome to
// finalization, so it is reported without changing the command's result.
func recordWorkerCompletion(ctx context.Context, err error) {
	var probe *npmProbeNeeded
	if ctx.Err() != nil || workerEffectsUncertain(err) || imageWarmPending(err) || errors.As(err, &probe) {
		return
	}
	if recordErr := controller.CompleteEffects(ctx, classifyError(err)); recordErr != nil {
		_, _ = fmt.Fprintln(os.Stderr, "yoloctl: operation completion was not recorded:", recordErr)
	}
}

// Component services cannot import controller (which owns their declarations).
// Only typed trusted Go errors carry this marker; stdout/message text cannot.
func workerEffectsUncertain(err error) bool {
	var controllerError *controller.EffectsUncertain
	var nativeError *agentbox.NativeEffectsUncertain
	return errors.As(err, &controllerError) || errors.As(err, &nativeError) || errors.Is(err, workercheckpoint.ErrUncertain)
}

// completeBeforeUnlock is deferred by a dispatcher right after it takes its
// native lock. Deferred role publication is registered later, so it runs
// first. Completion is recorded from the dispatcher's own returned outcome
// before the lock is released, never after a failed role, membership or
// channel publication that the dispatcher reports through publicationFailed.
// A panic records nothing: the deferred call re-raises it unchanged.
func completeBeforeUnlock(ctx context.Context, result *error, publicationFailed *bool) {
	if p := recover(); p != nil {
		panic(p)
	}
	if claimWorkerCompletion(ctx) && (publicationFailed == nil || !*publicationFailed) {
		recordWorkerCompletion(ctx, *result)
	}
}

func executeControllerWorker(ctx context.Context, work controller.WorkerInput) error {
	if work.Request.Method == "account.secret.set" {
		var secret controllerSecret
		if err := decodeBoundedJSON(bytes.NewReader(work.Request.Payload), &secret, 2*controller.MaxDocument); err != nil {
			return err
		}
		if secret.Value == "" || len(secret.Value) > controller.MaxDocument {
			return errors.New("invalid secret payload size")
		}
		ctx = context.WithValue(ctx, controllerSecretKey{}, secret)
	}
	catalogs, digest, cleanup, err := controller.CatalogSnapshot(spaceenv.Current().CatalogDir, spaceenv.RunRoot)
	if err != nil {
		return err
	}
	defer cleanup()
	spaceenv.UseCatalogSnapshot(catalogs)
	if state, ok := ctx.Value(warmKey{}).(*imageWarmState); ok {
		state.snapshot = digest
	}
	if handled, err := runControllerChatState(ctx, work.Request, os.Stdout); handled {
		return err
	}
	// All build/pull paths, including document edits, invoke this only after
	// their own native mutation guard is held.
	collector := nativeStorageCollector(work.Request.Space, os.Stderr)
	ctx = container.WithBuildGuard(ctx, func(ctx context.Context, refs []string) error {
		// A build, pull or disposable-image collection writes the shared
		// image domain; the operation's footprint records that first.
		if err := controller.ExpandEffects(ctx, imagesDomain); err != nil {
			return err
		}
		return collector.beforeBuild(ctx, refs, os.Stderr)
	})
	if handled, err := runControllerChatRuntime(ctx, work.Request, os.Stdout, os.Stderr); handled {
		return err
	}
	if handled, err := runControllerChatChannels(ctx, work.Request, os.Stdout); handled {
		return err
	}
	if handled, err := runControllerChatParticipation(ctx, work.Request, os.Stdout, os.Stderr); handled {
		return err
	}
	if work.Request.Method == "chat.stream" {
		return runControllerChatStream(ctx, work.Request, os.Stdin, os.Stdout, os.Stderr)
	}
	if work.Request.Method == "chat.artifact.upload" {
		return runControllerChatImage(ctx, work.Request, os.Stdin, os.Stdout)
	}
	if work.Request.Method == "chat.artifact.read" {
		return runControllerChatArtifactRead(ctx, work.Request, os.Stdout)
	}
	if err := translateOperatorRequest(ctx, work.Principal, &work.Request); err != nil {
		return err
	}
	if work.Request.Method == "control.complete" {
		return runControllerCompletion(ctx, work.Principal, work.Request)
	}
	if handled, err := runControllerView(ctx, work.Request, os.Stdout); handled {
		return err
	}
	if handled, err := runControllerDocument(ctx, work.Request, digest); handled {
		return err
	}
	if work.Request.Method == "box.copy" && len(work.Request.Payload) > 0 {
		if !work.Principal.Admin() {
			return errors.New("box operators cannot import a cross-space source")
		}
		var raw []byte
		if err := json.Unmarshal(work.Request.Payload, &raw); err != nil {
			return err
		}
		source, err := agentbox.ParseManifest(raw)
		if err != nil {
			return err
		}
		if len(work.Request.Args) != 2 || source.Name != work.Request.Args[0] {
			return errors.New("copy source identity mismatch")
		}
		ctx = context.WithValue(ctx, copySourceKey{}, source)
	}
	// Force the trusted scope even when the public command's space flag was
	// omitted; a caller's persisted context has no authority inside this VM.
	if work.Request.Flags == nil {
		work.Request.Flags = map[string][]string{}
	}
	work.Request.Flags["space"] = []string{spaceenv.Current().Space}
	root := newRootCmd()
	root.SilenceErrors = true
	argv, err := controllerCommandArgs(root, work.Request)
	if err != nil {
		return err
	}
	root.SetArgs(argv)
	return root.ExecuteContext(ctx)
}

// Encoding flags uses the real shared definitions, preserving slice/array
// semantics, explicit empty values and -- for command arguments.
func controllerFlagArgs(flags *pflag.FlagSet, values map[string][]string) ([]string, error) {
	var argv []string
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		f := flags.Lookup(name)
		if f == nil {
			return nil, fmt.Errorf("unknown command flag %q", name)
		}
		v := values[name]
		if f.Value.Type() == "stringSlice" {
			var encoded bytes.Buffer
			if len(v) > 0 {
				w := csv.NewWriter(&encoded)
				if err := w.Write(v); err != nil {
					return nil, err
				}
				w.Flush()
				if err := w.Error(); err != nil {
					return nil, err
				}
			}
			argv = append(argv, "--"+name+"="+strings.TrimSuffix(encoded.String(), "\n"))
		} else {
			for _, value := range v {
				argv = append(argv, "--"+name+"="+value)
			}
		}
	}
	return argv, nil
}

func controllerCommandArgs(root *cobra.Command, req controller.Request) ([]string, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	beforeDash := len(req.Args)
	if req.Dash >= 0 {
		beforeDash = req.Dash
	}
	for _, arg := range req.Args[:beforeDash] {
		if strings.HasPrefix(arg, "-") {
			return nil, errors.New("controller positional arguments cannot inject command flags")
		}
	}
	words := strings.Split(req.Method, ".")
	cmd, remaining, err := root.Find(words)
	if err != nil || len(remaining) != 0 || strings.TrimPrefix(cmd.CommandPath(), root.Name()+" ") != strings.Join(words, " ") {
		return nil, errors.New("controller method has no shared command")
	}
	if cmd.RunE == nil {
		return nil, errors.New("controller method is not executable")
	}
	flags := pflag.NewFlagSet("operation", pflag.ContinueOnError)
	flags.AddFlagSet(cmd.Flags())
	flags.AddFlagSet(cmd.InheritedFlags())
	flagArgs, err := controllerFlagArgs(flags, req.Flags)
	if err != nil {
		return nil, err
	}
	argv := append(append([]string{}, words...), flagArgs...)
	if req.Dash < 0 {
		return append(argv, req.Args...), nil
	}
	argv = append(argv, req.Args[:req.Dash]...)
	argv = append(argv, "--")
	return append(argv, req.Args[req.Dash:]...), nil
}
