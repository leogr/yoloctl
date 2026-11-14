package controller

import (
	"errors"
	"sort"
)

// operationView is output only. It deliberately is not the durable receipt
// schema: historical collision records never become current execution state.
type operationView struct {
	Receipt
	Source     string   `json:"source"`
	Historical *Receipt `json:"historical,omitempty"`
	NotStored  bool     `json:"not_stored,omitempty"`
}

func visibleReceipt(p Principal, r Receipt) bool { return p.Admin() || p == r.Principal }

// operationView runs under s.mu and applies visibility independently to both
// admissions sharing an ID. It never substitutes old completion for new work.
func (s *Server) operationView(id string, p Principal) (operationView, error) {
	if err := (Request{ID: id, Dash: -1}).Validate(); err != nil {
		return operationView{}, err
	}
	entry := s.executions[id]
	note, collision := s.collisions[id]
	if entry != nil {
		r := *entry.receipt
		r.RecordedLate = entry.late
		if !visibleReceipt(p, r) {
			if collision && visibleReceipt(p, note.historical) {
				old := s.visibleHistorical(note.historical)
				return operationView{Receipt: Receipt{ID: id, State: "not-recorded"}, Source: "history-only", Historical: &old, NotStored: true}, nil
			}
			return operationView{}, errors.New("operation receipt not available")
		}
		view := operationView{Receipt: publicReceipt(r), Source: "memory", NotStored: collision}
		if collision && visibleReceipt(p, note.historical) {
			old := note.historical
			if latest, err := s.currentHistory(id); err == nil && sameAdmission(latest, old) {
				old = latest
			}
			old = publicReceipt(old)
			view.Historical = &old
		}
		return view, nil
	}
	if collision {
		if !visibleReceipt(p, note.later) {
			if visibleReceipt(p, note.historical) {
				old := s.visibleHistorical(note.historical)
				return operationView{Receipt: Receipt{ID: id, State: "not-recorded"}, Source: "history-only", Historical: &old, NotStored: true}, nil
			}
			return operationView{}, errors.New("operation receipt not available")
		}
		// The ended execution has no stored result. Its process-local identity
		// note must not serialize a remembered completion as a receipt outcome.
		later := Receipt{ID: note.later.ID, Method: note.later.Method, Principal: note.later.Principal,
			Started: note.later.Started, Generation: note.later.Generation, DiskID: note.later.DiskID, State: "not-recorded"}
		view := operationView{Receipt: later, Source: "controller-log", NotStored: true}
		if visibleReceipt(p, note.historical) {
			old := note.historical
			if latest, err := s.currentHistory(id); err == nil && sameAdmission(latest, old) {
				old = latest
			}
			old = publicReceipt(old)
			view.Historical = &old
		}
		return view, nil
	}
	if r, ok := s.historical[id]; ok {
		if !visibleReceipt(p, r) {
			return operationView{}, errors.New("operation receipt not available")
		}
		r.RecordedLate = true
		return operationView{Receipt: publicReceipt(r), Source: "memory"}, nil
	}
	r, err := s.currentReceipt(id)
	if err != nil || !visibleReceipt(p, r) {
		return operationView{}, errors.New("operation receipt not available")
	}
	return operationView{Receipt: publicReceipt(r), Source: "history"}, nil
}

// operationViews returns known memory records even when durable history is
// unavailable. The caller reports that error, never an apparently complete list.
func (s *Server) operationViews() ([]operationView, error) {
	var all []Receipt
	var err error
	if s.nativeStorageFailure != nil {
		err = s.nativeStorageFailure
	} else {
		all, err = s.Receipts.List()
	}
	ids := map[string]bool{}
	for _, r := range all {
		ids[r.ID] = true
	}
	for id := range s.executions {
		ids[id] = true
	}
	for id := range s.collisions {
		ids[id] = true
	}
	for id := range s.historical {
		ids[id] = true
	}
	names := make([]string, 0, len(ids))
	for id := range ids {
		names = append(names, id)
	}
	sort.Strings(names)
	out := make([]operationView, 0, len(names))
	for _, id := range names {
		if historical, ok := s.historical[id]; ok && s.executions[id] == nil {
			if _, collision := s.collisions[id]; !collision {
				historical.RecordedLate = true
				out = append(out, operationView{Receipt: publicReceipt(historical), Source: "memory"})
				continue
			}
		}
		view, e := s.operationView(id, Principal{})
		if e != nil {
			err = errors.Join(err, e)
			continue
		}
		out = append(out, view)
	}
	return out, err
}

func (s *Server) currentHistory(id string) (Receipt, error) {
	if s.nativeStorageFailure != nil {
		return Receipt{}, s.nativeStorageFailure
	}
	return s.Receipts.Read(id)
}
func (s *Server) visibleHistorical(old Receipt) Receipt {
	if latest, err := s.currentHistory(old.ID); err == nil && sameAdmission(latest, old) {
		old = latest
	}
	return publicReceipt(old)
}
