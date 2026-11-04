package boxuse

import (
	"errors"
	"slices"
	"unicode"
	"unicode/utf8"
)

// KeyNames returns the supported native names for CLI completion.
func KeyNames() []string {
	names := []string{"enter", "escape", "tab", "backspace", "delete", "insert", "home", "end", "page_up", "page_down", "arrow_up", "arrow_down", "arrow_left", "arrow_right"}
	for c := 'a'; c <= 'z'; c++ {
		names = append(names, "key_"+string(c))
	}
	return names
}

// validateInput checks only properties independent of the running terminal.
// Bracketed paste and negotiated key encoding remain state.encode's authority.
func validateInput(r request) error {
	if r.Op == "text" {
		if r.Text == "" || len(r.Text) > maxInputBytes {
			return errors.New("text must contain 1..16384 UTF-8 bytes")
		}
		if !utf8.ValidString(r.Text) {
			return errors.New("invalid UTF-8 text")
		}
		for _, c := range r.Text {
			if unicode.IsControl(c) && c != '\n' && c != '\r' && c != '\t' {
				return errors.New("text contains a control character; use key for keys")
			}
		}
		return nil
	}
	for _, mod := range r.Modifiers {
		if mod != "shift" && mod != "ctrl" && mod != "alt" && mod != "super" {
			return errors.New("unknown key modifier")
		}
	}
	if !slices.Contains(KeyNames(), r.Key) {
		return errors.New("unknown key; use native names such as enter, arrow_up, key_c")
	}
	return nil
}
