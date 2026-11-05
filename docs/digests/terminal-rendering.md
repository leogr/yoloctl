# VT rendering and PTY child lifecycle

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Each terminal owns an isolated libghostty-vt WebAssembly instance under
wazero. The embedded artifact pins Ghostty revision
`33da6848d63b3bba2b4f31ab1531d618f2795192` and Zig 0.16.0; the bridge, native patch
and artifact have manifest hashes. The mouse patch handles buttons 10/11,
UTF-8 fields and already-routed pixel coordinates. A formatter patch closes an
open VT style before erased, unstyled cells, so exported rows do not extend an
underline or background into them. [19](#source-19) [20](#source-20) Normal builds use Go alone.
The maintenance builder needs `patch`, verifies the source archive, rebuilds the private bridge and can require byte
identity. Module memory is capped at 256 MiB, with only reply/row/event imports
and no WASI, filesystem or network imports. Owners serialize calls and close
instances. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Native snapshots restore a separate instance after exact core/version,
size and decoder validation. Snapshot creation does not emit past effects.
Managed attachment restores this state over a private channel; compatibility
attachment retains byte replay.
[15](#source-15)

[FACT] The core owns parsing, screens, native scrollback, modes, color state,
cursor position and Kitty keyboard stacks. The bridge enables grapheme mode
2027 by default, including after RIS; a child can change it. History has a
64 MiB native budget and the adapter bounds visible history to its configured
line count. Rows are formatted as text/SGR/OSC 8 without forwarding terminal
modes, queries or clipboard effects. The TUI captures styled grid/history and
cursor state together; synchronized output, anchored history and focus are
covered by the display adapter. [2](#source-2) [4](#source-4) [13](#source-13)

[FACT] OSC 52 interception precedes core parsing. Measured host foreground and
background become virtual defaults; pending OSC 10/11 queries are answered once
colors arrive. Child color changes remain local; unmodified palette entries
inherit host SGR colors. Live bells cross the host boundary, while native snapshot restoration is quiet and compatibility replay neutralizes
retained bells. These are bounded adapter contracts, not proof of
complete host-terminal fidelity. [1](#source-1) [2](#source-2)
[4](#source-4) [5](#source-5) [6](#source-6) [14](#source-14)

[FACT] Bubble Tea uses the pinned local Ultraviolet copy with color, mouse,
text-input and split-terminator fixes; a read ending at an ST's ESC waits
instead of turning the next `\` into a key. The maintenance checker reproduces
the patched sources. Measured geometry and pixel routing have separate bounded
contracts. [14](#source-14) [17](#source-17) [18](#source-18) [21](#source-21)
[22](#source-22)

[FACT] Typed input and replies share an asynchronous PTY queue, bounded to
4 MiB and 4096 packets including in-flight input. A rejected packet is never
partially enqueued. Partial transport failure stops delivery without retry;
rejection and errors reach the UI. Closing interrupts blocked PTY I/O even when
a descendant retains the slave. Prepared input (such as an image transfer)
reserves one queue position and a 32 KiB result budget. Preparation errors
are visible and skip that packet; later input retains its order.
[7](#source-7) [8](#source-8)

[FACT] Local PTY children receive `TERM=xterm-256color` and `YOLOCTL_TUI=1`. Managed session data uses a separate trusted channel. After direct
child exit, pumps allow 200 ms to drain final output, close the pollable master,
join input/output workers and drain the managed channel with a bounded wait.
Completed managed views recover their local dimensions before appending the
exit banner; resizing an exited tab remains local. A console cannot replace
its command before the old reader and filters finish. Closing releases its
core; session close kills the local client, whose managed attach detaches from
the persistent holder. In-place reset preserves the visible screen;
host-boundary reset also leaves the alternate screen. [9](#source-9)
[10](#source-10) [11](#source-11)

[FACT] Lifecycle tests use real child processes and PTYs to combine sustained
output, concurrent resize/rendering, bounded ordered history and final exit status.
A retained descendant attempts late output after console replacement; repeated
commands must not inherit its text or unfinished clipboard control. These tests
check local process/transport boundaries, not live Lima or graphical-host parity.
[16](#source-16)

[FACT] Ordinary tests include 35 hand-authored core scenarios, each written
whole and in 1/2/3/7-byte chunks: controls, scrolling, editing, wrapping, screens,
reset, replies, Unicode geometry, simple reflow and keyboard negotiation.
`CSI ?0u` reports supported Kitty flags; it does not decline the protocol.
The historical sequence plus resize is tested, but is not a reconstruction of
the lost session. No case is skipped or blessed as current-output goldens.
This corpus checks selected text/cursor/reply contracts, not graphical rendering
or native application parity. [12](#source-12)

## Sources

1. <a id="source-1"></a> [terminal.go](../../internal/terminal/terminal.go#L1-L313) — module imports, memory, ownership, row API, events and cleanup.
2. <a id="source-2"></a> [bridge.zig](../../internal/terminal/wasm/bridge.zig#L42) — private ABI, configured defaults, native rows and keyboard encoder.
3. <a id="source-3"></a> [build-terminal.py](../../scripts/build-terminal.py#L1-L80) — source/toolchain pins, hashes, regeneration and reproduction check.
4. <a id="source-4"></a> [emulator.go](../../internal/tui/term/emulator.go#L1-L249) — synchronization, output interception, reset and close.
5. <a id="source-5"></a> [handler.go](../../internal/tui/term/handler.go#L10) — `SetColors`; shared query handling in `internal/terminal/colors.go`.
6. <a id="source-6"></a> [tui.go](../../internal/tui/tui.go#L1625) — `View`, `hostCursor`, `runningCursor`.
7. <a id="source-7"></a> [input.go](../../internal/tui/term/input.go#L11) — queue bounds, prepared/ordinary input, failures and close.
8. <a id="source-8"></a> [pty_test.go](../../internal/tui/term/pty_test.go#L1-L52) — real PTY close with blocked I/O and retained slave.
9. <a id="source-9"></a> [session.go](../../internal/tui/term/session.go#L66) — `Start`, pumps, reset/banner, `Wait`, `Close`.
10. <a id="source-10"></a> [console.go](../../internal/tui/term/console.go#L15-L255) — direct-child completion, ordered reuse, `Kill`, `Close`.
11. <a id="source-11"></a> [reset.go](../../internal/termio/reset.go#L1-L49) — `SessionEndResetInPlace`, `SessionEndResetHost`.
12. <a id="source-12"></a> [fidelity_test.go](../../internal/tui/term/fidelity_test.go#L1-L128) — strict harness and adjacent JSON/source map; independent expectations and fragmented writes.
13. <a id="source-13"></a> [Display snapshots, history and focus](terminal-display.md) — frame ownership, synchronized output and bounded acceptance evidence.
14. <a id="source-14"></a> [Host colors and live terminal bells](terminal-colors-bells.md) — palette overrides, deferred queries, native event filtering and tests.
15. <a id="source-15"></a> [Native terminal snapshots](terminal-snapshots.md) — encoding, isolated restoration, limits and regression coverage.
16. <a id="source-16"></a> [lifecycle_test.go](../../internal/tui/term/lifecycle_test.go#L92) — retained-descendant console reuse and sustained output/resize/frame/status contracts.
17. <a id="source-17"></a> [Measured geometry and pixel input](terminal-geometry.md) — query ownership, geometry transport, input decoding and limits.
18. <a id="source-18"></a> [Compatibility evidence](terminal-compatibility.md) — committed text, focus/cursor and host-input/PTY acceptance.
19. <a id="source-19"></a> [native.patch](../../internal/terminal/wasm/native.patch#L67-L86) — formatter style close before blank cells.
20. <a id="source-20"></a> [terminal_test.go](../../internal/terminal/terminal_test.go#L96-L114) — `TestErasedCellsDoNotInheritStyle`.
21. <a id="source-21"></a> [terminal_reader.go](../../third_party/ultraviolet/terminal_reader.go#L289-L296) — split string-terminator wait.
22. <a id="source-22"></a> [stray_backslash_test.go](../../internal/tui/stray_backslash_test.go#L159) — `TestSTReplySplitAtEveryPoint`, every split of each ST family.
