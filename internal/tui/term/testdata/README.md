# Terminal compatibility corpus

`fidelity.json` is a hand-authored acceptance corpus, not a recording of the
current emulator. Run it with:

```sh
go test -run TestTerminalFidelity -count=1 ./internal/tui/term
```

This strict acceptance gate is included in the ordinary regression suite.
Every failure is reported; none is skipped or
converted into a golden. `go test -json` provides machine-readable results.
Each scenario runs whole and with 1, 2, 3 and 7 byte chunks, including splits
inside UTF-8 and terminal controls. Panics are failures, and do not prevent the
remaining scenarios from running. Candidate engines can replace
`fidelityFactory` using a Go overlay without changing the oracle.

Coordinates are zero-based. `lines` compares specified screen rows with trailing
spaces removed, **not** styled cells or pixel output. `cursor` checks the physical
cursor. Geometry must remain bounded after every scenario. `replies` requires
exact bytes, including absence of unexpected replies. A `resize` step is
`[rows, columns]`.

## Evidence and scope

The `source` field refers to these expectation sources:

| Source | Expectation |
|---|---|
| `xterm` | [Xterm control sequences](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html), C0, ESC, CSI and private-mode definitions: LF preserves the column, CR resets it; CNL/CPL reset column; DECOM offsets CUP; DECAWM controls wrapping; ICH/DCH/ECH are clipped to a row; 1047/1049 control alternate screens; RIS resets state; DSR/CPR have defined reply forms. Accessed 2026-10-01. |
| `kitty` | [Kitty keyboard protocol](https://sw.kovidgoyal.net/kitty/keyboard-protocol/#progressive-enhancement), query/set/push/pop and separate normal/alternate stacks. A zero-flags reply advertises support; it is not a refusal. Accessed 2026-10-01. |
| `unicode` | [Unicode UAX #11](https://www.unicode.org/reports/tr11/) and [UAX #29](https://www.unicode.org/reports/tr29/), plus the project's explicit cell policy: CJK wide glyphs occupy two columns, combining accents stay with their base, and a wide glyph that cannot fit wraps intact. Unicode classification alone does not specify a complete terminal policy. |
| `grapheme-policy` | Project acceptance requirement: the ZWJ sequence woman-technologist occupies two columns. Native comparison must name the terminal version and width configuration; this is not claimed universal terminal behavior. |
| `geometry` | Project invariant: resize bounds both the grid and cursor; later output cannot silently regrow the requested dimensions. |
| `reflow-policy` | Project acceptance requirement: shrinking and restoring a normal-screen width preserves this simple text line. Native reflow/scrollback comparisons remain necessary; this case does not define all resize semantics. |

[FACT] These tests exercise yoloctl's VT adapter directly. They do not exercise
Bubble Tea's host renderer, key decoding/routing, PTY/session transport or a
graphical terminal. Passing them is not proof of native parity.

The `kitty-old-crash-sequence` case covers the reported sequence followed by a
resize, with the same fragmentation schedules as other cases. It is not a
reproduction of the lost historical session or its surrounding byte stream.

A fidelity percentage may describe only this enumerated corpus, with failures
and untested areas disclosed. Never present it as a percentage of all terminal
behavior. Native acceptance still needs named Kitty/Ghostty versions and tests
for selection, focus, modifiers, IME, resize, styled cells, scrollback, reconnect
and actual agent interaction. Image input and browser integration are separate.
