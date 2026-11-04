package managedchat

import (
	"errors"
	"strings"
)

// ValidateNickname accepts a portable subset of AIRC's ASCII nickname grammar,
// excluding its punctuation case-fold aliases. Display names never change an
// authenticated account, membership, or box identity.
func ValidateNickname(nick string) error {
	if len(nick) == 0 || len(nick) > 24 {
		return errors.New("nickname must contain 1 to 24 ASCII letters, digits, underscores or hyphens and start with a letter or underscore")
	}
	for i, c := range []byte(nick) {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '_' && (i == 0 || (c < '0' || c > '9') && c != '-') {
			return errors.New("nickname must contain 1 to 24 ASCII letters, digits, underscores or hyphens and start with a letter or underscore")
		}
	}
	return nil
}

// SetHumanNickname changes the next client connection's display nickname. It
// never sends IRC NICK, disconnects another view, or rotates credentials. The
// caller publishes client files and explicitly reconnects its own human view.
func (s Store) SetHumanNickname(nick string, generation uint64, inventory Inventory) (*Room, error) {
	if err := ValidateNickname(nick); err != nil {
		return nil, err
	}
	r, err := s.mutation(generation, inventory)
	if err != nil {
		return nil, err
	}
	nicks, err := RoomNicknames(r)
	if err != nil {
		return nil, err
	}
	humanAccount, _ := AuthAccount(r.Human.ID)
	if strings.EqualFold(nick, humanAccount) {
		return nil, errors.New("nickname must differ from an immutable authentication account")
	}
	for _, m := range r.Members {
		account, _ := AuthAccount(m.ID)
		if strings.EqualFold(nick, account) || !m.Revoked && strings.EqualFold(nick, nicks[m.ID]) {
			return nil, errors.New("nickname is reserved for another managed member")
		}
	}
	if r.Human.Nickname == nick {
		return r, nil
	}
	r.Human.Nickname = nick
	r.Generation++
	if err := s.save(r); err != nil {
		return nil, err
	}
	return r, nil
}

// SetOperators records a complete controller-verified role snapshot. Callers
// must prove effective capability and exact live operator/chat binds first;
// this method is not an RPC accepting caller-supplied grants. A later render
// and verified AIRC reload are required before claiming live application.
func (s Store) SetOperators(ids map[string]bool, generation uint64, inventory Inventory) (*Room, error) {
	r, err := s.mutation(generation, inventory)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(r.Members))
	for _, m := range r.Members {
		if !m.Revoked && inventory.Declarations[m.Name] == m.BoxID {
			known[m.ID] = true
		}
	}
	for id, enabled := range ids {
		if !enabled || !known[id] {
			return nil, errors.New("channel operator snapshot contains an inactive or replaced membership")
		}
	}
	changed := false
	for i := range r.Members {
		want := ids[r.Members[i].ID]
		if r.Members[i].ChannelOperator != want {
			r.Members[i].ChannelOperator = want
			changed = true
		}
	}
	if !changed {
		return r, nil
	}
	r.Generation++
	if err := s.save(r); err != nil {
		return nil, err
	}
	return r, nil
}
