package session

import (
	"errors"

	"github.com/leogr/yoloctl/internal/terminal"
)

// terminalInputState models only the prefix evicted from the replay ring.
// Its native parser understands screen-local Kitty stacks, legacy input modes,
// resets and control strings. Replay restores that input state BEFORE the tail;
// applying a final-state suffix would interrupt a trailing split sequence and
// could double-apply stack operations still in the ring.
//
// This belongs only to compatibility replay, not managed native snapshots.
// The separate passthrough filter removes queries before these bytes arrive;
// display state still has the limitations of bounded byte-tail replay.
type terminalInputState struct {
	vt     *terminal.Terminal
	err    error
	closed bool
	seen   bool
}

// FilterBells follows all live replay bytes in a separate native stream; Feed
// still advances only the evicted prefix. A filter error refuses later replay,
// while the holder continues delivering unmodified live output.
func (s *terminalInputState) FilterBells(p []byte) []byte {
	if s.Init() != nil {
		return nil
	}
	var filtered []byte
	filtered, s.err = s.vt.FilterBells(p)
	return filtered
}

// Initialize before the agent starts: WASM compilation must never stall the
// live output/input lock when the ring first fills.
func (s *terminalInputState) Init() error {
	if s.closed {
		return errors.New("terminal replay tracker closed")
	}
	if s.vt == nil && s.err == nil {
		s.vt, s.err = terminal.NewInputTracker()
	}
	return s.err
}

func (s *terminalInputState) Feed(p []byte) {
	if len(p) == 0 || s.err != nil || s.closed {
		return
	}
	if s.Init() != nil {
		return
	}
	s.seen = true
	_, s.err = s.vt.Write(p)
}

func (s *terminalInputState) Replay() ([]byte, error) {
	if s.closed {
		return nil, errors.New("terminal replay tracker closed")
	}
	if s.err != nil {
		return nil, s.err
	}
	if !s.seen { // no overflow: the retained stream needs no input-mode prelude
		return nil, nil
	}
	return s.vt.InputReplay()
}

// Advance past unexportable parser state, without consuming later ground-state
// output from the tail. A still-unfinished control remains unavailable.
func (s *terminalInputState) Recover(p []byte) (int, error) {
	if s.closed || s.vt == nil {
		return 0, errors.New("terminal replay tracker unavailable")
	}
	n, err := s.vt.WriteUntilGround(p)
	if err != nil {
		s.err = err
	}
	return n, err
}

func (s *terminalInputState) Close() {
	s.closed = true
	if s.vt != nil {
		_ = s.vt.Close()
	}
}
