package chatimage

// IDs allocates 24-bit image and query IDs for one host process: a randomly
// seeded sequence that never returns 0 or an ID it has already returned.
// Random seeding makes collisions with an earlier writer to the same
// terminal unlikely; it is not secrecy or terminal-wide ownership.
type IDs struct {
	next uint32
	used map[uint32]bool
}

// NewIDs starts the sequence at seed (taken modulo 2^24).
func NewIDs(seed uint32) *IDs { return &IDs{next: seed & 0xffffff, used: map[uint32]bool{}} }

// Next returns a new ID, or false when all 2^24-1 IDs have been used.
func (a *IDs) Next() (uint32, bool) {
	for range 1 << 24 {
		id := a.next
		a.next = (a.next + 1) & 0xffffff
		if id != 0 && !a.used[id] {
			a.used[id] = true
			return id, true
		}
	}
	return 0, false
}
