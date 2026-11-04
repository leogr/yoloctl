# Native terminal snapshots

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] The terminal adapter can capture both screens, saved cursors, styles,
history, keyboard stacks, current/saved/default modes, colors, title and parser
continuation. Its opaque envelope identifies the adapter format, exact embedded
core/bridge SHA-256 and native history retention limit. This is an internal state
representation, not terminal output or a stable backup format. Managed session views use this representation; compatibility attachment retains
a separate bounded byte ring.
[1](#source-1) [2](#source-2) [6](#source-6)

[FACT] Capture and every other operation on the source terminal require owner
serialization. Complete output is capped at 64 MiB. Native continuation tracking
starts with terminal creation and retains at most 32 KiB. An unfinished control
beyond that limit prevents capture; later completion can make capture available
again without replacing or resetting the source. [1](#source-1) [2](#source-2)

[FACT] Restore creates a separate WASM instance. It accepts only a matching
envelope/core, validates native record framing/CRCs and full consumption through
FINISH, and rejects trailing bytes. The bridge checks constructor geometry bounds
including whole-cell pixel dimensions, and the declared history policy before loading history. Each instance retains
the 256 MiB memory ceiling. Native decoding is interruptible by the caller's
context and capped at five seconds; a failed candidate is closed. A successful
terminal has an independent lifetime after the decode context is canceled.
The owner publishes the new instance only after success. [1](#source-1)
[2](#source-2) [3](#source-3)

[FACT] Restore reinstalls reply, size, bell, synchronized-output and reset callbacks,
and the fixed `xterm-256color` identity, without applying constructor mode/color/
cursor defaults over decoded state. Callback counters and local selection/
viewport are fresh. Each managed viewer applies its own visible history cap
after restoring, without changing native retention or snapshot metadata.
Restore emits no historical replies or bells; future output
uses the installed callbacks. Continuation tracking survives subsequent captures.
[2](#source-2) [4](#source-4)

[FACT] Native snapshots preserve measured pixels and the tri-state Shift-mouse
preference. Restoration does not emit size notifications. Subsequent queries
use the restored geometry. [8](#source-8) [9](#source-9)

[FACT] The pinned upstream snapshot format is version 1 and explicitly unstable.
It omits Kitty image resources and glyph registrations. Focus, selection,
viewport, tracked references and caches are destination-local or reset. Adapter
and host effects/filters outside the native core need separate ownership; this
snapshot is not proof that an entire session frontend has been restored.
[5](#source-5) [6](#source-6) [7](#source-7)

[FACT] Regression coverage compares uninterrupted/restored frames, history,
queries and input encoding across state scenarios, subsequent screen switches
and resize. A mixed UTF-8/grapheme/CSI/OSC/DCS/query stream is captured at every
byte cut. Tests cover repeat capture, every truncation of a fixture, damaged
metadata/CRCs, trailing bytes, continuation/size bounds, local-state reset,
live callbacks and export beyond the scratch buffer. A native-payload fuzz
target exercises the same restore entry point. These are local core contracts,
not live Lima or graphical-host parity results. [4](#source-4)

## Sources

1. <a id="source-1"></a> [snapshot.go](../../internal/terminal/snapshot.go#L13) — envelope, `Snapshot`, `RestoreSnapshot`, size/context bounds and error ownership.
2. <a id="source-2"></a> [bridge.zig](../../internal/terminal/wasm/bridge.zig#L20-L121) — creation, callback installation and strict native restore.
3. <a id="source-3"></a> [terminal.go](../../internal/terminal/terminal.go#L39-L203) — interruptible bounded runtime, `New`, isolated instances and call errors.
4. <a id="source-4"></a> [snapshot_test.go](../../internal/terminal/snapshot_test.go#L99) — restoration/continuation/effect/malformed-input tests and `FuzzRestoreSnapshot`.
5. <a id="source-5"></a> [Pinned Ghostty snapshot format](https://github.com/ghostty-org/ghostty/blob/33da6848d63b3bba2b4f31ab1531d618f2795192/src/terminal/snapshot/main.zig#L18-L89) — version 1 at `33da6848`, verified against the hash-pinned archive.
6. <a id="source-6"></a> [holder_view.go](../../internal/session/holder_view.go#L213) — native snapshot admission and compatibility subscription.
7. <a id="source-7"></a> [Pinned Ghostty field classification](https://github.com/ghostty-org/ghostty/blob/33da6848d63b3bba2b4f31ab1531d618f2795192/src/terminal/snapshot/terminal.zig#L215-L243) — retained, local and omitted state at the same verified revision.
8. <a id="source-8"></a> [geometry_test.go](../../internal/terminal/geometry_test.go#L18) — measured-size restoration.
9. <a id="source-9"></a> [mouse_test.go](../../internal/terminal/mouse_test.go#L59) — Shift-state restoration.
