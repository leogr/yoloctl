package session

// Ring is a fixed-capacity byte buffer keeping the most recent writes:
// the holder's screen/history replay source (the node-side-container-log
// analog). Replaying its raw bytes through the client's terminal rebuilds
// recent output. A byte tail alone cannot reconstruct arbitrary screen state.
// Evicted bytes feed terminalInputState, whose input-state prelude runs before
// the retained tail on attach, including any incomplete parser continuation.
//
// Not safe for concurrent use; the holder guards it with its own mutex
// alongside the client list.
type Ring struct {
	max int
	buf []byte
}

// NewRing returns a ring keeping at most max bytes. max must be > 0.
func NewRing(max int) *Ring {
	return &Ring{max: max}
}

// Write appends p and returns a copy of the evicted prefix, in stream order.
// Eviction shifts in place so the backing array stays at ~max bytes instead of
// growing with every append-then-reslice cycle.
func (r *Ring) Write(p []byte) (evicted []byte) {
	if len(p) >= r.max {
		evicted = append(evicted, r.buf...)
		evicted = append(evicted, p[:len(p)-r.max]...)
		r.buf = append(r.buf[:0], p[len(p)-r.max:]...)
		return evicted
	}
	if over := len(r.buf) + len(p) - r.max; over > 0 {
		evicted = append(evicted, r.buf[:over]...)
		n := copy(r.buf, r.buf[over:])
		r.buf = r.buf[:n]
	}
	r.buf = append(r.buf, p...)
	return evicted
}

// Snapshot returns a copy of the buffered bytes, oldest first.
func (r *Ring) Snapshot() []byte {
	return append([]byte(nil), r.buf...)
}

// Discard advances the replay boundary after the tracker consumed this prefix.
func (r *Ring) Discard(n int) { r.buf = r.buf[:copy(r.buf, r.buf[n:])] }
