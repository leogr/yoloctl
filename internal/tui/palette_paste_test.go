package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A pasted line break used to survive in the editable palette after focus
// returned to a tab. It shifted the displayed grid without shifting its cursor.
func TestHostCompatibilityPalettePasteKeepsCursorWithGrid(t *testing.T) {
	for _, text := range []string{"/workdir/Screenshot.png\n", "box\r\nls\t--space default"} {
		for _, fragmented := range []bool{false, true} {
			t.Run(fmt.Sprintf("%q/fragmented=%t", text, fragmented), func(t *testing.T) {
				m := newTestModel(t, 120, 8)
				s := frameTestTab(t, m)
				_, _ = s.Write([]byte("\x1b[2;1HINPUT> text\x1b[2;8H"))
				m.focusPalette = true
				// Real host input decoding: paste, then hide the palette. No Enter
				// is sent, so pasted line breaks must never execute a command.
				runCompatibilityInput(t, m, "\x1b[200~"+text+"\x1b[201~\x1b[27u", fragmented)
				if m.focusPalette || m.console.Busy() {
					t.Fatal("paste/escape did not return to the existing tab")
				}
				v := m.View()
				rows := strings.Split(ansi.Strip(v.Content), "\n")
				if len(rows) != m.height {
					t.Fatalf("palette paste expanded view to %d rows, want %d: %q", len(rows), m.height, rows)
				}
				if v.Cursor == nil || v.Cursor.X != 7 || v.Cursor.Y != barRows+1 {
					t.Fatalf("cursor = %+v, want (7, %d)", v.Cursor, barRows+1)
				}
				if row := strings.TrimRight(rows[v.Cursor.Y], " "); row != "INPUT> text" {
					t.Fatalf("cursor points at %q instead of the input row", row)
				}
			})
		}
	}
}
