package controller

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/leogr/yoloctl/internal/imagejob"
	"github.com/leogr/yoloctl/pkg/prettylog"
	digest "github.com/opencontainers/go-digest"
)

// ImagePhase is the image job's own durable transaction, independent of every
// request's later apply. The stable input excludes force nonces and job IDs.
// PrivateVersion qualifies the one-time private-export/non-consumption contract.
// Abandonment records authority revocation, never proof the solve stopped.
type ImagePhase struct {
	Stage              string    `json:"stage,omitempty"`
	OrphanRecorded     bool      `json:"orphan_recorded,omitempty"`
	PrivateVersion     int       `json:"private_version,omitempty"`
	AbandonedAt        time.Time `json:"abandoned_at,omitempty,omitzero"`
	ReleasedAt         time.Time `json:"released_at,omitempty,omitzero"`
	Residual           bool      `json:"possible_surviving_work,omitempty"`
	LegacyBoot         string    `json:"legacy_residual_boot,omitempty"`
	LegacyAliasPending bool      `json:"legacy_alias_pending,omitempty"`
	JobID              string    `json:"job_id"`
	StableInput        string    `json:"stable_input"`
	Attempt            string    `json:"attempt"`
	SolveRef           string    `json:"solve_ref"`
	Incarnation        string    `json:"incarnation"`
	PreparedRef        string    `json:"prepared_ref"`
	Output             string    `json:"output,omitempty"`
	Lease              string    `json:"lease,omitempty"`
}

func (p ImagePhase) validate(r Receipt) error {
	if p.LegacyBoot != "" || p.LegacyAliasPending {
		if p.PrivateVersion != 0 || !p.Residual || r.Completion != nil || r.State != "interrupted" || !imageBootIdentity(p.LegacyBoot) {
			return errors.New("invalid legacy image residual evidence")
		}
	}
	if p.Stage != "" && p.Stage != "preflight" && p.Stage != "build" {
		return errors.New("invalid image phase stage")
	}
	if p.PrivateVersion < 0 || p.PrivateVersion > 1 {
		return errors.New("unsupported private image contract")
	}
	if !p.ReleasedAt.IsZero() && (r.Completion == nil || !p.AbandonedAt.IsZero()) {
		return errors.New("invalid completed image release")
	}
	if !p.AbandonedAt.IsZero() && p.PrivateVersion != 1 {
		return errors.New("invalid image abandonment")
	}
	if !p.AbandonedAt.IsZero() && r.Completion == nil && (!p.Residual || !p.OrphanRecorded) {
		return errors.New("abandoned image lacks its durable orphan fact")
	}
	if r.ProofVersion != 2 || r.Method != "image.job" || p.JobID != r.ID || p.SolveRef != p.JobID || p.PreparedRef != "yoloctl/prepared/"+p.JobID || p.Incarnation == "" {
		return errors.New("invalid image-phase identity")
	}
	for _, value := range []string{p.StableInput, p.Attempt} {
		b, err := hex.DecodeString(value)
		if err != nil || len(b) != sha256.Size {
			return errors.New("invalid image input identity")
		}
	}
	if r.Scope == nil || !containsResource(r.Scope.Writes, Resource{Kind: ResourceImages, Name: p.PreparedRef}) || p.PrivateVersion == 0 && !containsResource(r.Scope.Writes, inputResource(p.StableInput)) {
		return errors.New("image phase lacks its stable input and output reservations")
	}
	if p.Output != "" {
		if err := digest.Digest(p.Output).Validate(); err != nil {
			return errors.New("invalid image-phase output digest")
		}
	}
	if r.Completion != nil && r.Completion.Status == 0 && (p.Output == "" || p.Lease == "") {
		return errors.New("image completion lacks its retained output")
	}
	return nil
}

func inputResource(stable string) Resource {
	return Resource{Kind: ResourceImages, Name: "yoloctl/input:" + stable}
}

// ImageRecipe is copied between trusted workers and controller jobs.
type ImageRecipe = imagejob.Recipe

// ImageBackend belongs to the root controller. Preflight takes only a brief
// native space lock, after a slot is granted; Build runs with no such lock.
// Build must export privately and establish the durable lease before return.
// Known build errors use ImageBuildFailure; cancellation and lost transport
// remain unknown. No error classification is a proof that remote work stopped.
type ImageBackend interface {
	Preflight(context.Context, ImageRecipe) error
	Build(context.Context, ImageRecipe, ImagePhase) (PinnedImage, error)
	Validate(context.Context, PinnedImage) error
	Release(context.Context, PinnedImage) error
}

type PinnedImage struct{ JobID, Reference, Digest, Lease string }

type imageJob struct {
	log                                    imageBuildLog
	owner                                  BlockerSummary
	ownerCtx                               context.Context
	progress                               func(ImageWait)
	capacityReported                       bool
	receipt                                Receipt
	phase                                  ImagePhase
	recipe                                 ImageRecipe
	ctx                                    context.Context
	cancel                                 context.CancelFunc
	done                                   chan struct{}
	interests                              int
	slotted, building, finished, uncertain bool
	result                                 PinnedImage
	err                                    error
	// step is the job's build/pull/retain step; it ends with the job's result.
	step *prettylog.Step
	// image titles the stage step: the starting request's image reference
	// and features; the short job ID when unknown.
	image string
}

// ImageJobs is controller-owned. Workers only hold an interest and consume a
// result through their private channel. Running attempts exclude conflicting image work; ended outcomes do not
// reserve input slots. Strict stage and residual ownership facts remain durable.
type ImageJobs struct {
	server      *Server
	backend     ImageBackend
	incarnation string
	capacity    int
	cleaning    bool
	ctx         context.Context
	mu          sync.Mutex
	jobs        map[string]*imageJob
	changed     chan struct{}
}

func NewImageJobs(ctx context.Context, s *Server, b ImageBackend, incarnation string, cpus int) (*ImageJobs, error) {
	if s.DiskID == "" || incarnation == "" || b == nil {
		return nil, errors.New("image jobs require disk and VM-incarnation authority")
	}
	m := &ImageJobs{server: s, backend: b, incarnation: incarnation, capacity: max(cpus-1, 1), ctx: ctx, jobs: map[string]*imageJob{}, changed: make(chan struct{})}
	// Previous workers belong to a terminated controller. Their cleanup facts
	// are recovered separately; neither those facts nor record health reserves
	// capacity in this controller's live job registry.
	return m, nil
}

func (m *ImageJobs) notify() { close(m.changed); m.changed = make(chan struct{}) }

// RetainedImage remains interested through the consumer's locked apply. Release
// is idempotent; merely returning the warm result does not drop its pin.
type RetainedImage struct {
	PinnedImage
	once    sync.Once
	release func()
}

func (r *RetainedImage) Release() {
	if r != nil {
		r.once.Do(r.release)
	}
}

// ImageBuildFailure is a returned, known preparation failure. It is not a
// lost reply, caller cancellation, or permission to consume partial output.
type ImageBuildFailure struct{ Cause error }

func (e *ImageBuildFailure) Error() string { return e.Cause.Error() }
func (e *ImageBuildFailure) Unwrap() error { return e.Cause }

// ImageInterrupted is typed private-worker evidence. Retryable is true only
// when the job never received an effect permit; the caller must separately
// prove its request never entered apply and must never prompt on cancellation.
type ImageInterrupted struct {
	JobID     string
	Retryable bool
	Cause     error
}

func (e *ImageInterrupted) Error() string {
	message := "the shared image build was interrupted (job " + e.JobID + ")"
	if !e.Retryable {
		message += "; retry the command"
	}
	if e.Cause == nil {
		return message
	}
	cause := e.Cause.Error()
	if strings.HasPrefix(cause, message) {
		return cause
	}
	return message + ": " + cause
}
func (e *ImageInterrupted) Unwrap() error { return e.Cause }

func (m *ImageJobs) Acquire(ctx context.Context, recipe ImageRecipe) (*RetainedImage, error) {
	return m.AcquireWithStatus(ctx, recipe, BlockerSummary{}, nil)
}
func (m *ImageJobs) AcquireWithStatus(ctx context.Context, recipe ImageRecipe, owner BlockerSummary, progress func(ImageWait)) (*RetainedImage, error) {
	return m.acquireWithProgress(ctx, recipe, owner, progress, nil)
}
func (m *ImageJobs) acquireWithProgress(ctx context.Context, recipe ImageRecipe, owner BlockerSummary, progress func(ImageWait), output *imageProgress) (*RetainedImage, error) {
	stable, err := recipe.Identity()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	// Drop ended tracked attempts before counting running build capacity.
	// Strict residual cleanup evidence remains separately retained.
	if err := m.refreshUncertain(); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	var job *imageJob
	joined := false
	for _, j := range m.jobs {
		if j.phase.StableInput == stable && !j.finished && j.recipe.Force == recipe.Force && (!recipe.Force || !j.building) {
			job = j
			joined = true
			break
		}
	}
	if job == nil {
		idBytes := sha256.Sum256([]byte(rand.Text()))
		id := hex.EncodeToString(idBytes[:])
		attempt := stable
		if recipe.Force {
			h := sha256.New()
			_, _ = h.Write([]byte(stable))
			_, _ = h.Write([]byte(rand.Text()))
			attempt = hex.EncodeToString(h.Sum(nil))
		}
		jobCtx, cancel := context.WithCancel(m.ctx)
		job = &imageJob{owner: owner, ownerCtx: ctx, progress: progress, phase: ImagePhase{PrivateVersion: 1, JobID: id, StableInput: stable, Attempt: attempt, SolveRef: id, Incarnation: m.incarnation, PreparedRef: "yoloctl/prepared/" + id}, recipe: imagejob.Copy(recipe), ctx: jobCtx, cancel: cancel, done: make(chan struct{}), image: output.imageLabel()}
		m.jobs[id] = job
		go m.run(job)
	}
	blocker := jobBlocker(job)
	jobID := job.phase.JobID
	job.interests++
	m.notify()
	m.mu.Unlock()
	if joined {
		output.note("Joining image build " + shortImageJob(jobID))
	}
	unsubscribe := job.log.subscribe(output)
	defer unsubscribe()
	if joined && progress != nil {
		progress(ImageWait{Kind: "image job", JobID: jobID, Blockers: []BlockerSummary{blocker}})
	}
	select {
	case <-ctx.Done():
		m.leave(job)
		return nil, ctx.Err()
	case <-job.done:
	}
	m.mu.Lock()
	result, err := job.result, job.err
	m.mu.Unlock()
	if err != nil {
		m.leave(job)
		return nil, err
	}
	if err := m.backend.Validate(ctx, result); err != nil {
		m.leave(job)
		return nil, fmt.Errorf("validate retained image: %w", err)
	}
	if err := ctx.Err(); err != nil {
		m.leave(job)
		return nil, err
	}
	return &RetainedImage{PinnedImage: result, release: func() { m.leave(job) }}, nil
}

func imageBaseReads(recipe ImageRecipe) []Resource {
	var reads []Resource
	for _, input := range recipe.Images {
		source := input.Source
		if source == "" {
			source = input.Reference
		}
		reads = append(reads, Resource{Kind: ResourceImages, Name: source})
	}
	if len(reads) == 0 && recipe.BaseReference != "" && recipe.BaseReference != "scratch" {
		reads = append(reads, Resource{Kind: ResourceImages, Name: recipe.BaseReference})
	}
	return reads
}

// Caller holds m.mu. Ended outcomes do not reserve build slots; durable
// residual ownership and alias cleanup are independent of this local capacity.
func (m *ImageJobs) refreshUncertain() error {
	for id, j := range m.jobs {
		if j.uncertain && j.finished {
			delete(m.jobs, id)
			m.notify()
		}
	}
	return nil
}

// abandonLocked revokes all future use before requesting best-effort cancel.
// m.mu serializes this with completion; BuildKit responsiveness is irrelevant.
func (m *ImageJobs) abandonLocked(j *imageJob) {
	if !j.phase.AbandonedAt.IsZero() {
		return
	}
	j.phase.AbandonedAt = time.Now().UTC()
	r := j.receipt
	if r.ID != "" {
		server := m.server
		server.mu.Lock()
		// A durable completion wins over a generic abandonment. Local metadata
		// ownership is revoked here; a late Solve may only release private garbage.
		if onDisk, err := server.Receipts.Read(r.ID); err == nil && sameAdmission(onDisk, r) && onDisk.Image != nil && onDisk.Completion != nil {
			r = onDisk
			j.phase.Output, j.phase.Lease = r.Image.Output, r.Image.Lease
		}
		phase := j.phase
		r.Image = &phase
		var err error
		if r.Completion != nil {
			r.Image.Residual = false
		} else if !j.building {
			r.State, r.Status, r.Finished = "complete", 2, time.Now().UTC()
			r.Completion = &Completion{At: r.Finished, Status: 2}
		} else {
			r.State, r.Status, r.Finished = "interrupted", 2, time.Now().UTC()
			r.Image.Residual = true
			err = server.Receipts.SaveImageOrphan(r)
			if err == nil {
				r.Image.OrphanRecorded = true
			}
		}
		j.phase = *r.Image
		j.receipt = r
		if err == nil {
			err = server.saveImageJob(r)
		}
		server.record(r, err, true, false, "")
		_ = server.refreshRecovery()
		server.notifyReceipts()
		server.mu.Unlock()
	}
	delete(m.jobs, j.phase.JobID)
	j.slotted = false
	j.cancel()
	m.notify()
	if j.building && !j.finished {
		phase := j.phase
		go func() {
			_ = m.backend.Release(context.WithoutCancel(m.ctx), PinnedImage{JobID: phase.JobID, Reference: phase.PreparedRef, Lease: "yoloctl-image-" + phase.JobID})
		}()
	}
}

func (m *ImageJobs) leave(j *imageJob) {
	m.mu.Lock()
	j.interests--
	if j.interests != 0 {
		m.mu.Unlock()
		return
	}
	if !j.finished {
		m.abandonLocked(j)
		m.mu.Unlock()
		return
	}
	if j.uncertain {
		m.mu.Unlock()
		return
	}
	if j.phase.AbandonedAt.IsZero() && j.receipt.Completion != nil {
		phase := j.phase
		phase.ReleasedAt = time.Now().UTC()
		r := j.receipt
		r.Image = &phase
		j.phase, j.receipt = phase, r
		m.server.mu.Lock()
		err := m.server.saveImageJob(r)
		m.server.record(r, err, true, false, "")
		_ = m.server.refreshRecovery()
		m.server.notifyReceipts()
		m.server.mu.Unlock()
	}
	delete(m.jobs, j.phase.JobID)
	j.cancel()
	m.notify()
	result := j.result
	m.mu.Unlock()
	if result.Lease != "" {
		_ = m.backend.Release(context.WithoutCancel(m.ctx), result)
	}
}

func (m *ImageJobs) slot(ctx context.Context, j *imageJob) error {
	for {
		m.mu.Lock()
		if err := m.refreshUncertain(); err != nil {
			m.mu.Unlock()
			return err
		}
		used := 0
		for _, other := range m.jobs {
			if other.slotted && (!other.finished || other.uncertain) {
				used++
			}
		}
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return err
		}
		if used < m.capacity && !m.cleaning {
			j.slotted = true
			m.mu.Unlock()
			return nil
		}
		changed := m.changed
		var blockers []BlockerSummary
		report := !j.capacityReported && j.progress != nil
		if report {
			j.capacityReported = true
			for _, other := range m.jobs {
				if other.slotted && (!other.finished || other.uncertain) && len(blockers) < 3 {
					blockers = append(blockers, jobBlocker(other))
				}
			}
		}
		m.mu.Unlock()
		if report {
			j.progress(ImageWait{Kind: "build capacity", Blockers: blockers})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		case <-m.serverReceiptChange():
		}
	}
}
func (m *ImageJobs) serverReceiptChange() <-chan struct{} {
	m.server.mu.Lock()
	defer m.server.mu.Unlock()
	return m.server.receiptsChanged()
}

func (m *ImageJobs) run(j *imageJob) {
	err := m.slot(j.ctx, j)
	m.mu.Lock()
	jobID, preparedRef := j.phase.JobID, j.phase.PreparedRef
	m.mu.Unlock()
	scope := &Scope{Writes: []Resource{{Kind: ResourceImages, Name: preparedRef}}, Reads: imageBaseReads(j.recipe)}
	if err == nil {
		err = m.server.awaitReservations(j.ctx, jobID, scope)
	}
	permitted := false
	if err == nil {
		m.mu.Lock()
		server := m.server
		server.mu.Lock()
		if server.draining || server.nativeStorageFailure != nil {
			err = errors.New("image job admission is closed")
		} else if e := j.ctx.Err(); e != nil {
			err = e
		} else {
			h := server.loadHolds(j.phase.JobID)
			if resources, held, _ := h.conflict(scope); len(held) != 0 {
				err = heldError(server.Hello.Space, true, resources, held)
			} else {
				var r Receipt
				r, err = server.beginImageJob(j.phase.JobID)
				if r.ID != "" {
					j.phase.Stage = "preflight"
					phase := j.phase
					r.ProofVersion, r.EffectPhase, r.Scope, r.Image = 2, EffectStarted, scope, &phase
					j.receipt = r
				}
				if err == nil {
					err = server.saveImageJob(j.receipt)
					permitted = err == nil
				}
				if err != nil {
					server.record(j.receipt, err, false, false, "")
				}
			}
		}
		server.mu.Unlock()
		m.mu.Unlock()
	}
	// This permit covers private preparation only. Deleting cleanup obtains its
	// own durable images-domain transaction through Cleanup before its helper.
	if err == nil {
		err = m.backend.Preflight(j.ctx, j.recipe)
	}
	if err == nil {
		err = m.awaitCleanup(j.ctx)
	}
	if err == nil {
		err = m.server.awaitReservations(j.ctx, jobID, scope)
	}
	var result PinnedImage
	if err == nil {
		m.mu.Lock()
		server := m.server
		server.mu.Lock()
		if j.ctx.Err() != nil {
			err = j.ctx.Err()
		} else if server.draining || server.nativeStorageFailure != nil || m.cleaning {
			err = errors.New("image build admission changed before permission")
		} else {
			h := server.loadHolds(j.phase.JobID)
			if resources, held, _ := h.conflict(scope); len(held) != 0 {
				err = heldError(server.Hello.Space, true, resources, held)
			} else {
				j.phase.Stage = "build"
				phase := j.phase
				j.receipt.Image = &phase
				err = server.saveImageJob(j.receipt)
				if err == nil {
					j.building = true
				} else {
					server.record(j.receipt, err, false, false, "")
				}
			}
		}
		phase := j.phase
		server.mu.Unlock()
		m.mu.Unlock()
		if err == nil {
			stage, executor := "Building image ", imageExecutor
			switch j.recipe.Builder {
			case "pull/1":
				stage, executor = "Pulling image ", "containerd"
			case imagejob.LocalRetainBuilder:
				stage, executor = "Retaining local image ", "containerd"
			}
			title := j.image
			if title == "" {
				title = shortImageJob(jobID)
			}
			j.step = j.log.Logger().StartStep(stage+title, prettylog.Actor(imageActor), prettylog.Executor(executor), slog.String("job", shortImageJob(jobID)))
			target := imageProgressTarget{step: j.step, log: j.log.Logger().Slog().With(prettylog.Actor(imageActor), prettylog.Executor(executor))}
			result, err = m.backend.Build(context.WithValue(j.ctx, imageBuildStepKey{}, target), j.recipe, phase)
			if err == nil {
				err = m.backend.Validate(context.WithoutCancel(j.ctx), result)
			}
		}
	}
	m.mu.Lock()
	var failed *ImageBuildFailure
	knownFailure := j.ctx.Err() == nil && errors.As(err, &failed) && j.phase.AbandonedAt.IsZero()
	if knownFailure {
		r := j.receipt
		r.State, r.Status, r.Finished = "complete", 2, time.Now().UTC()
		r.Completion = &Completion{At: r.Finished, Status: 2}
		j.receipt = r
		m.server.mu.Lock()
		saveErr := m.server.saveImageJob(r)
		m.server.record(r, saveErr, true, false, "")
		_ = m.server.refreshRecovery()
		m.server.notifyReceipts()
		m.server.mu.Unlock()
		if saveErr != nil {
			err = errors.Join(err, saveErr)
			knownFailure = false
		}
	}
	if !j.phase.AbandonedAt.IsZero() {
		err = errors.Join(err, context.Canceled)
	} else if err == nil {
		if result.JobID != j.phase.JobID || result.Reference != j.phase.PreparedRef || result.Lease == "" || digest.Digest(result.Digest).Validate() != nil {
			err = errors.New("build returned an invalid pin")
		}
		if err == nil {
			phase := j.phase
			phase.Output, phase.Lease = result.Digest, result.Lease
			j.phase = phase
			r := j.receipt
			r.Image = &phase
			r.State, r.Status, r.Finished = "complete", 0, time.Now().UTC()
			r.Completion = &Completion{At: r.Finished, Status: 0}
			j.receipt = r
			server := m.server
			server.mu.Lock()
			err = server.saveImageJob(r)
			server.record(r, err, true, false, "")
			_ = server.refreshRecovery()
			server.notifyReceipts()
			server.mu.Unlock()
		}
	}
	if err != nil && !knownFailure && j.phase.AbandonedAt.IsZero() {
		m.abandonLocked(j)
	}
	// No local receipt updates survive abandonment or this finalization. A
	// lingering service-side solve has no publication or metadata authority.
	if j.receipt.ID != "" {
		m.server.mu.Lock()
		m.server.record(j.receipt, nil, true, false, "")
		m.server.mu.Unlock()
	}
	j.result, j.finished = result, true
	if knownFailure {
		j.err = fmt.Errorf("image preparation failed (job %s): %w", j.phase.JobID, err)
	} else if err != nil {
		j.err = &ImageInterrupted{JobID: j.phase.JobID, Retryable: !permitted, Cause: err}
	}
	j.endSteps(knownFailure)
	close(j.done)
	m.notify()
	orphan := j.interests == 0 || !j.phase.AbandonedAt.IsZero()
	if orphan {
		delete(m.jobs, j.phase.JobID)
	}
	m.mu.Unlock()
	if orphan && result.Lease != "" {
		_ = m.backend.Release(context.WithoutCancel(m.ctx), result)
	}
}

// Cleanup serializes disposable storage work against active image effects.
// Waiting happens before the callback acquires its brief native space lock.
// Private abandoned solves have no publication authority and BuildKit retains
// its own active resources; their cancellation never delays this admission.

func (m *ImageJobs) awaitCleanup(ctx context.Context) error {
	for {
		m.mu.Lock()
		busy, changed := m.cleaning, m.changed
		m.mu.Unlock()
		if !busy {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (m *ImageJobs) RetainedReferences() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var refs []string
	for _, j := range m.jobs {
		for _, input := range imageBaseReads(j.recipe) {
			refs = append(refs, input.Name)
		}
		refs = append(refs, j.phase.PreparedRef)
	}
	return refs
}

// endSteps ends the job's open steps with its result: ok, a known build
// failure, or an uncertain (interrupted) outcome in the existing wording.
// Steps already ended are unchanged (end calls are idempotent).
func (j *imageJob) endSteps(knownFailure bool) {
	for _, s := range []*prettylog.Step{j.step} {
		switch {
		case s == nil:
		case j.err == nil:
			s.Done()
		case knownFailure:
			s.Fail(j.err)
		default:
			s.Fail(j.err)
		}
	}
}

// shortImageJob is the job ID shown in progress (the full ID stays in
// receipts, errors and ImageInterrupted).
func shortImageJob(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func jobBlocker(j *imageJob) BlockerSummary {
	if j.ownerCtx != nil && j.ownerCtx.Err() == nil && j.owner.Method != "" {
		return j.owner
	}
	return BlockerSummary{Method: "image.job"}
}

func imageBootIdentity(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size && hex.EncodeToString(raw) == value
}
