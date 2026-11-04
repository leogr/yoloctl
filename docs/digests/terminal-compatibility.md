# Terminal compatibility evidence and limits

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] `make test-terminal-fidelity` runs both the strict VT corpus and the
host-compatibility tests; ordinary `make check` also includes them. The core
corpus has 35 independent expected results, each tested with five write chunk
schedules. Host cases use literal input/output bytes and both whole-stream and
bytewise reads. Passing this gate measures those cases, not a percentage of all
terminal behavior. [1](#source-1) [2](#source-2) [3](#source-3)

## Input boundary

[FACT] The host-input fixture runs Bubble Tea's actual reader/model, yoloctl's
encoder and ordered input queue, and a raw local PTY peer. The peer selects
its own modes and records input until an ordered sentinel. This detects lost,
changed and extra bytes without accepting only a prefix. The scripted host uses
`TERM=xterm-256color` and leaves geometry/mouse queries unanswered; unknown
capabilities stay unknown. It is not an actual xterm process. [3](#source-3)

[FACT] Cases cover legacy control/navigation/application-cursor keys, a legacy
host feeding a Kitty-enabled child, bracketed/unbracketed Unicode paste, CJK,
combining accents and emoji text, composed-key associated text, and text-only
IME events. Palette input receives committed text too. These tests cover text
already delivered by the host, not its composition interface. [3](#source-3)

[FACT] Kitty key 0 with associated text denotes text without a known key. The
pinned decoder represents it as `KeyExtended`, avoiding its legacy NUL mapping.
The child encoder emits key 0 plus text when both report-all and associated-text
flags are set; otherwise a text-only event retains its UTF-8 payload. It has no
release owner, and a release cannot insert the text again. Key 0 without text
and explicit Ctrl+Space keep their control meaning. [4](#source-4)
[5](#source-5) [6](#source-6)

## Focus and display boundary

[FACT] The focus fixture drives the real host reader with blur/refocus and
palette/tab changes while three PTYs record their input. Two request mode 1004;
one does not. Assertions require each requested transition exactly once at its
owner and no unsolicited reports at the third peer. [7](#source-7)

[FACT] Cursor/cell tests compose the host-facing view in tab and standalone
layouts. They check all DECSCUSR values 0–6, hide/show, suppression after exit,
and fixed cell positions for ASCII, CJK, combining accents and a single-codepoint
emoji under both host width methods. Concurrent frame tests separately check
that cursor and grid are coherent. These fixtures do not measure glyph rasterization,
cursor blink timing, ZWJ font policies or desktop IME candidate placement.
[8](#source-8) [9](#source-9)

[FACT] The isolated live attach smoke covers transport across TUI, standalone
managed and compatibility views, including input, resize, slow readers and
exit. Its guest audit is not a graphical-terminal oracle. [10](#source-10)

## Sources

1. <a id="source-1"></a> [Makefile](../../Makefile#L58-L61) — `test-terminal-fidelity`.
2. <a id="source-2"></a> [fidelity_test.go](../../internal/tui/term/fidelity_test.go#L47-L128) — `TestTerminalFidelity`, independent expectations and chunk schedules.
3. <a id="source-3"></a> [host_compatibility_test.go](../../internal/tui/host_compatibility_test.go#L18-L138) — raw peer, input harness, literal wire and palette cases.
4. <a id="source-4"></a> [Kitty text events](https://sw.kovidgoyal.net/kitty/keyboard-protocol/#text-as-code-points) — text-only key 0 and associated codepoints; accessed 2026-10-06.
5. <a id="source-5"></a> [decoder.go](../../third_party/ultraviolet/decoder.go#L1391-L1490) — `parseKittyKeyboard`, text-only and control-key distinction.
6. <a id="source-6"></a> [terminal_keys.go](../../internal/tui/terminal_keys.go#L31-L244) — `sendKey`, `childKey`, `kittyKey`.
7. <a id="source-7"></a> [host_compatibility_test.go](../../internal/tui/host_compatibility_test.go#L140-L160) — `TestHostCompatibilityFocusOwnership`.
8. <a id="source-8"></a> [host_compatibility_test.go](../../internal/tui/host_compatibility_test.go#L165-L209) — `TestHostCompatibilityUnicodeCellsAndCursor`.
9. <a id="source-9"></a> [frames_test.go](../../internal/tui/frames_test.go#L25-L53) — `TestViewCapturesCursorWithGrid`, concurrent coherent-frame assertions.
10. <a id="source-10"></a> [attach-mixed.py](../../scripts/smoke/attach-mixed.py#L1) — PTY frontends, guest input/resize audit and completion checks.
