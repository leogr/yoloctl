package controller

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/operator"
)

const (
	NativePhaseSlots            = 32
	NativePhaseStateCollecting  = "collecting"
	NativePhaseStateComplete    = "complete"
	NativePhaseStateIncomplete  = "incomplete"
	NativePhaseStateUnavailable = "unavailable"
	NativePhaseOutcomeOK        = "ok"
	NativePhaseOutcomeFailed    = "failed"
	NativePhaseOutcomeUncertain = "uncertain"
	NativePhaseOutcomeCanceled  = "canceled"
	nativePhaseMetadataBytes    = 8 << 10
	nativePhaseDrainAfterExit   = time.Second
)

// NativePhaseParent is copied only from the admitted parent execution. No
// request argument, child event or diagnostic read can supply this custody.
type NativePhaseParent struct {
	Hello         Hello     `json:"hello"`
	DiskID        string    `json:"disk_id"`
	VMIncarnation string    `json:"vm_incarnation"`
	ExecutionID   string    `json:"execution_id"`
	Method        string    `json:"method"`
	Principal     Principal `json:"principal"`
	Scope         *Scope    `json:"scope,omitempty"`
}

type NativePhaseResult struct {
	Status  int    `json:"status"`
	Outcome string `json:"outcome"`
}

// Transcript is canonical, schema-checked JSON only. Malformed input is never
// exposed. This diagnostic result is not a receipt, proof or recovery permit.
type NativePhaseReadout struct {
	Schema             int                `json:"schema"`
	State              string             `json:"state"`
	Reason             string             `json:"reason"`
	ControllerInstance string             `json:"controller_instance,omitempty"`
	Parent             NativePhaseParent  `json:"parent"`
	WorkerPID          int                `json:"worker_pid,omitempty"`
	Result             *NativePhaseResult `json:"result,omitempty"`
	Transcript         json.RawMessage    `json:"transcript,omitempty"`
}

type nativePhaseEntry struct {
	value     NativePhaseReadout
	token     uint64
	completed uint64
	capture   bool
	exited    bool
	result    bool
	loss      string
}

// NativePhaseStore owns at most 32 live/retained slots. It persists nothing and
// never calls admission, receipts, recovery, workers or a client writer.
type NativePhaseStore struct {
	mu       sync.Mutex
	instance string
	next     uint64
	entries  map[string]*nativePhaseEntry
	readers  int
}

func NewNativePhaseStore() *NativePhaseStore {
	return &NativePhaseStore{instance: rand.Text(), entries: make(map[string]*nativePhaseEntry)}
}

func nativePhaseToken(value string, max int) bool {
	return value != "" && len(value) <= max && utf8.ValidString(value) && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func copyNativePhaseScope(scope *Scope) (*Scope, bool) {
	if scope == nil {
		return nil, true
	}
	if scope.validate() != nil {
		return nil, false
	}
	size := 128
	for _, list := range [][]Resource{scope.Reads, scope.Writes} {
		for _, resource := range list {
			size += len(resource.Kind) + len(resource.Name) + 64
			if size > nativePhaseMetadataBytes {
				return nil, false
			}
		}
	}
	copy := cloneScope(scope)
	for _, list := range [][]Resource{copy.Reads, copy.Writes} {
		for i := range list {
			list[i].Kind, list[i].Name = strings.Clone(list[i].Kind), strings.Clone(list[i].Name)
		}
	}
	raw, err := json.Marshal(copy)
	return copy, err == nil && len(raw) <= nativePhaseMetadataBytes
}

func copyNativePhaseParent(parent NativePhaseParent) (NativePhaseParent, bool) {
	if parent.Hello.Protocol != ProtocolVersion || !nativePhaseToken(parent.Hello.Space, 128) || !nativePhaseToken(parent.Hello.Bundle, 256) || !nativePhaseToken(parent.Hello.Generation, 256) || !nativePhaseToken(parent.DiskID, 256) || !nativePhaseToken(parent.VMIncarnation, 256) {
		return NativePhaseParent{}, false
	}
	if (Request{ID: parent.ExecutionID, Method: parent.Method, Space: parent.Hello.Space, Dash: -1}).Validate() != nil || len(parent.Method) == 0 || strings.IndexFunc(parent.Method, func(r rune) bool { return r != '.' && r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') }) >= 0 {
		return NativePhaseParent{}, false
	}
	if parent.Principal.Box != "" {
		if layout.ValidateName("agentbox", parent.Principal.Box) != nil || !nativePhaseToken(parent.Principal.Incarnation, 128) {
			return NativePhaseParent{}, false
		}
	} else if parent.Principal.Incarnation != "" {
		return NativePhaseParent{}, false
	}
	var ok bool
	parent.Scope, ok = copyNativePhaseScope(parent.Scope)
	if !ok {
		return NativePhaseParent{}, false
	}
	parent.Hello.Space, parent.Hello.Bundle, parent.Hello.Generation = strings.Clone(parent.Hello.Space), strings.Clone(parent.Hello.Bundle), strings.Clone(parent.Hello.Generation)
	parent.DiskID, parent.VMIncarnation = strings.Clone(parent.DiskID), strings.Clone(parent.VMIncarnation)
	parent.ExecutionID, parent.Method = strings.Clone(parent.ExecutionID), strings.Clone(parent.Method)
	parent.Principal.Box, parent.Principal.Incarnation = strings.Clone(parent.Principal.Box), strings.Clone(parent.Principal.Incarnation)
	raw, err := json.Marshal(parent)
	return parent, err == nil && len(raw) <= nativePhaseMetadataBytes
}

// Reserve precedes pipe/buffer/goroutine allocation. Only completed slots are
// evicted; saturation disables diagnostics without delaying the operation.
func (s *NativePhaseStore) Reserve(parent NativePhaseParent) *WorkerObservation {
	if s == nil {
		return nil
	}
	if len(parent.ExecutionID) < 16 || len(parent.ExecutionID) > 80 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil || s.instance == "" || s.entries[parent.ExecutionID] != nil {
		return nil
	}
	oldestID := ""
	if len(s.entries) >= NativePhaseSlots {
		oldest := ^uint64(0)
		for id, entry := range s.entries {
			if entry.completed != 0 && entry.completed < oldest {
				oldestID, oldest = id, entry.completed
			}
		}
		if oldestID == "" {
			return nil
		}
	}
	parent, ok := copyNativePhaseParent(parent)
	if !ok {
		return nil
	}
	if oldestID != "" {
		delete(s.entries, oldestID)
	}
	s.next++
	entry := &nativePhaseEntry{token: s.next, value: NativePhaseReadout{Schema: 1, State: NativePhaseStateCollecting, Reason: "none", ControllerInstance: s.instance, Parent: parent}}
	s.entries[parent.ExecutionID] = entry
	return &WorkerObservation{store: s, id: parent.ExecutionID, token: entry.token, exit: make(chan struct{}), result: make(chan struct{})}
}

func unavailableNativePhase() NativePhaseReadout {
	return NativePhaseReadout{Schema: 1, State: NativePhaseStateUnavailable, Reason: "unavailable"}
}

// TryRead bounds outstanding copied/encoded external responses as well as the
// retained store. A busy diagnostic reader never waits for another client.
// External callers retain release through their bounded output/deadline.
func (s *NativePhaseStore) TryRead() (release func(), ok bool) {
	if s == nil {
		return func() {}, true
	}
	s.mu.Lock()
	if s.readers >= 4 {
		s.mu.Unlock()
		return nil, false
	}
	s.readers++
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.readers--
			s.mu.Unlock()
		})
	}, true
}

// Read returns owned immutable copies; callers write them only after this
// bounded lock has been released. Reading never reserves a slot.
func (s *NativePhaseStore) Read(executionID string) NativePhaseReadout {
	if s == nil || len(executionID) > 80 {
		return unavailableNativePhase()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[executionID]
	if entry == nil {
		return unavailableNativePhase()
	}
	value := entry.value
	value.Parent.Scope = cloneScope(value.Parent.Scope)
	value.Transcript = bytes.Clone(value.Transcript)
	if value.Result != nil {
		result := *value.Result
		value.Result = &result
	}
	return value
}

// WorkerObservation belongs to one original slot generation. Late transport
// completion cannot replace another execution after eviction/reuse.
type WorkerObservation struct {
	store      *NativePhaseStore
	id         string
	token      uint64
	exit       chan struct{}
	result     chan struct{}
	exitOnce   sync.Once
	resultOnce sync.Once
}

func (o *WorkerObservation) entryLocked() *nativePhaseEntry {
	entry := o.store.entries[o.id]
	if entry == nil || entry.token != o.token {
		return nil
	}
	return entry
}

func (o *WorkerObservation) matches(principal Principal, request Request) bool {
	if o == nil {
		return false
	}
	o.store.mu.Lock()
	defer o.store.mu.Unlock()
	entry := o.entryLocked()
	return entry != nil && entry.completed == 0 && entry.value.Parent.ExecutionID == request.ID && entry.value.Parent.Method == request.Method && entry.value.Parent.Hello.Space == request.Space && entry.value.Parent.Principal == principal
}

func (o *WorkerObservation) Started(pid int) {
	if o == nil {
		return
	}
	o.store.mu.Lock()
	defer o.store.mu.Unlock()
	if entry := o.entryLocked(); entry != nil {
		if pid <= 1 || entry.value.WorkerPID != 0 {
			entry.loss = "identity"
			return
		}
		entry.value.WorkerPID = pid
	}
}

func (o *WorkerObservation) finishUnavailableLocked(entry *nativePhaseEntry) {
	if entry.result && entry.exited && !entry.capture && entry.completed == 0 {
		entry.value.State, entry.value.Reason = NativePhaseStateUnavailable, "unavailable"
		o.store.next++
		entry.completed = o.store.next
	}
}

// Exited only notifies diagnostics. It never joins the independent drain.
func (o *WorkerObservation) Exited() {
	if o == nil {
		return
	}
	o.exitOnce.Do(func() {
		o.store.mu.Lock()
		if entry := o.entryLocked(); entry != nil {
			entry.exited = true
			o.finishUnavailableLocked(entry)
		}
		o.store.mu.Unlock()
		close(o.exit)
	})
}

// Result is a separate parent fact, copied after Execute with its actual scope.
// No raw error enters the store; outcome is closed and never settles a receipt.
func (o *WorkerObservation) Result(status int, outcome string, scope *Scope) {
	if o == nil {
		return
	}
	o.resultOnce.Do(func() {
		copy, valid := copyNativePhaseScope(scope)
		switch outcome {
		case NativePhaseOutcomeOK, NativePhaseOutcomeFailed, NativePhaseOutcomeUncertain, NativePhaseOutcomeCanceled:
		default:
			valid, outcome = false, NativePhaseOutcomeFailed
		}
		if status < 0 || status > 255 {
			valid, status = false, 2
		}
		o.store.mu.Lock()
		if entry := o.entryLocked(); entry != nil {
			entry.result = true
			entry.value.Result = &NativePhaseResult{Status: status, Outcome: outcome}
			if valid {
				entry.value.Parent.Scope = copy
			} else {
				entry.loss = "identity"
				entry.value.Parent.Scope = nil
			}
			o.finishUnavailableLocked(entry)
		}
		o.store.mu.Unlock()
		close(o.result)
	})
}

func (o *WorkerObservation) pipe() (*os.File, *os.File) {
	if o == nil {
		return nil, nil
	}
	o.store.mu.Lock()
	entry := o.entryLocked()
	if entry == nil || entry.capture || entry.completed != 0 {
		o.store.mu.Unlock()
		return nil, nil
	}
	// Reserve the single transport before any capture resources are created.
	entry.capture = true
	o.store.mu.Unlock()
	read, write, err := os.Pipe()
	if err != nil {
		o.store.mu.Lock()
		if entry := o.entryLocked(); entry != nil {
			entry.capture = false
			o.finishUnavailableLocked(entry)
		}
		o.store.mu.Unlock()
		return nil, nil
	}
	go o.drain(read)
	return read, write
}

func (o *WorkerObservation) drain(read *os.File) {
	drained := make(chan struct{})
	go func() {
		select {
		case <-drained:
			return
		case <-o.exit:
		}
		timer := time.NewTimer(nativePhaseDrainAfterExit)
		defer timer.Stop()
		select {
		case <-drained:
		case <-timer.C:
			_ = read.Close() // diagnostic reader only
		}
	}()
	raw := make([]byte, operator.NativePhaseMaxBytes+1)
	n, err := io.ReadFull(read, raw)
	_ = read.Close()
	close(drained)
	loss := "none"
	if n > operator.NativePhaseMaxBytes {
		loss, raw = "overflow", nil
	} else if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		loss, raw = "missing_end", nil
	} else {
		raw = raw[:n:n]
	}
	// Decode only after parent custody includes the actual launched worker,
	// final admitted scope and command result. Never wait on the product path.
	<-o.exit
	<-o.result
	o.accept(raw, loss)
}

func (o *WorkerObservation) accept(raw []byte, loss string) {
	if len(raw) > operator.NativePhaseMaxBytes {
		loss, raw = "overflow", nil
	}
	o.store.mu.Lock()
	entry := o.entryLocked()
	if entry == nil || entry.completed != 0 || !entry.exited || !entry.result {
		o.store.mu.Unlock()
		return
	}
	value := entry.value
	priorLoss := entry.loss
	o.store.mu.Unlock()
	value.State, value.Reason = NativePhaseStateIncomplete, loss
	if value.WorkerPID <= 1 {
		value.State, value.Reason = NativePhaseStateUnavailable, "unavailable"
	} else if priorLoss != "" {
		value.Reason = priorLoss
		if priorLoss == "unavailable" {
			value.State = NativePhaseStateUnavailable
		}
	} else if value.Parent.Scope == nil {
		value.Reason = "identity"
	} else if loss == "none" {
		transcript, err := operator.DecodeNativePhaseTranscript(raw)
		switch {
		case len(raw) == 0:
			value.Reason = "missing_end"
		case err != nil:
			value.Reason = "malformed"
		case !containsResource(value.Parent.Scope.Writes, Resource{Kind: ResourceBox, Name: transcript.Box}):
			value.Reason = "identity"
		default:
			value.Transcript = bytes.Clone(raw)
			value.Reason = string(transcript.Terminal.Loss)
			if transcript.Terminal.Complete {
				value.State, value.Reason = NativePhaseStateComplete, "none"
			}
		}
	}
	// Include custody/result/envelope overhead in the per-slot/readout ceiling.
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > operator.NativePhaseMaxBytes {
		value.Transcript = nil
		value.State, value.Reason = NativePhaseStateIncomplete, "overflow"
	}
	o.store.mu.Lock()
	defer o.store.mu.Unlock()
	if entry := o.entryLocked(); entry != nil && entry.completed == 0 {
		entry.value = value
		o.store.next++
		entry.completed = o.store.next
	}
}
