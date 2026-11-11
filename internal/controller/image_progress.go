package controller

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/leogr/yoloctl/pkg/prettylog"
)

const imageProgressLimit = 1 << 20
const imageProgressInterval = 2 * time.Second

// imageRecentRecords bounds the non-step records (warnings, notices) a job
// keeps for a late follower; steps are replayed from the job's Tracker.
const imageRecentRecords = 32

// Fixed actor/executor names of image progress (the internal/logging
// vocabulary values; that package is not imported by the controller core).
const (
	imageActor    = "builder"
	imageExecutor = "buildkit"
)

type progressItem struct {
	raw   []byte
	lossy bool
	src   *progressSource // the job the record came from; nil for request notes
}

// progressSource is one followed job, as one request's queue sees it.
type progressSource struct {
	log *imageBuildLog
	// dropping: the queue overflowed after this job's records were queued;
	// its live records are discarded until its replay is captured, under the
	// job's publication lock, as one snapshot/live cut.
	dropping bool
	done     bool // unsubscribed: no more live records
	queued   int  // this job's records waiting in the queue
}

// imageProgress is one request's bounded queue of encoded log records and its
// sole progress sender. Producers never perform network I/O. For a slow
// client, lossy detail records (executor output) are dropped first. If the
// queue is still over its limit, it is emptied: each followed job's live
// records are then discarded until the sender, with an empty queue, captures
// that job's replay (its step-less records, Tracker snapshot and end marker)
// under the job's lock and queues it within the limit. Every tracked step's
// current state is delivered; superseded transitions, older step-less records
// and detail lines that do not fit are not, and a notice counts what was
// skipped. Its context is the public connection's lifetime, whose
// cancellation closes the connection and unblocks a writer.
type imageProgress struct {
	mu       sync.Mutex
	items    []progressItem
	bytes    int
	skipped  int
	records  int // non-detail records discarded by an overflow
	sources  map[*imageBuildLog]*progressSource
	order    []*progressSource
	finished bool
	phase    string
	image    string
	// root is this request's image step ("Image for box X …"): every job
	// step the request follows, live or replayed, is shown under it, so a
	// build ends as one line. The ID stays set to the end, so replays still
	// owed when the request ends keep their parent. rootRecord is its
	// running record, resent before any job replay when a structural
	// overflow dropped it (rootResend). Its end is decided when the request
	// ends (rootEnding: ready when rootOK) and sent last, once nothing is
	// queued or owed, titled with the first failed job step's rootErr.
	root                      string
	rootSubject, rootFeatures string
	rootStarted               time.Time
	rootRecord                slog.Record
	rootResend                bool
	rootEnding, rootOK        bool
	rootEnded                 bool
	rootTook                  time.Duration
	rootErr                   string
	started                   time.Time
	wake                      chan struct{}
	done                      chan struct{}
}

// newImageProgress sends each record as one Write of its encoded bytes to out
// (a "log" frame writer).
func newImageProgress(ctx context.Context, out io.Writer) *imageProgress {
	p := &imageProgress{wake: make(chan struct{}, 1), done: make(chan struct{}), started: time.Now(), sources: map[*imageBuildLog]*progressSource{}}
	go p.run(ctx, out)
	return p
}

func (p *imageProgress) notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Handle queues one request-level record. It never blocks on the client.
func (p *imageProgress) Handle(_ context.Context, r slog.Record) error {
	raw, err := prettylog.MarshalRecord(r, prettylog.Limits{})
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pushLocked(progressItem{raw: raw, lossy: prettylog.IsLossy(r)})
	return nil
}

// fromJob queues one record a job published. Called with the job's lock held.
func (p *imageProgress) fromJob(l *imageBuildLog, r slog.Record) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r = p.underRootLocked(r)
	raw, err := prettylog.MarshalRecord(r, prettylog.Limits{})
	if err != nil {
		return
	}
	src := p.sources[l]
	if src == nil {
		return
	}
	item := progressItem{raw: raw, lossy: prettylog.IsLossy(r), src: src}
	if src.dropping {
		p.discardLocked(item) // covered by this job's replay, captured later
		return
	}
	p.pushLocked(item)
}

func (p *imageProgress) discardLocked(it progressItem) {
	p.skipped += len(it.raw)
	if !it.lossy {
		p.records++
	}
}

func (p *imageProgress) pushLocked(item progressItem) {
	if p.finished {
		return
	}
	p.items = append(p.items, item)
	p.bytes += len(item.raw)
	if item.src != nil {
		item.src.queued++
	}
	if p.bytes > imageProgressLimit {
		// Drop the oldest lossy records down to 3/4 of the limit in one pass.
		kept := p.items[:0]
		for _, it := range p.items {
			if it.lossy && p.bytes > imageProgressLimit*3/4 {
				p.discardLocked(it)
				p.bytes -= len(it.raw)
				if it.src != nil {
					it.src.queued--
				}
				continue
			}
			kept = append(kept, it)
		}
		clear(p.items[len(kept):])
		p.items = kept
	}
	if p.bytes > imageProgressLimit {
		// Only structural records remain over the limit: drop them all and
		// resend each followed job's current state instead.
		for _, it := range p.items {
			p.discardLocked(it)
		}
		p.items, p.bytes = nil, 0
		p.rootResend = p.root != ""
		for _, src := range p.order {
			src.queued = 0
			src.dropping = true
		}
		p.releaseLocked()
	}
	p.notify()
}

// releaseLocked forgets unsubscribed jobs that owe this client nothing more.
func (p *imageProgress) releaseLocked() {
	kept := p.order[:0]
	for _, src := range p.order {
		if src.done && src.queued == 0 && !src.dropping {
			delete(p.sources, src.log)
			continue
		}
		kept = append(kept, src)
	}
	clear(p.order[len(kept):])
	p.order = kept
}

// follow registers a job as a source. Called with the job's lock held.
func (p *imageProgress) follow(l *imageBuildLog) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sources[l] == nil {
		src := &progressSource{log: l}
		p.sources[l] = src
		p.order = append(p.order, src)
	}
}

// unfollow ends a job's live records. Called with the job's lock held.
func (p *imageProgress) unfollow(l *imageBuildLog) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if src := p.sources[l]; src != nil {
		src.done = true
		p.releaseLocked()
	}
}

// resyncOne captures one dropping job's replay, if the queue is empty, and
// queues it within the limit. The job lock is taken before p.mu, as in
// publication, so the capture and the end of dropping are one cut.
func (p *imageProgress) resyncOne() {
	p.mu.Lock()
	if len(p.items) == 0 && p.rootResend {
		// The root first, so every replayed job step has its parent.
		p.rootResend = false
		if raw, err := prettylog.MarshalRecord(p.rootRecord, prettylog.Limits{}); err == nil {
			p.items, p.bytes = append(p.items, progressItem{raw: raw}), len(raw)
		}
		p.mu.Unlock()
		return
	}
	var src *progressSource
	if len(p.items) == 0 {
		for _, s := range p.order {
			if s.dropping {
				src = s
				break
			}
		}
	}
	p.mu.Unlock()
	if src == nil {
		return
	}
	src.log.mu.Lock()
	defer src.log.mu.Unlock()
	replay := src.log.replayLocked()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !src.dropping {
		return
	}
	for i, r := range replay {
		replay[i] = p.underRootLocked(r)
	}
	// The room is rechecked here: request notes may have been queued while
	// this waited for the job lock.
	items, ok := encodeReplay(replay, src, imageProgressLimit-p.bytes, p)
	if !ok {
		return // keep dropping; capture again once the queue is empty
	}
	src.dropping = false
	for _, it := range items {
		p.items = append(p.items, it)
		p.bytes += len(it.raw)
		src.queued++
	}
	p.releaseLocked()
}

// encodeReplay encodes one job's replay within budget bytes, or reports
// that even its most compact form does not fit (the caller retries with an
// empty queue). Every tracked step's state record and the markers are kept;
// when they do not fit at full size, step records are compacted (title, err
// and reason cut, ordinary fields dropped) to 256 and then 32 bytes per
// value. Step-less records, then each step's tail lines, are added newest
// first while they fit. Everything left out is counted in the skip notice,
// and a step's left-out lines are added to its omitted bytes, as the Tracker
// counts them.
func encodeReplay(replay []slog.Record, src *progressSource, budget int, p *imageProgress) ([]progressItem, bool) {
	const (
		kindStep = iota
		kindMarker
		kindRecent
		kindTail
	)
	type entry struct {
		r     slog.Record
		raw   []byte
		kind  int
		keep  bool
		owner int // index of the step record a tail line belongs to
	}
	entries := make([]entry, 0, len(replay))
	owner := -1
	for _, r := range replay {
		raw, err := prettylog.MarshalRecord(r, prettylog.Limits{})
		if err != nil {
			continue
		}
		st := prettylog.StructureOf(r)
		e := entry{r: r, raw: raw, owner: owner}
		switch {
		case st.Snapshot && st.Detail && st.Step != "":
			e.kind = kindTail
		case st.Snapshot && st.Step != "":
			e.kind = kindStep
			owner = len(entries)
		case st.Snapshot:
			e.kind = kindMarker
		default:
			e.kind = kindRecent
		}
		entries = append(entries, e)
	}
	full := make([]int, len(entries))
	for i, e := range entries {
		full[i] = len(e.raw)
	}
	// Mandatory records, compacted until they fit.
	used := 0
	for _, limit := range []int{0, 256, 32} {
		used = 0
		for i := range entries {
			e := &entries[i]
			if e.kind != kindStep && e.kind != kindMarker {
				continue
			}
			if limit > 0 && e.kind == kindStep {
				if raw, err := prettylog.MarshalRecord(compactStep(e.r, limit), prettylog.Limits{}); err == nil {
					e.r, e.raw = compactStep(e.r, limit), raw
				}
			}
			e.keep = true
			used += len(e.raw)
			if e.kind == kindStep {
				used += omittedGrowth // room for a larger omitted count
			}
		}
		if used <= budget {
			break
		}
	}
	if used > budget {
		return nil, false
	}
	// Optional records, newest first: step-less records, then tail lines.
	for _, kind := range []int{kindRecent, kindTail} {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].kind == kind && used+len(entries[i].raw) <= budget {
				entries[i].keep = true
				used += len(entries[i].raw)
			}
		}
	}
	dropped := map[int]int64{}
	for i, e := range entries {
		if e.keep {
			if cut := full[i] - len(e.raw); cut > 0 {
				p.skipped += cut // shortened by compaction
			}
			continue
		}
		p.discardLocked(progressItem{raw: e.raw, lossy: e.kind == kindTail})
		if e.kind == kindTail {
			dropped[e.owner] += int64(len(e.r.Message) + 1)
		}
	}
	out := make([]progressItem, 0, len(entries))
	for i, e := range entries {
		if !e.keep {
			continue
		}
		raw := e.raw
		if n := dropped[i]; n > 0 {
			if more, err := prettylog.MarshalRecord(withOmitted(e.r, n), prettylog.Limits{}); err == nil {
				raw = more
			}
		}
		out = append(out, progressItem{raw: raw, src: src})
	}
	return out, true
}

// compactStep is a snapshot step record with its title, err and reason cut
// to limit bytes and its ordinary fields dropped; its identity, parentage,
// state, timing, omitted bytes and actor are kept.
func compactStep(r slog.Record, limit int) slog.Record {
	out := slog.NewRecord(r.Time, r.Level, cutText(r.Message, limit), 0)
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case prettylog.KeyErr, prettylog.KeyReason:
			out.AddAttrs(slog.String(a.Key, cutText(a.Value.String(), limit)))
		case prettylog.KeyStep, prettylog.KeyParent, prettylog.KeyState, prettylog.KeyStarted, prettylog.KeyTook,
			prettylog.KeyOmitted, prettylog.KeySnapshot, prettylog.KeyActor, prettylog.KeyExecutor:
			out.AddAttrs(a)
		}
		return true
	})
	return out
}

// cutText cuts s to at most n bytes at a rune boundary, marking the cut.
func cutText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:max(cut, 0)] + "…"
}

// omittedGrowth bounds how much a step record's encoding can grow when
// withOmitted raises its omitted count (at most 20 more digits).
const omittedGrowth = 20

// withOmitted is a snapshot step record with more omitted detail bytes.
func withOmitted(r slog.Record, more int64) slog.Record {
	out := slog.NewRecord(r.Time, r.Level, r.Message, 0)
	omitted := prettylog.StructureOf(r).Omitted
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != prettylog.KeyOmitted {
			out.AddAttrs(a)
		}
		return true
	})
	out.AddAttrs(slog.Int64(prettylog.KeyOmitted, omitted+more))
	return out
}

func imageRecord(level slog.Level, msg string, attrs ...slog.Attr) slog.Record {
	r := slog.NewRecord(time.Now(), level, msg, 0)
	r.AddAttrs(prettylog.Actor(imageActor))
	r.AddAttrs(attrs...)
	return r
}

// underRootLocked shows a job's top step under this request's root step and
// remembers a failed one's error for the root.
func (p *imageProgress) underRootLocked(r slog.Record) slog.Record {
	st := prettylog.StructureOf(r)
	if p.root == "" || st.Step == "" || st.Parent != "" {
		return r
	}
	if st.State == string(prettylog.StepFailed) && p.rootErr == "" {
		p.rootErr = st.Err
	}
	out := r.Clone()
	out.AddAttrs(slog.String(prettylog.KeyParent, p.root)) // the last parent wins
	return out
}

// note is request-level progress text: a detail line of the root step (seen
// while it runs, and in a failure's output), or an ordinary line before it.
func (p *imageProgress) note(msg string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	root := p.root
	p.mu.Unlock()
	if root == "" {
		_ = p.Handle(context.Background(), imageRecord(prettylog.LevelInfo, msg))
		return
	}
	_ = p.Handle(context.Background(), imageRecord(prettylog.LevelDebug, msg, slog.String(prettylog.KeyStep, root), slog.Bool(prettylog.KeyDetail, true)))
}

// preparing reports the image a request is preparing: subject ("Image for
// box X", or "Image <reference>") and features (" (features: …)" or "").
// The first call starts the request's root step; later ones (a base image,
// the composed image again) only update the heartbeat and build titles.
func (p *imageProgress) preparing(description, image, subject, features string) {
	p.mu.Lock()
	p.phase, p.image = description, image
	start := p.root == ""
	if start {
		var id [8]byte
		_, _ = rand.Read(id[:])
		p.root, p.rootSubject, p.rootFeatures, p.rootStarted = "image."+hex.EncodeToString(id[:]), subject, features, time.Now()
	}
	if !start {
		p.mu.Unlock()
		return
	}
	p.rootRecord = imageRecord(prettylog.LevelInfo, subject+features,
		slog.String(prettylog.KeyStep, p.root), slog.String(prettylog.KeyState, string(prettylog.StepRunning)))
	if raw, err := prettylog.MarshalRecord(p.rootRecord, prettylog.Limits{}); err == nil {
		p.pushLocked(progressItem{raw: raw})
	}
	p.mu.Unlock()
}

// rootEndLocked is the root's end record: ready, failed with a job's error,
// or uncertain when the request ended before its images were prepared.
func (p *imageProgress) rootEndLocked() slog.Record {
	attrs := []slog.Attr{slog.String(prettylog.KeyStep, p.root), prettylog.Took(p.rootTook)}
	subject, features := p.rootSubject, p.rootFeatures
	switch {
	case p.rootOK:
		return imageRecord(prettylog.LevelInfo, subject+" ready"+features, append(attrs, slog.String(prettylog.KeyState, string(prettylog.StepOK)))...)
	case p.rootErr != "":
		return imageRecord(prettylog.LevelError, subject+features, append(attrs, slog.String(prettylog.KeyState, string(prettylog.StepFailed)), slog.String(prettylog.KeyErr, p.rootErr))...)
	default:
		return imageRecord(prettylog.LevelWarn, subject+features, append(attrs, slog.String(prettylog.KeyState, string(prettylog.StepUnknown)), slog.String(prettylog.KeyErr, "did not finish before the command ended"))...)
	}
}

// prepared ends the root as ready and drains the queue (the "prepared"
// action: every image of the request is in place).
func (p *imageProgress) prepared() { p.end(true) }

// imageLabel is the image of the last preparing notice, or "".
func (p *imageProgress) imageLabel() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.image
}

// idle is the quiet-preparation heartbeat. It is a Debug record: it keeps
// the transport active as before, and shows only at debug (an interactive
// steps view shows the running step's elapsed time instead).
func (p *imageProgress) idle(now, last time.Time) []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.finished || p.phase == "" || now.Sub(last) < imageProgressInterval {
		return nil
	}
	raw, _ := prettylog.MarshalRecord(imageRecord(prettylog.LevelDebug, fmt.Sprintf("Still preparing image %s (%s elapsed)", p.phase, now.Sub(p.started).Round(time.Second))), prettylog.Limits{})
	return raw
}

func (p *imageProgress) next() ([]byte, bool) {
	p.resyncOne() // takes job locks; never under p.mu
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.skipped > 0 {
		n, records := p.skipped, p.records
		p.skipped, p.records = 0, 0
		level, msg := prettylog.LevelDebug, fmt.Sprintf("[image progress: skipped %d bytes of build output for this slow client]", n)
		if records > 0 {
			level, msg = prettylog.LevelNotice, fmt.Sprintf("[image progress: this client fell behind; skipped %d bytes (%d records); resending the current steps]", n, records)
		}
		raw, _ := prettylog.MarshalRecord(imageRecord(level, msg), prettylog.Limits{})
		return raw, false
	}
	if len(p.items) > 0 {
		item := p.items[0]
		p.items[0] = progressItem{}
		p.items = p.items[1:]
		p.bytes -= len(item.raw)
		if item.src != nil {
			item.src.queued--
			p.releaseLocked()
		}
		return item.raw, false
	}
	for _, src := range p.order {
		if src.dropping {
			return nil, false // a replay is still owed; the sender loops again
		}
	}
	if p.rootResend {
		return nil, false
	}
	if p.finished && p.rootEnding && !p.rootEnded {
		// Last: after everything queued and every owed replay.
		p.rootEnded = true
		raw, _ := prettylog.MarshalRecord(p.rootEndLocked(), prettylog.Limits{})
		return raw, false
	}
	return nil, p.finished
}

func (p *imageProgress) run(ctx context.Context, out io.Writer) {
	defer func() {
		p.mu.Lock()
		p.finished = true
		p.items = nil
		p.bytes = 0
		p.mu.Unlock()
		close(p.done)
	}()
	timer := time.NewTimer(imageProgressInterval)
	defer timer.Stop()
	last := time.Now()
	for {
		if ctx.Err() != nil {
			return
		}
		data, finished := p.next()
		if len(data) > 0 {
			if _, err := out.Write(data); err != nil {
				return
			}
			last = time.Now()
			timer.Reset(imageProgressInterval)
			continue
		}
		if finished {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
		case now := <-timer.C:
			if raw := p.idle(now, last); raw != nil {
				if _, err := out.Write(raw); err != nil {
					return
				}
				last = now
			}
			timer.Reset(imageProgressInterval)
		}
	}
}

// finish drains every queued record to the client before the final response.
// A root step still open (no "prepared") ends as failed or uncertain, last.
func (p *imageProgress) finish() { p.end(false) }

// end stops queueing new records, decides the root's end (sent after every
// queued record and owed replay) and waits for the sender to drain.
func (p *imageProgress) end(ok bool) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.endLocked(ok)
	p.mu.Unlock()
	p.notify()
	<-p.done
}

func (p *imageProgress) endLocked(ok bool) {
	if p.root != "" && !p.rootEnding {
		p.rootEnding, p.rootOK, p.rootTook = true, ok, time.Since(p.rootStarted)
	}
	p.finished = true
}

// imageBuildLog is one job's progress: its step Tracker, its recent non-step
// records and its followers. Registering a follower and taking its snapshot
// is atomic with publication (one lock), so a follower sees every step
// exactly from the snapshot on; monotonic step states make a repeated live
// transition harmless. No client I/O happens under this lock: each follower
// feeds its own independent bounded request queue.
type imageBuildLog struct {
	mu          sync.Mutex
	tracker     *prettylog.Tracker
	recent      []slog.Record
	subscribers map[*imageProgress]struct{}

	loggerOnce sync.Once
	logger     *prettylog.Logger
	pending    []byte // partial frame; Logger writes are serialized by its mutex
}

// Logger is the job's producer front end: steps and records written through
// it are published to the Tracker and every follower.
func (l *imageBuildLog) Logger() *prettylog.Logger {
	l.loggerOnce.Do(func() {
		l.logger = prettylog.New(imageBuildSink{l}, prettylog.Options{Format: prettylog.FormatWire, Verbosity: prettylog.Trace})
	})
	return l.logger
}

// imageBuildSink decodes the Logger's framed records back into records. The
// encoding round trip keeps one canonical record form (caps, escaping of
// reserved attrs) for the Tracker, the followers and the wire.
type imageBuildSink struct{ l *imageBuildLog }

func (s imageBuildSink) Write(p []byte) (int, error) {
	l := s.l
	l.pending = append(l.pending, p...)
	for len(l.pending) >= 4 {
		n := int(binary.BigEndian.Uint32(l.pending[:4]))
		if len(l.pending) < 4+n {
			break
		}
		if r, err := prettylog.UnmarshalRecord(l.pending[4 : 4+n]); err == nil {
			l.publish(r)
		}
		l.pending = l.pending[4+n:]
	}
	if len(l.pending) == 0 {
		l.pending = nil
	}
	return len(p), nil
}

func (l *imageBuildLog) publish(r slog.Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.tracker == nil {
		l.tracker = prettylog.NewTracker(prettylog.TrackerLimits{})
	}
	// Step records (including those of steps past the Tracker's limit) are
	// never "recent": only the Tracker replays steps.
	if st := prettylog.StructureOf(r); !l.tracker.Apply(r) && st.Step == "" && !st.Snapshot {
		l.recent = append(l.recent, r)
		if len(l.recent) > imageRecentRecords {
			l.recent = l.recent[len(l.recent)-imageRecentRecords:]
		}
	}
	for p := range l.subscribers {
		p.fromJob(l, r)
	}
}

// snapshotEnd closes a replayed snapshot: a step-less snapshot record (the
// Tracker ignores it) that tells a host view the replay is complete.
func snapshotEnd() slog.Record {
	r := imageRecord(prettylog.LevelDebug, "build steps replayed", slog.Bool(prettylog.KeySnapshot, true))
	return r
}

// replayLocked is the job's current state for a follower: its last
// step-less records, the Tracker snapshot and the snapshot end marker.
func (l *imageBuildLog) replayLocked() []slog.Record {
	out := append([]slog.Record(nil), l.recent...)
	if l.tracker != nil {
		out = append(out, l.tracker.Snapshot()...)
	}
	return append(out, snapshotEnd())
}

func (l *imageBuildLog) subscribe(p *imageProgress) func() {
	if p == nil {
		return func() {}
	}
	l.mu.Lock()
	if l.subscribers == nil {
		l.subscribers = make(map[*imageProgress]struct{})
	}
	p.follow(l)
	for _, r := range l.replayLocked() {
		p.fromJob(l, r)
	}
	l.subscribers[p] = struct{}{}
	l.mu.Unlock()
	return func() {
		l.mu.Lock()
		delete(l.subscribers, p)
		p.unfollow(l)
		l.mu.Unlock()
	}
}

// imageBuildStepKey carries an imageProgressTarget into a job's build.
type imageBuildStepKey struct{}
