package managedchat

import "errors"

// MatchesReload verifies the deliberately partial reload reply. AIRC omits
// listener/features/account inventory from that acknowledgment, so calling
// Matches on it would reject a successful reload. The caller obtains a full
// status proof next; neither step can substitute a different process instance.
func (a Applied) MatchesReload(want Rendered, previous Applied) error {
	if err := previous.Validate(); err != nil {
		return err
	}
	if a.Incarnation != previous.Incarnation {
		return errors.New("AIRC process incarnation changed during reload; inspect before recovery")
	}
	if a.Generation != want.Generation || a.ConfigSHA256 != want.ConfigSHA256 {
		return errors.New("AIRC reload did not acknowledge the exact desired generation/configuration")
	}
	return nil
}

// MatchesIncarnation is for the full status obtained after a confirmed reload.
// Peer/container/holder verification remains the controller's responsibility.
func (a Applied) MatchesIncarnation(want Rendered, previous Applied) error {
	if err := a.MatchesReload(want, previous); err != nil {
		return err
	}
	return a.Matches(want)
}
