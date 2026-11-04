// Package termio owns host-integration filters shared by direct attaches and
// the embedded TUI.
package termio

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	// MaxOSC52DecodedBytes bounds an agent-controlled host clipboard write.
	// One MiB is ample for copied terminal text while preventing an OSC from
	// turning into an unbounded allocation or terminal write.
	MaxOSC52DecodedBytes = 1 << 20

	// OSC52DisabledNotice is deliberately actionable without implying that
	// clipboard writes are harmless: enabling them lets a confined process
	// influence later commands pasted into a host application.
	OSC52DisabledNotice = "OSC 52 copy blocked by seams.clipboard: false; remove the override or set it to true to allow agent-to-host clipboard writes"

	maxBufferedOSCBytes = 2 * MaxOSC52DecodedBytes
)

// OSC52 is one complete OSC 52 control sequence removed from a child output
// stream. Payload is the encoded clipboard data, not decoded bytes.
type OSC52 struct {
	Selection string
	Payload   string
	Malformed bool
}

// Sequence returns the canonical 7-bit, ST-terminated OSC 52 sequence emitted
// to the host terminal after the event passes policy validation.
func (o OSC52) Sequence() string {
	return "\x1b]52;" + o.Selection + ";" + o.Payload + "\x1b\\"
}

// EvaluateOSC52 applies yoloctl's host-boundary policy. Clipboard reads are
// never allowed; enabled writes target only the explicit
// clipboard selection "c", carry valid base64, and are size-bounded.
func EvaluateOSC52(o OSC52, enabled bool) (sequence, notice string) {
	if !enabled {
		return "", OSC52DisabledNotice
	}
	if o.Malformed {
		return "", "OSC 52 copy blocked: malformed sequence"
	}
	if o.Payload == "?" {
		return "", "OSC 52 clipboard read blocked; yoloctl never exposes the host clipboard to a box"
	}
	if o.Selection != "c" {
		return "", fmt.Sprintf("OSC 52 copy blocked: unsupported selection %q (only \"c\" is allowed)", o.Selection)
	}
	if err := validateOSC52Payload(o.Payload); err != nil {
		return "", "OSC 52 copy blocked: " + err.Error()
	}
	return o.Sequence(), ""
}

func validateOSC52Payload(payload string) error {
	if len(payload) > base64.StdEncoding.EncodedLen(MaxOSC52DecodedBytes) {
		return fmt.Errorf("payload exceeds %d bytes", MaxOSC52DecodedBytes)
	}
	if payload == "" { // clearing the clipboard
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(payload)
	}
	if err != nil {
		return errors.New("payload is not valid base64")
	}
	if len(decoded) > MaxOSC52DecodedBytes {
		return fmt.Errorf("payload exceeds %d bytes", MaxOSC52DecodedBytes)
	}
	return nil
}

// OSC52Scanner incrementally separates OSC 52 controls from ordinary terminal
// output. It carries incomplete OSC sequences across Write boundaries and
// preserves every non-52 byte exactly.
type OSC52Scanner struct {
	pending []byte
}

// Feed consumes p and returns output safe to pass to a terminal emulator plus
// the complete OSC 52 events removed from it.
func (s *OSC52Scanner) Feed(p []byte) (output []byte, events []OSC52) {
	s.pending = append(s.pending, p...)
	input := s.pending
	s.pending = nil

	for len(input) > 0 {
		start, introLen, incompleteAt := findOSC(input)
		if start < 0 {
			// Retain only the suffix whose meaning depends on the next write:
			// either ESC (possibly the first half of ESC ]) or an incomplete
			// UTF-8 rune (whose continuation bytes may equal raw C1 controls).
			if incompleteAt >= 0 {
				output = append(output, input[:incompleteAt]...)
				s.pending = append(s.pending, input[incompleteAt:]...)
			} else {
				output = append(output, input...)
			}
			break
		}
		output = append(output, input[:start]...)
		input = input[start:]

		end, termLen := findOSCTerminator(input, introLen)
		if end < 0 {
			if len(input) > maxBufferedOSCBytes {
				// Do not retain attacker-controlled output without a bound.
				// Passing an overlong, incomplete sequence to the emulator is
				// safe: it is not forwarded to the host clipboard.
				output = append(output, input...)
			} else {
				s.pending = append(s.pending, input...)
			}
			break
		}

		raw := input[:end+termLen]
		if event, ok := parseOSC52(raw, introLen, end); ok {
			events = append(events, event)
		} else {
			output = append(output, raw...)
		}
		input = input[end+termLen:]
	}
	return output, events
}

// Flush returns an incomplete trailing sequence as ordinary terminal output.
// An unterminated OSC cannot perform a clipboard action.
func (s *OSC52Scanner) Flush() []byte {
	out := append([]byte(nil), s.pending...)
	s.pending = nil
	return out
}

// Pending returns a copy of the buffered incomplete sequence without
// consuming it: the bytes withheld from Feed's output whose classification
// depends on the next write. The session holder appends them to an attach
// replay so a client attaching mid-sequence stays byte-continuous with the
// live stream that completes it.
func (s *OSC52Scanner) Pending() []byte {
	return append([]byte(nil), s.pending...)
}

// findOSC locates an OSC introducer without mistaking a UTF-8 continuation
// byte for the raw 8-bit C1 control. incompleteAt is the start of a trailing
// ESC or partial UTF-8 rune that must be carried into the next Feed call.
func findOSC(p []byte) (start, introLen, incompleteAt int) {
	for i := 0; i < len(p); {
		b := p[i]
		if b < utf8.RuneSelf {
			if b == '\x1b' {
				if i+1 == len(p) {
					return -1, 0, i
				}
				if p[i+1] == ']' {
					return i, 2, -1
				}
			}
			i++
			continue
		}

		// Skip a complete, valid UTF-8 rune as one unit. In particular, this
		// keeps E2 9D AF (❯) from treating its 9D continuation as raw C1 OSC.
		if utf8.RuneStart(b) {
			if !utf8.FullRune(p[i:]) {
				return -1, 0, i
			}
			if _, size := utf8.DecodeRune(p[i:]); size > 1 {
				i += size
				continue
			}
		}
		if b == '\x9d' { // standalone 8-bit OSC
			return i, 1, -1
		}
		i++
	}
	return -1, 0, -1
}

// findOSCTerminator returns the terminator's starting offset and byte length.
func findOSCTerminator(p []byte, start int) (int, int) {
	for i := start; i < len(p); {
		b := p[i]
		if b < utf8.RuneSelf {
			switch {
			case b == '\a':
				return i, 1
			case b == '\x1b' && i+1 < len(p) && p[i+1] == '\\':
				return i, 2
			}
			i++
			continue
		}

		// OSC strings may contain UTF-8 (for example a window title). Skip
		// valid runes so a 9C continuation byte is not mistaken for raw C1 ST.
		if utf8.RuneStart(b) && utf8.FullRune(p[i:]) {
			if _, size := utf8.DecodeRune(p[i:]); size > 1 {
				i += size
				continue
			}
		}
		if b == '\x9c' { // standalone 8-bit ST
			return i, 1
		}
		i++
	}
	return -1, 0
}

func parseOSC52(raw []byte, introLen, terminatorStart int) (OSC52, bool) {
	body := string(raw[introLen:terminatorStart])
	command, args, ok := strings.Cut(body, ";")
	if !ok || command != "52" {
		return OSC52{}, false
	}
	selection, payload, ok := strings.Cut(args, ";")
	if !ok {
		// It is OSC 52 but malformed. Surface it to policy as invalid rather
		// than letting an emulator silently consume it.
		return OSC52{Selection: selection, Malformed: true}, true
	}
	return OSC52{Selection: selection, Payload: payload}, true
}

// OSC52Filter is the io.Writer form used by direct attaches. Non-52 output is
// written downstream; OSC 52 events are delivered to handler instead.
type OSC52Filter struct {
	mu      sync.Mutex
	scanner OSC52Scanner
	dst     io.Writer
	handler func(OSC52)
}

func NewOSC52Filter(dst io.Writer, handler func(OSC52)) *OSC52Filter {
	return &OSC52Filter{dst: dst, handler: handler}
}

func (f *OSC52Filter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	output, events := f.scanner.Feed(p)
	if len(output) > 0 {
		n, err := f.dst.Write(output)
		if err != nil {
			return 0, err
		}
		if n != len(output) {
			return 0, io.ErrShortWrite
		}
	}
	for _, event := range events {
		if f.handler != nil {
			f.handler(event)
		}
	}
	return len(p), nil
}

// Flush preserves any incomplete, non-actionable trailing control bytes.
func (f *OSC52Filter) Flush() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	output := f.scanner.Flush()
	if len(output) == 0 {
		return nil
	}
	n, err := f.dst.Write(output)
	if err != nil {
		return err
	}
	if n != len(output) {
		return io.ErrShortWrite
	}
	return nil
}
