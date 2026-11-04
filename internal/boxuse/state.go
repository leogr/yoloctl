// Package boxuse provides a demand-driven, structured frontend to a managed
// terminal session. It has no workload, controller, or desktop authority itself.
package boxuse

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/leogr/yoloctl/internal/session"
	"github.com/leogr/yoloctl/internal/terminal"
)

const (
	maxRows       = 200
	maxTextBytes  = 32 << 10
	maxInputBytes = 16 << 10
)

type cursor struct {
	Row     int  `json:"row"`
	Col     int  `json:"col"`
	Visible bool `json:"visible"`
}

type activity struct {
	Sequence     uint64 `json:"sequence"`
	PeerRevision uint64 `json:"peer_revision"`
	LastPeer     string `json:"last_peer,omitempty"`
}

type observation struct {
	Revision  uint64      `json:"revision"`
	Rows      int         `json:"rows"`
	Cols      int         `json:"cols"`
	StartRow  int         `json:"start_row"`
	Cursor    cursor      `json:"cursor"`
	Alternate bool        `json:"alternate"`
	Holding   bool        `json:"holding"`
	Title     string      `json:"title,omitempty"`
	Input     activity    `json:"input"`
	Truncated bool        `json:"truncated"`
	Full      bool        `json:"full"`
	Lines     []string    `json:"lines,omitempty"`
	Changed   []rowChange `json:"changed,omitempty"`
}

type rowChange struct {
	Row  int    `json:"row"`
	Text string `json:"text"`
}

// state owns the replica. No external I/O takes place while its lock is held.
type state struct {
	mu              sync.Mutex
	vt              *terminal.Terminal
	session, client string
	screen          observation
	changed         chan struct{}
	lastChange      time.Time
	ended           bool
	// accepted counts this attachment's input bytes that the holder queued.
	accepted uint64
}

func newState() *state { return &state{changed: make(chan struct{})} }

func (s *state) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vt != nil {
		_ = s.vt.Close()
	}
}

func (s *state) notify() {
	close(s.changed)
	s.changed = make(chan struct{})
	s.lastChange = time.Now()
}

func (s *state) end() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
	s.notify()
}

func (s *state) apply(ctx context.Context, e session.ViewEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(e.Snapshot) > 0 {
		vt, err := terminal.RestoreSnapshot(ctx, e.Snapshot, func([]byte) {})
		if err != nil {
			return err
		}
		if s.vt != nil {
			_ = s.vt.Close()
		}
		s.vt = vt
		s.session, s.client = e.SessionID, e.ClientID
		if err := vt.SetHistoryViewLimit(10000); err != nil {
			return err
		}
	}
	if s.vt == nil {
		return errors.New("box-use: missing initial snapshot")
	}
	if e.Size.Rows > 0 {
		if err := s.vt.ResizeGeometry(e.Size); err != nil {
			return err
		}
	}
	if e.EndHold {
		s.vt.EndHold()
	}
	if len(e.Data) > 0 {
		if _, err := s.vt.Write(e.Data); err != nil {
			return err
		}
	}
	// Clipboard, bells and browser integration have no effects in this frontend.
	// Color metadata is irrelevant to plain text; application modes stay native.
	next := s.screen
	if e.Input != nil {
		// Own acceptance always advances the sequence, so the revision below
		// notifies awaitInput. Peer counts never satisfy this attachment.
		if e.Input.Source == s.client {
			if e.Input.Bytes <= s.accepted {
				return errors.New("box-use: invalid input acceptance")
			}
			s.accepted = e.Input.Bytes
		}
		next.Input.Sequence = e.Input.Sequence
		if e.Input.Source != "" && e.Input.Source != s.client {
			next.Input.PeerRevision = e.Input.Sequence
			next.Input.LastPeer = e.Input.Source
		}
	}
	next.Holding = s.vt.Holding()
	if !next.Holding || s.screen.Revision == 0 {
		next.Rows, next.Cols = s.vt.Get(2), s.vt.Get(1)
		next.StartRow = max(0, next.Rows-maxRows)
		lines, err := s.vt.Lines(false, true, next.StartRow, next.Rows-next.StartRow)
		if err != nil {
			return err
		}
		next.Lines, next.Truncated = boundLines(lines)
		next.Truncated = next.Truncated || next.StartRow != 0
		next.Cursor = cursor{Row: s.vt.Get(4), Col: s.vt.Get(3), Visible: s.vt.Get(7) != 0}
		next.Alternate = s.vt.Get(6) != 0
		next.Title = boundedUTF8(s.vt.Title(), 256)
	}
	old := s.screen
	if old.Revision == 0 || !slices.Equal(old.Lines, next.Lines) || old.Cursor != next.Cursor ||
		old.Rows != next.Rows || old.Cols != next.Cols || old.StartRow != next.StartRow || old.Truncated != next.Truncated ||
		old.Alternate != next.Alternate || old.Holding != next.Holding || old.Title != next.Title || old.Input != next.Input {
		next.Revision++
		s.screen = next
		s.notify()
	}
	return s.vt.Err()
}

func boundedUTF8(text string, budget int) string {
	if len(text) <= budget {
		return text
	}
	for budget > 0 && !utf8.RuneStart(text[budget]) {
		budget--
	}
	return text[:budget]
}

func boundLines(lines []string) ([]string, bool) {
	out := make([]string, 0, len(lines))
	budget := maxTextBytes
	for _, line := range lines {
		line = strings.TrimRight(line, " ")
		if len(line)+1 > budget {
			if budget > 1 {
				out = append(out, boundedUTF8(line, budget-1))
			}
			return out, true
		}
		out = append(out, line)
		budget -= len(line) + 1
	}
	return out, false
}

func (s *state) observe(since uint64, previous observation) (observation, observation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	full := s.screen
	full.Lines = slices.Clone(full.Lines)
	full.Full = true
	result := full
	if since != 0 && since == previous.Revision && !previous.Truncated && !full.Truncated &&
		previous.Rows == full.Rows && previous.Cols == full.Cols && previous.StartRow == full.StartRow && previous.Alternate == full.Alternate {
		result.Full = false
		result.Lines = nil
		result.Changed = []rowChange{}
		for i, line := range full.Lines {
			if i >= len(previous.Lines) || line != previous.Lines[i] {
				result.Changed = append(result.Changed, rowChange{Row: full.StartRow + i, Text: line})
			}
		}
	}
	return result, full
}

func (s *state) wait(ctx context.Context, since uint64, timeout, settle time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		revision, changed, last, ended := s.screen.Revision, s.changed, s.lastChange, s.ended
		s.mu.Unlock()
		if since > revision {
			return false, errors.New("since exceeds current revision")
		}
		if ended {
			return false, nil
		}
		if revision > since && time.Since(last) >= settle {
			return false, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return true, nil
		}
		if revision > since {
			remaining = min(remaining, max(0, time.Until(last.Add(settle))))
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// awaitInput returns once the holder has queued all submitted bytes of this
// attachment. That is acceptance, not execution or durability across holder
// death. Cancellation, refusal, disconnect or exit first leaves it uncertain.
func (s *state) awaitInput(ctx context.Context, submitted uint64) error {
	for {
		s.mu.Lock()
		accepted, changed, ended := s.accepted, s.changed, s.ended
		s.mu.Unlock()
		if accepted > submitted {
			return errors.New("holder acknowledged more input than this attachment sent")
		}
		if accepted == submitted {
			return nil
		}
		if ended {
			return fmt.Errorf("session view ended with %d of %d input bytes acknowledged", accepted, submitted)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("holder acknowledged %d of %d input bytes before cancellation", accepted, submitted)
		case <-changed:
		}
	}
}

func (s *state) encode(r request) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return nil, errors.New("session ended")
	}
	if err := validateInput(r); err != nil {
		return nil, err
	}
	if r.Op == "text" {
		text := strings.ReplaceAll(strings.ReplaceAll(r.Text, "\r\n", "\n"), "\r", "\n")
		if s.vt.Mode(2004) {
			return []byte("\x1b[200~" + text + "\x1b[201~"), nil
		}
		if strings.ContainsAny(text, "\n\t") {
			return nil, errors.New("multiline/tab text requires bracketed paste mode; send individual keys otherwise")
		}
		return []byte(text), nil
	}
	k := terminal.Key{Name: r.Key, Action: 1}
	for _, mod := range r.Modifiers {
		switch mod {
		case "shift":
			k.Mods |= 1
		case "ctrl":
			k.Mods |= 2
		case "alt":
			k.Mods |= 4
		case "super":
			k.Mods |= 8
		default:
			return nil, errors.New("unknown key modifier")
		}
	}
	if len(r.Key) == 5 && strings.HasPrefix(r.Key, "key_") && r.Key[4] >= 'a' && r.Key[4] <= 'z' {
		k.Text, k.Codepoint = r.Key[4:], rune(r.Key[4])
		if k.Mods&1 != 0 {
			k.Text = strings.ToUpper(k.Text)
			k.Codepoint = rune(k.Text[0])
		}
	}
	press, err := s.vt.EncodeKey(k)
	if err != nil {
		return nil, err
	}
	k.Action = 0
	release, err := s.vt.EncodeKey(k)
	return append(press, release...), err
}
