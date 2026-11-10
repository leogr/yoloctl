package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/distribution/reference"
)

// Holds are derived from durable receipts on every check; there is no second
// authoritative hold database. An unresolved outcome holds its recorded
// write footprint. An effect-started execution reserves its footprint until
// its disposition is durable, even after its native lock was released. An
// outcome without a trustworthy footprint holds the whole space.
type holds struct {
	// global names receipts that hold the whole space.
	global []Receipt
	// writes maps each held resource to the receipts holding it.
	writes map[Resource][]Receipt
}

// unresolved reports a receipt whose outcome awaits proof or host review.
func (r Receipt) unresolved() bool { return r.State == "uncertain" && !r.abandonedImage() }

// reserving reports a running execution already permitted to have effects
// whose disposition is not yet durable. A completed setup or a recorded
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

// loadHolds lists the complete receipt inventory. Callers hold s.mu. An
// unreadable inventory is an error, never an empty set of holds.
func (s *Server) loadHolds(exclude string) (holds, error) {
	h := holds{writes: map[Resource][]Receipt{}}
	all, err := s.Receipts.List()
	if err != nil {
		return h, err
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

// unresolvedGlobal reports space-wide holds from finished outcomes. Running
// reservations are checked at the effect gate, not at admission.
func (h holds) unresolvedGlobal() []Receipt {
	var out []Receipt
	for _, r := range h.global {
		if r.unresolved() {
			out = append(out, r)
		}
	}
	return out
}

func (h holds) any() bool {
	if len(h.global) != 0 {
		return true
	}
	for _, list := range h.writes {
		for _, r := range list {
			if r.unresolved() {
				return true
			}
		}
	}
	return false
}

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
	var names []string
	for _, r := range resources {
		names = append(names, r.String())
	}
	what := "this space"
	if len(names) != 0 && !global {
		what = strings.Join(names, ", ")
	}
	if !admin {
		return fmt.Errorf("%s is held by an interrupted operation that a host must review; unrelated changes can proceed", what)
	}
	var ids []string
	for _, r := range receipts {
		ids = append(ids, r.ID+" ("+r.Method+", "+r.State+")")
	}
	return fmt.Errorf("%s is held by an interrupted or running operation: %s. "+
		"Inspect it on the host with `yoloctl space operation %s ID`; a finished unknown outcome can be accepted with --acknowledge after review. "+
		"Unrelated changes can proceed", what, strings.Join(ids, ", "), space)
}

// refreshRecovery recomputes the compatibility summary: some outcome holds
// part or all of the space. Callers hold s.mu. Background maintenance
// stays off while it is set.
func (s *Server) refreshRecovery() error {
	h, err := s.loadHolds("")
	if err != nil {
		s.RecoveryRequired = true
		return err
	}
	s.RecoveryRequired = h.any()
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
// already-permitted executions wait for their durable dispositions instead of
// failing on a race with their finalization. An unresolved conflict returns
// at once; the authoritative check under the gate's own save refuses it.
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
		return "Image preparation was abandoned and cannot be published or consumed. Its slot is free; possible surviving build work may still use CPU, memory or disk. Execution result remains unknown; no acknowledgment is required.\n"
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
		return "A host accepted this unknown outcome after review; it was neither retried nor undone and is not known to have succeeded.\n"
	case "command-uncertain":
		return "A box exec command may have run but its result was lost; inspect its effects in the box. It holds nothing.\n"
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
	case "uncertain":
		what := "the whole space, because it has no bounded footprint"
		if scope := s.boundedScope(r); scope != nil && len(scope.Writes) != 0 {
			var names []string
			for _, w := range scope.Writes {
				names = append(names, w.String())
			}
			what = strings.Join(names, ", ")
		}
		reason := "No automatic proof exists for this kind of interruption."
		if r.Verifier != "" && len(r.Proofs) != 0 {
			reason = "Read-back has not established the recorded document postimage; unavailable checks retry automatically, but a mismatch requires host review."
		}
		return fmt.Sprintf("Unknown outcome: it holds %s until a host reviews it. %s Acknowledging neither retries nor undoes it.\n", what, reason)
	}
	return ""
}

// explainHolds summarizes the current holds for the host's receipt list.
func explainHolds(all []Receipt, s *Server) string {
	var barriers strings.Builder
	if s.receiptFailure != nil {
		barriers.WriteString("Receipt storage is not reconciled: changes are paused while the controller rechecks it automatically; acknowledgment is refused.\n")
	}
	if s.draining {
		barriers.WriteString("The controller is in maintenance; recovery does not reopen it.\n")
	}
	var held []string
	global := false
	for _, r := range all {
		if !r.unresolved() {
			continue
		}
		scope := s.boundedScope(r)
		if scope == nil {
			global = true
			continue
		}
		for _, w := range scope.Writes {
			if !slicesContains(held, w.String()) {
				held = append(held, w.String())
			}
		}
	}
	switch {
	case global:
		return barriers.String() + "Changes in this space are paused by an unknown outcome without a bounded footprint; reads still work.\n"
	case len(held) != 0:
		sort.Strings(held)
		return barriers.String() + fmt.Sprintf("Held by unknown outcomes: %s. Changes without overlapping dependencies can proceed when no other barrier applies.\n", strings.Join(held, ", "))
	}
	return barriers.String()
}

func slicesContains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// publicReceipt retains the outcome and footprint without private verifier material.
func publicReceipt(r Receipt) Receipt {
	r.Proofs = nil
	return r
}

// CheckImageCleanup is the short-lived native worker's read-only recheck after
// acquiring its guard. It never waits while holding that guard, and an uncertain
// inventory is never permission for store-wide deletion.
func (store Receipts) CheckImageCleanup(space, disk string, exclude ...string) error {
	s := &Server{Hello: Hello{Space: space}, DiskID: disk, Receipts: store}
	id := ""
	if len(exclude) != 0 {
		id = exclude[0]
	}
	h, err := s.loadHolds(id)
	if err != nil {
		return err
	}
	_, receipts, _ := h.conflict(&Scope{Writes: []Resource{{Kind: ResourceImages}}})
	if len(receipts) != 0 {
		return fmt.Errorf("image store is reserved or held in space %s; no cleanup started; retry after current image work is resolved", space)
	}
	return nil
}

// CheckAuditReconcile refuses startup application while its native domain is
// reserved or held. It never treats current service state as historical proof.
func (store Receipts) CheckAuditReconcile(space, disk string) error {
	s := &Server{Hello: Hello{Space: space}, DiskID: disk, Receipts: store}
	h, err := s.loadHolds("")
	if err != nil {
		return err
	}
	_, receipts, _ := h.conflict(&Scope{Writes: []Resource{{Kind: ResourceAudit}}})
	if len(receipts) != 0 {
		return fmt.Errorf("audit state is reserved or held in space %s; review its operation before reconciling", space)
	}
	return nil
}
