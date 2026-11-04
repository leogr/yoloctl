package controller

import "context"

// RunMaintenance admits only an idle controller with certain durable state.
// It participates in draining, yields to new mutations, and never blocks reads.
// Its callback may remove only disposable storage using a fresh inventory;
// user operations retain the normal receipt/recovery contract.
func (s *Server) RunMaintenance(ctx context.Context, run func(context.Context) error) (bool, error) {
	s.mu.Lock()
	if s.draining || s.active != 0 || s.RecoveryRequired || s.receiptFailure != nil {
		s.mu.Unlock()
		return false, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	s.backgroundCancel = cancel
	s.active++
	s.idle = make(chan struct{})
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.backgroundCancel = nil
		s.active--
		if s.active == 0 {
			close(s.idle)
		}
		s.mu.Unlock()
	}()
	return true, run(ctx)
}
