package operator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

const (
	NativePhaseMaxEvents = 256
	NativePhaseMaxBytes  = 64 << 10
)

type NativePhaseRole string
type NativePhaseOutcome string
type NativePhaseKind string
type NativePhaseLoss string

const (
	NativePhaseOriginal  NativePhaseRole    = "original"
	NativePhaseDesired   NativePhaseRole    = "desired"
	NativePhaseRollback  NativePhaseRole    = "rollback"
	NativePhaseOK        NativePhaseOutcome = "ok"
	NativePhaseFailed    NativePhaseOutcome = "failed"
	NativePhaseUncertain NativePhaseOutcome = "uncertain"
	NativePhaseCanceled  NativePhaseOutcome = "canceled"
	NativePhasePending   NativePhaseOutcome = "pending"

	NativePhaseRestartBegin            NativePhaseKind = "restart_begin"
	NativePhaseOldStopReturn           NativePhaseKind = "old_stop_return"
	NativePhaseLaunchAttempt           NativePhaseKind = "launch_attempt"
	NativePhaseLaunchReturn            NativePhaseKind = "launch_return"
	NativePhasePrepareReturn           NativePhaseKind = "prepare_return"
	NativePhaseEngineStartReturn       NativePhaseKind = "engine_start_return"
	NativePhaseActivationReturn        NativePhaseKind = "activation_return"
	NativePhaseSSHReturn               NativePhaseKind = "ssh_return"
	NativePhaseForwardStartReturn      NativePhaseKind = "forward_start_return"
	NativePhaseForwardReadyReturn      NativePhaseKind = "forward_ready_return"
	NativePhaseForwardCleanupReturn    NativePhaseKind = "forward_cleanup_return"
	NativePhaseGenerationCleanupReturn NativePhaseKind = "generation_cleanup_return"
	NativePhaseRestartReturn           NativePhaseKind = "restart_return"

	NativePhaseLossNone        NativePhaseLoss = "none"
	NativePhaseLossUnavailable NativePhaseLoss = "unavailable"
	NativePhaseLossOverflow    NativePhaseLoss = "overflow"
	NativePhaseLossIdentity    NativePhaseLoss = "identity"
	NativePhaseLossMalformed   NativePhaseLoss = "malformed"
	NativePhaseLossMissingEnd  NativePhaseLoss = "missing_end"
)

type NativePhaseForward struct {
	Unit       string `json:"unit"`
	HostPort   int    `json:"host_port"`
	TargetPort int    `json:"target_port"`
	Attempt    string `json:"attempt"`
}

// Events contain only fixed observations. They cannot carry error bodies,
// policy, environment, endpoints, credentials, callbacks or effect authority.
type NativePhaseEvent struct {
	Sequence   int                 `json:"sequence"`
	Kind       NativePhaseKind     `json:"kind"`
	Role       NativePhaseRole     `json:"role"`
	Outcome    NativePhaseOutcome  `json:"outcome"`
	CID        string              `json:"cid"`
	Generation string              `json:"generation"`
	Attempt    int                 `json:"attempt"`
	Forward    *NativePhaseForward `json:"forward,omitempty"`
}

type NativePhaseTerminal struct {
	Count        int                `json:"count"`
	LastSequence int                `json:"last_sequence"`
	Complete     bool               `json:"complete"`
	Disposition  NativePhaseOutcome `json:"disposition"`
	Loss         NativePhaseLoss    `json:"loss"`
}

// A complete transcript describes observations, including failed commands.
// It is neither G6 bind-cause qualification nor proof for receipt recovery.
type NativePhaseTranscript struct {
	Schema             int                 `json:"schema"`
	Box                string              `json:"box"`
	CID                string              `json:"cid"`
	OriginalGeneration string              `json:"original_generation"`
	Events             []NativePhaseEvent  `json:"events"`
	Terminal           NativePhaseTerminal `json:"terminal"`
}

type NativePhaseCollector struct {
	mu             sync.Mutex
	trace          NativePhaseTranscript
	span           *nativePhaseSpan
	seen, finished bool
	loss           NativePhaseLoss
	disposition    NativePhaseOutcome
}

type nativePhaseSpan struct {
	collector          *NativePhaseCollector
	box, cid, original string
}
type nativePhaseContext struct {
	span *nativePhaseSpan
	role NativePhaseRole
}
type nativePhaseCollectorKey struct{}
type nativePhaseContextKey struct{}

func NewNativePhaseCollector() *NativePhaseCollector {
	return &NativePhaseCollector{loss: NativePhaseLossNone, disposition: NativePhasePending}
}

func WithNativePhaseCollector(ctx context.Context, collector *NativePhaseCollector) context.Context {
	return context.WithValue(ctx, nativePhaseCollectorKey{}, collector)
}

func BeginNativePhase(ctx context.Context, box, cid, originalGeneration string) context.Context {
	c, _ := ctx.Value(nativePhaseCollectorKey{}).(*NativePhaseCollector)
	if c == nil {
		return ctx
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen {
		c.lose(NativePhaseLossIdentity)
		return ctx
	}
	c.seen = true
	if c.disposition == "" {
		c.disposition = NativePhasePending
	}
	if !validNativePhaseHeader(box, cid, originalGeneration) {
		c.lose(NativePhaseLossIdentity)
		return ctx
	}
	c.span = &nativePhaseSpan{c, strings.Clone(box), strings.Clone(cid), strings.Clone(originalGeneration)}
	c.trace = NativePhaseTranscript{Schema: 1, Box: c.span.box, CID: c.span.cid, OriginalGeneration: c.span.original}
	c.append(NativePhaseEvent{Kind: NativePhaseRestartBegin, Role: NativePhaseOriginal, Outcome: NativePhasePending, CID: c.span.cid, Generation: c.span.original})
	return context.WithValue(ctx, nativePhaseContextKey{}, nativePhaseContext{c.span, NativePhaseOriginal})
}

func NativePhaseActive(ctx context.Context) bool {
	p, ok := ctx.Value(nativePhaseContextKey{}).(nativePhaseContext)
	if !ok || p.span == nil || p.span.collector == nil {
		return false
	}
	c := p.span.collector
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.span == p.span && !c.finished
}

func WithNativePhaseRole(ctx context.Context, role NativePhaseRole) context.Context {
	p, ok := ctx.Value(nativePhaseContextKey{}).(nativePhaseContext)
	if !ok || p.span == nil {
		return ctx
	}
	if nativePhaseAttempt(role) < 0 {
		p.span.collector.mu.Lock()
		p.span.collector.lose(NativePhaseLossIdentity)
		p.span.collector.mu.Unlock()
		return ctx
	}
	p.role = role
	return context.WithValue(ctx, nativePhaseContextKey{}, p)
}

func RecordNativePhase(ctx context.Context, event NativePhaseEvent) {
	p, ok := ctx.Value(nativePhaseContextKey{}).(nativePhaseContext)
	if !ok || p.span == nil {
		return
	}
	c := p.span.collector
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.span != p.span || c.finished {
		c.lose(NativePhaseLossIdentity)
		return
	}
	if event.Kind == NativePhaseRestartBegin || event.Kind == NativePhaseRestartReturn {
		c.lose(NativePhaseLossMalformed)
		return
	}
	if event.CID != "" && event.CID != p.span.cid || event.Role != "" && event.Role != p.role || event.Attempt != 0 && event.Attempt != nativePhaseAttempt(p.role) {
		c.lose(NativePhaseLossIdentity)
		return
	}
	event.CID, event.Role, event.Attempt = p.span.cid, p.role, nativePhaseAttempt(p.role)
	if p.role == NativePhaseOriginal && event.Generation != "" && event.Generation != p.span.original {
		c.lose(NativePhaseLossIdentity)
		return
	}
	if p.role == NativePhaseOriginal && event.Generation == "" {
		event.Generation = p.span.original
	}
	if event.Kind == NativePhaseLaunchReturn && event.Generation == "" {
		for _, prior := range c.trace.Events {
			if prior.Role == p.role && prior.Kind == NativePhasePrepareReturn {
				event.Generation = prior.Generation
			}
		}
	}
	if p.role != NativePhaseOriginal && event.Generation != "" && event.Kind != NativePhaseLaunchAttempt {
		for _, prior := range c.trace.Events {
			if prior.Kind != NativePhasePrepareReturn || prior.Generation == "" {
				continue
			}
			if prior.Role == p.role && event.Generation != prior.Generation || prior.Role != p.role && event.Generation == prior.Generation {
				c.lose(NativePhaseLossIdentity)
				return
			}
		}
		if event.Generation == p.span.original {
			c.lose(NativePhaseLossIdentity)
			return
		}
	}
	c.append(event)
}

func FinishNativePhase(ctx context.Context, outcome NativePhaseOutcome) {
	p, ok := ctx.Value(nativePhaseContextKey{}).(nativePhaseContext)
	if !ok || p.span == nil {
		return
	}
	c := p.span.collector
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.span != p.span || c.finished {
		c.lose(NativePhaseLossIdentity)
		return
	}
	if !validNativePhaseOutcome(outcome, false) {
		c.lose(NativePhaseLossMalformed)
	} else {
		c.disposition = outcome
		c.append(NativePhaseEvent{Kind: NativePhaseRestartReturn, Role: NativePhaseOriginal, CID: p.span.cid, Generation: p.span.original, Outcome: outcome})
	}
	c.finished = true
}

func MarkNativePhaseIncomplete(ctx context.Context) {
	p, ok := ctx.Value(nativePhaseContextKey{}).(nativePhaseContext)
	if !ok || p.span == nil {
		return
	}
	p.span.collector.mu.Lock()
	p.span.collector.lose(NativePhaseLossUnavailable)
	p.span.collector.mu.Unlock()
}

func (c *NativePhaseCollector) lose(reason NativePhaseLoss) {
	if c.loss == "" || c.loss == NativePhaseLossNone {
		c.loss = reason
	}
}

// append runs only bounded memory operations. Once capture loses a fact, later
// callbacks cannot synthesize a complete suffix or resume collection.
func (c *NativePhaseCollector) append(event NativePhaseEvent) {
	if c.loss != "" && c.loss != NativePhaseLossNone {
		return
	}
	if len(c.trace.Events) >= NativePhaseMaxEvents {
		c.lose(NativePhaseLossOverflow)
		return
	}
	if event.Sequence != 0 && event.Sequence != len(c.trace.Events)+1 {
		c.lose(NativePhaseLossMalformed)
		return
	}
	event.Sequence = len(c.trace.Events) + 1
	if !validNativePhaseEvent(c.trace, event) {
		c.lose(NativePhaseLossMalformed)
		return
	}
	event.CID, event.Generation = strings.Clone(event.CID), strings.Clone(event.Generation)
	if event.Forward != nil {
		copy := *event.Forward
		copy.Unit, copy.Attempt = strings.Clone(copy.Unit), strings.Clone(copy.Attempt)
		event.Forward = &copy
	}
	candidate := c.trace
	candidate.Events = append(candidate.Events, event)
	if _, err := validateNativePhaseEvents(candidate); err != nil {
		c.lose(NativePhaseLossMalformed)
		return
	}
	// Reserve the largest terminal strings before accepting this record.
	candidate.Terminal = NativePhaseTerminal{Count: len(candidate.Events), LastSequence: len(candidate.Events), Disposition: NativePhaseUncertain, Loss: NativePhaseLossUnavailable}
	raw, err := json.Marshal(candidate)
	if err != nil || len(raw) > NativePhaseMaxBytes {
		c.lose(NativePhaseLossOverflow)
		return
	}
	c.trace.Events = candidate.Events
}

func (c *NativePhaseCollector) MarshalFinal() ([]byte, error) {
	if c == nil {
		return nil, errors.New("native phase collection is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.span == nil || len(c.trace.Events) == 0 {
		return nil, errors.New("native phase span is unavailable")
	}
	loss := c.loss
	if loss == "" {
		loss = NativePhaseLossNone
	}
	if !c.finished && loss == NativePhaseLossNone {
		loss = NativePhaseLossMissingEnd
	}
	t := c.trace
	t.Terminal = NativePhaseTerminal{Count: len(t.Events), LastSequence: len(t.Events), Complete: c.finished && loss == NativePhaseLossNone, Disposition: c.disposition, Loss: loss}
	raw, err := json.Marshal(t)
	if err != nil || len(raw) > NativePhaseMaxBytes {
		return nil, errors.New("native phase transcript exceeds its bound")
	}
	if _, err := DecodeNativePhaseTranscript(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func DecodeNativePhaseTranscript(raw []byte) (NativePhaseTranscript, error) {
	var t NativePhaseTranscript
	if len(raw) == 0 || len(raw) > NativePhaseMaxBytes {
		return t, errors.New("native phase transcript exceeds its bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&t); err != nil {
		return NativePhaseTranscript{}, errors.New("malformed native phase transcript")
	}
	canonical, err := json.Marshal(t)
	if err != nil || !bytes.Equal(canonical, raw) {
		return NativePhaseTranscript{}, errors.New("noncanonical native phase transcript")
	}
	if err := validateNativePhaseTranscript(t); err != nil {
		return NativePhaseTranscript{}, err
	}
	return t, nil
}
