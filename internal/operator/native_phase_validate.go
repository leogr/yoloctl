package operator

import (
	"errors"
	"fmt"

	"github.com/leogr/yoloctl/internal/layout"
)

func nativePhaseAttempt(role NativePhaseRole) int {
	switch role {
	case NativePhaseOriginal:
		return 0
	case NativePhaseDesired:
		return 1
	case NativePhaseRollback:
		return 2
	default:
		return -1
	}
}

func nativePhaseHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func nativePhaseGeneration(value string) bool {
	return len(value) == 49 && value[0] == 'g' && nativePhaseHex(value[1:], 48)
}

func validNativePhaseHeader(box, cid, generation string) bool {
	return len(box) <= 63 && layout.ValidateName("agentbox", box) == nil && nativePhaseHex(cid, 64) && nativePhaseGeneration(generation)
}

func validNativePhaseOutcome(outcome NativePhaseOutcome, pending bool) bool {
	switch outcome {
	case NativePhaseOK, NativePhaseFailed, NativePhaseUncertain, NativePhaseCanceled:
		return true
	case NativePhasePending:
		return pending
	default:
		return false
	}
}

func validNativePhaseEvent(t NativePhaseTranscript, e NativePhaseEvent) bool {
	if e.Sequence < 1 || e.Sequence > NativePhaseMaxEvents || e.CID != t.CID || e.Attempt != nativePhaseAttempt(e.Role) || e.Attempt < 0 {
		return false
	}
	switch e.Kind {
	case NativePhaseRestartBegin, NativePhaseOldStopReturn, NativePhaseRestartReturn:
		if e.Role != NativePhaseOriginal || e.Generation != t.OriginalGeneration || e.Forward != nil {
			return false
		}
		if e.Kind == NativePhaseRestartBegin {
			return e.Outcome == NativePhasePending
		}
		return validNativePhaseOutcome(e.Outcome, false)
	case NativePhaseLaunchAttempt:
		return e.Role != NativePhaseOriginal && e.Outcome == NativePhasePending && e.Generation == "" && e.Forward == nil
	case NativePhasePrepareReturn, NativePhaseLaunchReturn:
		if e.Role == NativePhaseOriginal || !validNativePhaseOutcome(e.Outcome, false) || e.Forward != nil {
			return false
		}
		return nativePhaseGeneration(e.Generation) || e.Generation == "" && e.Outcome != NativePhaseOK
	case NativePhaseEngineStartReturn, NativePhaseActivationReturn, NativePhaseSSHReturn, NativePhaseGenerationCleanupReturn:
		return e.Role != NativePhaseOriginal && validNativePhaseOutcome(e.Outcome, false) && nativePhaseGeneration(e.Generation) && e.Forward == nil
	case NativePhaseForwardStartReturn, NativePhaseForwardReadyReturn, NativePhaseForwardCleanupReturn:
		f := e.Forward
		return e.Role != NativePhaseOriginal && validNativePhaseOutcome(e.Outcome, false) && nativePhaseGeneration(e.Generation) && f != nil &&
			f.HostPort >= 1 && f.HostPort <= 65535 && f.TargetPort >= 1 && f.TargetPort <= 65535 && nativePhaseHex(f.Attempt, 32) && len(f.Unit) <= 180 &&
			// This spelling is part of the closed version-1 diagnostic format.
			// Importing runtime launch packages would widen the helper input closure.
			f.Unit == fmt.Sprintf("yoloctl-openshell-port-%s-%d-%d.service", t.Box, f.HostPort, f.TargetPort)
	default:
		return false
	}
}

type nativePhaseForwardState struct {
	identity       NativePhaseForward
	start          NativePhaseOutcome
	ready, cleanup NativePhaseOutcome
}

type nativePhaseLaunchState struct {
	attempted, returned              bool
	result                           NativePhaseOutcome
	generation                       string
	prepare, engine, activation, ssh NativePhaseOutcome
	cleanup                          NativePhaseOutcome
	cleanupStarted                   bool
	forwards                         map[string]*nativePhaseForwardState
}

func (l *nativePhaseLaunchState) uncertain() bool {
	if l.prepare == NativePhaseUncertain || l.engine == NativePhaseUncertain || l.activation == NativePhaseUncertain || l.ssh == NativePhaseUncertain || l.cleanup == NativePhaseUncertain {
		return true
	}
	for _, f := range l.forwards {
		if f.start == NativePhaseUncertain || f.ready == NativePhaseUncertain || f.cleanup == NativePhaseUncertain {
			return true
		}
	}
	return false
}

func (l *nativePhaseLaunchState) cleanable() bool {
	if l.prepare != NativePhaseOK || l.engine != "" && l.engine != NativePhaseOK || l.activation == NativePhaseUncertain || l.ssh == NativePhaseUncertain {
		return false
	}
	for _, f := range l.forwards {
		if f.start != NativePhaseOK {
			return false
		}
	}
	return true
}

func (l *nativePhaseLaunchState) readyForForward() bool {
	if l.ssh != NativePhaseOK || l.cleanupStarted || l.cleanup != "" {
		return false
	}
	for _, f := range l.forwards {
		if f.start != NativePhaseOK || f.ready != NativePhaseOK || f.cleanup != "" {
			return false
		}
	}
	return true
}

func (l *nativePhaseLaunchState) successful() bool {
	return l.prepare == NativePhaseOK && l.engine == NativePhaseOK && l.activation == NativePhaseOK && l.ssh == NativePhaseOK && l.readyForForward()
}

type nativePhaseState struct {
	begun, ended      bool
	stop              NativePhaseOutcome
	active            NativePhaseRole
	desired, rollback nativePhaseLaunchState
	forwardAttempts   map[string]bool
}

func (s *nativePhaseState) launch(role NativePhaseRole) *nativePhaseLaunchState {
	if role == NativePhaseDesired {
		return &s.desired
	}
	return &s.rollback
}

func (s *nativePhaseState) apply(t NativePhaseTranscript, e NativePhaseEvent) error {
	bad := errors.New("invalid native phase callback sequence")
	if s.ended {
		return bad
	}
	if e.Kind == NativePhaseRestartBegin {
		if s.begun || e.Sequence != 1 {
			return bad
		}
		s.begun = true
		return nil
	}
	if !s.begun {
		return bad
	}
	switch e.Kind {
	case NativePhaseOldStopReturn:
		if s.stop != "" || s.desired.attempted || s.rollback.attempted {
			return bad
		}
		s.stop = e.Outcome
		return nil
	case NativePhaseLaunchAttempt:
		if s.stop != NativePhaseOK || s.active != "" {
			return bad
		}
		l := s.launch(e.Role)
		if l.attempted || e.Role == NativePhaseDesired && s.rollback.attempted {
			return bad
		}
		if e.Role == NativePhaseRollback && s.desired.attempted && (!s.desired.returned || s.desired.result != NativePhaseFailed && s.desired.result != NativePhaseCanceled) {
			return bad
		}
		l.attempted, l.forwards, s.active = true, map[string]*nativePhaseForwardState{}, e.Role
		return nil
	case NativePhaseRestartReturn:
		if s.active != "" || !s.validReturn(e.Outcome) {
			return bad
		}
		s.ended = true
		return nil
	}
	if s.active == "" || e.Role != s.active {
		return bad
	}
	l := s.launch(e.Role)
	if e.Kind == NativePhasePrepareReturn {
		if l.prepare != "" || l.generation != "" {
			return bad
		}
		if e.Generation != "" && (e.Generation == t.OriginalGeneration || e.Generation == s.launch(otherNativePhaseRole(e.Role)).generation) {
			return bad
		}
		l.prepare, l.generation = e.Outcome, e.Generation
		return nil
	}
	if e.Generation != l.generation {
		return errors.New("native phase generation differs from its original preparation")
	}
	if e.Kind == NativePhaseLaunchReturn {
		if e.Outcome == NativePhaseOK && !l.successful() {
			return bad
		}
		if e.Outcome == NativePhaseFailed || e.Outcome == NativePhaseCanceled {
			if l.uncertain() || l.prepare == NativePhaseOK && l.cleanup != NativePhaseOK {
				return bad
			}
		}
		l.returned, l.result, s.active = true, e.Outcome, ""
		return nil
	}
	if l.prepare != NativePhaseOK || l.cleanup != "" {
		return bad
	}
	switch e.Kind {
	case NativePhaseEngineStartReturn:
		if l.engine != "" || l.activation != "" || l.ssh != "" || l.cleanupStarted {
			return bad
		}
		l.engine = e.Outcome
	case NativePhaseActivationReturn:
		if l.engine != NativePhaseOK || l.activation != "" || l.ssh != "" || l.cleanupStarted {
			return bad
		}
		l.activation = e.Outcome
	case NativePhaseSSHReturn:
		if l.activation != NativePhaseOK || l.ssh != "" || l.cleanupStarted {
			return bad
		}
		l.ssh = e.Outcome
	case NativePhaseForwardStartReturn:
		if !l.readyForForward() || s.forwardAttempts[e.Forward.Attempt] {
			return bad
		}
		for _, f := range l.forwards {
			if f.identity.Unit == e.Forward.Unit {
				return bad
			}
		}
		l.forwards[e.Forward.Attempt] = &nativePhaseForwardState{identity: *e.Forward, start: e.Outcome}
		s.forwardAttempts[e.Forward.Attempt] = true
	case NativePhaseForwardReadyReturn:
		f := l.forwards[e.Forward.Attempt]
		if f == nil || f.identity != *e.Forward || f.start != NativePhaseOK || f.ready != "" || f.cleanup != "" || l.cleanupStarted {
			return bad
		}
		f.ready = e.Outcome
	case NativePhaseForwardCleanupReturn:
		f := l.forwards[e.Forward.Attempt]
		if f == nil || f.identity != *e.Forward || f.start != NativePhaseOK || f.ready == "" || f.cleanup != "" || !l.cleanable() {
			return bad
		}
		f.cleanup, l.cleanupStarted = e.Outcome, true
	case NativePhaseGenerationCleanupReturn:
		if !l.cleanable() {
			return bad
		}
		if e.Outcome == NativePhaseOK {
			if l.uncertain() {
				return bad
			}
			for _, f := range l.forwards {
				if f.ready == "" || f.cleanup != NativePhaseOK {
					return bad
				}
			}
		}
		l.cleanup, l.cleanupStarted = e.Outcome, true
	default:
		return bad
	}
	return nil
}

func otherNativePhaseRole(role NativePhaseRole) NativePhaseRole {
	if role == NativePhaseDesired {
		return NativePhaseRollback
	}
	return NativePhaseDesired
}

func (s *nativePhaseState) validReturn(outcome NativePhaseOutcome) bool {
	if s.stop == "" {
		return !s.desired.attempted && !s.rollback.attempted && outcome != NativePhaseOK
	}
	if s.stop != NativePhaseOK {
		return !s.desired.attempted && !s.rollback.attempted && outcome == s.stop
	}
	if s.desired.returned && s.desired.result == NativePhaseOK {
		return !s.rollback.attempted && outcome == NativePhaseOK
	}
	if s.desired.returned && s.desired.result == NativePhaseUncertain {
		return !s.rollback.attempted && outcome == NativePhaseUncertain
	}
	if !s.rollback.returned {
		return false
	}
	if s.rollback.result != NativePhaseOK {
		return outcome == NativePhaseUncertain
	}
	if !s.desired.attempted {
		return outcome == NativePhaseCanceled
	}
	return s.desired.returned && outcome == s.desired.result && (outcome == NativePhaseFailed || outcome == NativePhaseCanceled)
}

func validateNativePhaseEvents(t NativePhaseTranscript) (nativePhaseState, error) {
	s := nativePhaseState{forwardAttempts: map[string]bool{}}
	if len(t.Events) == 0 || len(t.Events) > NativePhaseMaxEvents {
		return s, errors.New("native phase events are unavailable or exceed their bound")
	}
	for index, event := range t.Events {
		if event.Sequence != index+1 || !validNativePhaseEvent(t, event) {
			return s, errors.New("invalid native phase event identity or fields")
		}
		if err := s.apply(t, event); err != nil {
			return s, err
		}
	}
	return s, nil
}

func validateNativePhaseTranscript(t NativePhaseTranscript) error {
	if t.Schema != 1 || !validNativePhaseHeader(t.Box, t.CID, t.OriginalGeneration) {
		return errors.New("invalid native phase transcript identity")
	}
	s, err := validateNativePhaseEvents(t)
	if err != nil {
		return err
	}
	end := t.Terminal
	if end.Count != len(t.Events) || end.LastSequence != len(t.Events) || !validNativePhaseOutcome(end.Disposition, !end.Complete) {
		return errors.New("invalid native phase terminal coverage")
	}
	switch end.Loss {
	case NativePhaseLossNone, NativePhaseLossUnavailable, NativePhaseLossOverflow, NativePhaseLossIdentity, NativePhaseLossMalformed, NativePhaseLossMissingEnd:
	default:
		return errors.New("unknown native phase collection loss")
	}
	if end.Complete && (!s.ended || end.Loss != NativePhaseLossNone) || !end.Complete && end.Loss == NativePhaseLossNone {
		return errors.New("native phase completeness contradicts its observed coverage")
	}
	if end.Loss == NativePhaseLossMissingEnd && s.ended {
		return errors.New("native phase missing-end disposition contradicts its final callback")
	}
	if s.ended && t.Events[len(t.Events)-1].Outcome != end.Disposition {
		return errors.New("native phase disposition differs from its final callback")
	}
	return nil
}
