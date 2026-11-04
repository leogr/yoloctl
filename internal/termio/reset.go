package termio

// Terminal renormalization for a child/agent stream that ended without
// restoring the screen — a box stop kills the session holder (or a VM stop
// kills an exec'd shell) mid-frame, leaving mouse/paste/kitty-keyboard modes
// on, a partial scroll region set, and the cursor parked inside the dead UI.
// Any yoloctl message printed straight onto that corpse lands at the parked
// cursor and splices into leftover frame rows ("✗ exit 2ass permissions
// on …"). Emit a reset first, then print on fresh lines.
//
// Two variants, differing only in alternate-screen policy:
//
//   - SessionEndResetInPlace stays on whichever screen the child died on.
//     The TUI tab pump and a TUI-embedded attach client use it so the
//     agent's final frame remains visible above the messages — switching
//     to the main grid would blank the tab (the main screen behind a
//     full-screen agent is mostly empty).
//   - SessionEndResetHost additionally leaves the alternate screen, for a
//     standalone `box attach` in a real terminal: the user's shell resumes
//     on the main screen, and dropping the alt frame there is normal
//     terminal behavior. The park deliberately PRECEDES ?1049l: when the
//     agent died on the alt screen the restore then lands the cursor at
//     the saved main-screen position (continuing right under the shell
//     history, no bottom-of-screen gap), and when it died on the main
//     screen the park stands and ?1049l is a no-op.
//
// Both real terminals and the embedded native core interpret the sequences:
//
//   - CSI r (scroll-region reset) homes the cursor, so the bottom park must
//     come after it;
//   - CSI 999;1 H clamps to the last row of the fixed-size grid.
//
// Neither variant erases anything: callers follow up with their own
// separator newlines and erase-to-EOL per printed line.
const (
	SessionEndResetInPlace = "\x1b[<u" + // pop kitty keyboard enhancements
		"\x1b[?2004l" + // bracketed paste off
		"\x1b[?9l\x1b[?1003l\x1b[?1002l\x1b[?1000l" + // mouse tracking off
		"\x1b[?1005l\x1b[?1006l\x1b[?1015l\x1b[?1016l" + // all extended coordinate formats off
		"\x1b[?1007l\x1b[>0s" + // alternate scroll off; restore Shift selection
		"\x1b[0m" + // drop lingering SGR attributes
		"\x1b[r" + // scroll region → full screen (homes the cursor)
		"\x1b[?6l\x1b[?7h" + // origin mode off, autowrap on
		"\x1b[?25h" + // cursor visible again (agents hide it during redraws)
		"\x1b[999;1H" // park on the bottom row, column 0 (clamped)

	SessionEndResetHost = SessionEndResetInPlace +
		"\x1b[?1049l" // back to the main screen (no-op when already there)
)
