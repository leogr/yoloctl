package controller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Observation records one bounded closing-time read, never historical success.
// The record is durable so an unavailable read or proof is not polled after restart.
type Observation struct {
	At     time.Time        `json:"at"`
	Reason string           `json:"reason,omitempty"`
	Boxes  []BoxObservation `json:"boxes,omitempty"`
	Proof  string           `json:"proof,omitempty"`
}

type BoxObservation struct {
	Name   string `json:"name"`
	ID     string `json:"id,omitempty"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

func (r Receipt) interruptionText(space string) string {
	if space == "" {
		space = "SPACE"
	}
	detail := "not observed; read the state with yoloctl space operation " + space + " " + r.ID
	if o := r.Observation; o != nil {
		detail = "closing observation at " + o.At.Format(time.RFC3339Nano)
		if o.Reason != "" {
			detail += ": not observed (" + observationText(o.Reason) + ")"
		}
		for _, box := range o.Boxes {
			detail += fmt.Sprintf("; box %s %s", box.Name, box.State)
			if box.ID != "" {
				detail += " (" + box.ID + ")"
			}
			if box.Reason != "" {
				detail += ": " + observationText(box.Reason)
			}
		}
		if o.Proof != "" {
			detail += "; document proof: " + observationText(o.Proof)
		}
	}
	if authorizedCommand(r) {
		detail += "; the command may have run and is not run again"
	}
	return "interrupted: " + r.Method + " did not finish; " + detail
}

func observationBoxes(r Receipt, disk string) ([]string, string) {
	if r.DiskID == "" || disk == "" || r.DiskID != disk {
		return nil, "recorded for another disk"
	}
	if r.ProofVersion == 0 || r.Scope == nil || r.Scope.Global || r.Scope.validate() != nil {
		return nil, "scope unknown"
	}
	var names []string
	seen := map[string]bool{}
	for _, resource := range append(append([]Resource(nil), r.Scope.Writes...), r.Scope.Reads...) {
		if resource.Kind == ResourceBox && !seen[resource.Name] {
			names = append(names, resource.Name)
			seen[resource.Name] = true
		}
	}
	return names, ""
}

// checkInterrupted observes containers and reads the existing document proof
// under the pass context; each verifier has its own fail-safe once it holds the
// space lock. Failure is a recorded observation, not a hold/retry. notStarted
// reports that the pass ended before any observation started (every verifier
// called was still waiting for the lock): nothing is recorded then.
func (s *Server) checkInterrupted(ctx context.Context, r Receipt) (next Receipt, observedDocs []DocumentIntent, notStarted bool) {
	next = r
	o := &Observation{At: time.Now().UTC()}
	next.Observation = o
	names, reason := observationBoxes(r, s.DiskID)
	o.Reason = reason
	if reason == "" && len(names) != 0 {
		if s.Observe == nil {
			o.Reason = "container observer unavailable"
		} else {
			boxes, err := s.Observe(ctx, r)
			if errors.Is(err, errObservationNotStarted) {
				return r, nil, true
			}
			if err != nil {
				o.Reason = err.Error()
				for _, name := range names {
					o.Boxes = append(o.Boxes, BoxObservation{Name: name, State: "not observed", Reason: err.Error()})
				}
			} else {
				// A callback cannot silently omit a scoped box or add an unrelated one.
				for _, name := range names {
					found := false
					for _, box := range boxes {
						if box.Name == name {
							o.Boxes = append(o.Boxes, box)
							found = true
							break
						}
					}
					if !found {
						o.Boxes = append(o.Boxes, BoxObservation{Name: name, State: "not observed", Reason: "observer returned no result"})
					}
				}
			}
		}
	}
	if r.Verifier == "" || len(r.Proofs) == 0 {
		if len(names) == 0 && o.Reason == "" {
			o.Reason = "no box named in recorded scope"
		}
		return next, nil, false
	}
	if reason != "" {
		o.Proof = "not observed (" + reason + ")"
		return next, nil, false
	}
	if s.Verify == nil || s.RecoveryKey == nil {
		o.Proof = "not observed (verifier unavailable)"
		return next, nil, false
	}
	observed, err := s.Verify(ctx, r)
	if errors.Is(err, errObservationNotStarted) && len(o.Boxes) == 0 {
		// No container observation preceded it: nothing started.
		return r, nil, true
	}
	if err != nil {
		o.Proof = "not observed (" + strings.TrimSpace(err.Error()) + ")"
		return next, nil, false
	}
	o.Proof = "mismatch"
	if len(observed) != len(r.Proofs) {
		return next, nil, false
	}
	for i, p := range r.Proofs {
		if !s.RecoveryKey.matchesVersion(r.ProofVersion, r.DiskID, r.ID, r.Verifier, p, observed[i]) {
			return next, nil, false
		}
	}
	o.Proof = "matched"
	return next, observed, false
}

// Inspection stderr is plain text, not a terminal-control channel.
func observationText(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}
