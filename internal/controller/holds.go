package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/distribution/reference"
)

// Running reservations come from controller-owned live admissions. Finished
// outcomes and stored history never reserve resources.
type holds struct {
	// global names receipts that hold the whole space.
	global []Receipt
	// writes maps each held resource to the receipts holding it.
	writes map[Resource][]Receipt
}

// unresolved is the compatibility predicate for old call sites. No finished
// receipt fences an execution, including old outcome spellings.
func (r Receipt) unresolved() bool { return false }

// reserving reports a running execution already permitted to have effects
// whose effects have not settled. A completed setup or a known
// completion is such a disposition; an authorized exec's remote command is
// never tracked as controller state, so neither reserves.
func (r Receipt) reserving() bool {
	return (r.Image == nil || r.Image.PrivateVersion != 1 || r.Image.Stage != "preflight") && !r.abandonedImage() && r.State == "pending" && r.EffectPhase != EffectWaiting && !r.SetupComplete && r.Completion == nil && !authorizedCommand(r)
}

// boundedScope returns the receipt's footprint when it can be trusted for
// this disk, or nil for a space-wide hold.
func (s *Server) boundedScope(r Receipt) *Scope {
	if r.ProofVersion == 0 || r.Scope == nil || r.Scope.Global || s.DiskID != "" && r.DiskID != s.DiskID {
		return nil
	}
	return r.Scope
}

// loadHolds reads the complete live admission table. Callers hold s.mu.
// Historical files and recording health never authorize or exclude work.
func (s *Server) loadHolds(exclude string) (holds, error) {
	h := holds{writes: map[Resource][]Receipt{}}
	var all []Receipt
	for _, e := range s.executions {
		if !e.settled {
			all = append(all, *e.receipt)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Started.Before(all[j].Started) })
	for _, r := range all {
		if r.ID == exclude || !r.unresolved() && !r.reserving() {
			continue
		}
		scope := s.boundedScope(r)
		if scope == nil {
			h.global = append(h.global, r)
			continue
		}
		for _, w := range scope.Writes {
			h.writes[w] = append(h.writes[w], r)
		}
	}
	return h, nil
}

// unresolvedOnly is the stop-beside-other-operations policy: running
// reservations do not delay that stop, and finished outcomes reserve nothing.
func (h holds) unresolvedOnly() holds { return holds{writes: map[Resource][]Receipt{}} }

// conflict returns the held receipts a new footprint intersects: a write or a
// dependency read of a held resource. An unbounded footprint intersects
// every hold.
func (h holds) conflict(scope *Scope) (resources []Resource, receipts []Receipt, global bool) {
	seen := map[string]bool{}
	add := func(list []Receipt) {
		for _, r := range list {
			if !seen[r.ID] {
				seen[r.ID] = true
				receipts = append(receipts, r)
			}
		}
	}
	add(h.global)
	if scope == nil || scope.Global {
		for res, list := range h.writes {
			resources = append(resources, res)
			add(list)
		}
	} else {
		for index, list := range [][]Resource{scope.Writes, scope.Reads} {
			for _, access := range list {
				for res, held := range h.writes {
					if resourcesOverlap(res, access, index == 0) && !containsResource(resources, res) {
						resources = append(resources, res)
						add(held)
					}
				}
			}
		}
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].String() < resources[j].String() })
	return resources, receipts, len(h.global) != 0
}

// resourcesOverlap is directional: an unnamed compatibility read does not
// meet a named image write, but a store-wide write meets every image resource.
// Identical normalized image references represent the same resource even when
// one caller omits the default registry. All other kinds keep exact matching.
func resourcesOverlap(held, access Resource, writing bool) bool {
	if held.Kind != ResourceImages || access.Kind != ResourceImages {
		return held == access
	}
	if held.Name == "" {
		return true
	}
	if access.Name == "" {
		return writing
	}
	a, errA := reference.ParseNormalizedNamed(held.Name)
	b, errB := reference.ParseNormalizedNamed(access.Name)
	if errA != nil || errB != nil {
		return true // invalid scopes must never open a gate
	}
	return reference.TagNameOnly(a).String() == reference.TagNameOnly(b).String()
}

// heldError explains a refused effect. Hosts see the receipts to inspect;
// a box operator sees only the resources it asked to use, never receipt
// identities or another principal's operations.
func heldError(space string, admin, global bool, resources []Resource, receipts []Receipt) error {
	if !admin {
		var names []string
		for _, resource := range resources {
			names = append(names, resource.String())
		}
		return fmt.Errorf("waiting for running operations using %s", strings.Join(names, ", "))
	}
	var ids []string
	for _, r := range receipts {
		ids = append(ids, r.ID+" ("+r.Method+")")
	}
	return fmt.Errorf("waiting for running operations in space %s: %s", space, strings.Join(ids, ", "))
}

// refreshRecovery retains the compatibility field without deriving admission
// restrictions from finished receipts. Running reservations are checked separately.
func (s *Server) refreshRecovery() error {
	s.RecoveryRequired = false
	return nil
}

// receiptsChanged returns a channel closed by the next receipt transition.
// Callers hold s.mu.
func (s *Server) receiptsChanged() <-chan struct{} {
	if s.changed == nil {
		s.changed = make(chan struct{})
	}
	return s.changed
}

// notifyReceipts wakes gates waiting for a reservation. Callers hold s.mu.
func (s *Server) notifyReceipts() {
	if s.changed != nil {
		close(s.changed)
		s.changed = nil
	}
}

// awaitReservations lets a gate whose footprint meets only running,
// already-permitted executions wait for their live disposition instead of
// failing on a race with finalization. The gate rechecks the live table before
// granting its own footprint.
func (s *Server) awaitReservations(ctx context.Context, id string, scope *Scope) error {
	for {
		s.mu.Lock()
		current, err := s.loadHolds(id)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		_, held, _ := current.conflict(scope)
		running := len(held) != 0
		for _, r := range held {
			running = running && r.reserving()
		}
		if !running {
			s.mu.Unlock()
			return nil
		}
		changed := s.receiptsChanged()
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// explainOutcome is the host's plain reading of one receipt, written beside
// its JSON. It never contains payloads, secrets or fingerprints.
func (s *Server) explainOutcome(r Receipt) string {
	if r.abandonedImage() && r.Completion == nil {
		return "Image preparation was abandoned and cannot be published or consumed. Its slot is free; possible surviving build work may still use CPU, memory or disk. The execution was interrupted; no acknowledgment is required.\n"
	}
	switch r.State {
	case "pending":
		return "Running: it may still have effects and is never acknowledged.\n"
	case "complete":
		if r.Status == 0 {
			return "Finished successfully.\n"
		}
		return fmt.Sprintf("Finished with exit status %d.\n", r.Status)
	case "setup-complete":
		return "Its setup finished before the interruption; nothing awaits review.\n"
	case "acknowledged":
		return "A prior build recorded an acknowledgment; this does not establish success and nothing was replayed.\n"
	case "interrupted", "uncertain", "command-uncertain":
		return r.interruptionText(s.Hello.Space) + "\n"
	case "recovered":
		switch r.Recovery.Disposition {
		case DispositionNotApplied:
			return "Recovered automatically: not applied. The operation never received permission for its first effect.\n"
		case DispositionRecordedCompletion:
			result := "succeeded"
			if r.Status != 0 {
				result = fmt.Sprintf("failed with exit status %d", r.Status)
			}
			return fmt.Sprintf("Recovered automatically from its completion record: every effect had ended and the operation %s. Its client did not receive that reply; nothing was replayed.\n", result)
		case DispositionApplied:
			if r.Verifier == VerifierChatRevoke {
				return "Recovered automatically: the desired chat revocation is recorded on disk; the command's historical result remains unknown. Whether the running chat server applied it is not established.\n"
			}
			return "Recovered automatically: the exact account declaration it was writing is on disk; the command's historical result remains unknown. Existing boxes keep their previous secrets until they are recreated.\n"
		}

	}
	return ""
}

// explainHolds summarizes the current holds for the host's receipt list.
func explainHolds(all []Receipt, s *Server) string {
	var barriers strings.Builder
	if s.receiptFailure != nil {
		barriers.WriteString("Some operation records await storage repair; commands continue and current executions are shown from memory.\n")
	}
	if s.draining {
		barriers.WriteString("The controller is in maintenance; recovery does not reopen it.\n")
	}

	return barriers.String()
}

// publicReceipt retains the outcome and footprint without private verifier material.
func publicReceipt(r Receipt) Receipt {
	r.Proofs = nil
	return r
}

// CheckImageCleanup checks current granted footprints, never receipt files.
func (s *Server) CheckImageCleanup(exclude string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.loadHolds(exclude)
	if err != nil {
		return err
	}
	_, running, _ := h.conflict(&Scope{Writes: []Resource{{Kind: ResourceImages}}})
	if len(running) != 0 {
		return fmt.Errorf("image store is reserved by running work in space %s", s.Hello.Space)
	}
	return nil
}

// CheckAuditReconcile is called in the controller process after its native
// audit guard. No disk record substitutes for a live execution's authority.
func (s *Server) CheckAuditReconcile() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, err := s.loadHolds("")
	if err != nil {
		return err
	}
	_, running, _ := h.conflict(&Scope{Writes: []Resource{{Kind: ResourceAudit}}})
	if len(running) != 0 {
		return fmt.Errorf("audit state is reserved by running work in space %s", s.Hello.Space)
	}
	return nil
}
