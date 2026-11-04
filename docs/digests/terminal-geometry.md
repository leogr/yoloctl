# Measured geometry and pixel mouse input

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Geometry contains the text grid and measured cell width/height. Pixel
dimensions are either both zero (unknown) or bounded positive values. The TUI
requests CSI16t on window/focus events, with one outstanding request, a bounded
wait and a coalesced follow-up. It never divides ioctl/window pixel totals to
infer a font: those totals may include padding. Measurements update tabs and
the console, excluding their chrome. No response leaves the last measurement
or unknown dimensions; geometry during an unreported font change is not proven.
[1](#source-1) [2](#source-2) [13](#source-13)

[FACT] The session protocol carries columns, rows and both cell dimensions in
eight-byte resize frames. Invalid geometry is rejected. A local zero-column or
zero-row resize ends that view with the measured grid and nonzero-size remedy,
instead of disguising its validation error as holder loss. Unknown pixel size
0/0 with a valid grid is accepted. [21](#source-21) [22](#source-22) The latest accepted
resize, including a font-only change, defines canonical geometry. Managed
views consume that ordered event; late views restore it from the native
snapshot. Local resize never independently changes a live replica's grid.
The native size callback answers CSI14t/16t/18t and mode 2048 notifications;
only the holder delivers replies to a managed application. Unknown pixels
report zero. [3](#source-3) [4](#source-4) [5](#source-5) [14](#source-14)

[FACT] SSH PTY/window requests send measured text-grid totals or zero, avoiding
the SSH library's assumed 8×8 font. Local PTYs use the same geometry. If totals
exceed the kernel's 16-bit winsize fields, both pixel totals become zero rather
than wrap; CSI replies still retain full dimensions. Once a local child
exits, a resize is local: the PTY that is closing is never reported as an
error. These tests use local PTY children and an in-process SSH peer, not a
live OpenShell server. [6](#source-6) [7](#source-7) [15](#source-15) [16](#source-16) [20](#source-20)

[FACT] Pixel reports use their actual signed, zero-origin position. The pinned
host decoder patch accepts signed SGR fields without leaking suffix digits as
keys, while retaining its cell-oriented minus-one convention. The TUI undoes
that subtraction only after the host acknowledges pixel mode. It uses measured
cells for chrome/selection hit tests, subtracts the child's displayed pixel
origin and retains signed coordinates for delivery. The encoder scales the
position within each cell to the canonical font, rounding to the nearest
pixel. Missing measurements cause an explicit error; cell centers are never
invented. [8](#source-8) [9](#source-9) [10](#source-10) [17](#source-17) [18](#source-18)

[FACT] The native encoder still filters tracking/actions/modifiers. A narrow
option bypasses physical-window admission for already-routed pixel reports:
their host padding is unknown, and a valid host report may be negative. Cell
formats keep native clipping and their original cell coordinates. Input
coordinates are bounded to exactly representable native integers.
[9](#source-9) [11](#source-11) [19](#source-19)

[FACT] Passthrough remains a compatibility view: its resize supplies cells
with unknown pixels, and raw input is not rescaled across different fonts.
Such a resize clears canonical pixel measurements. Managed pixel encoding
then needs a new measured resize; it cannot borrow an unmeasured raw viewer's
font. This is an explicit limit of mixed pixel/passthrough use.
[4](#source-4) [12](#source-12)

[FACT] Regression fixtures include measured 19×42 cells and signed pixel
reports `(0,7)`, `(-1,16)`, `(12,13)` from a user-provided native Ghostty capture.
Tests exercise fragmented input, query replies, quiet snapshots, font-only
resizes, owner retention, acknowledged mode switches, timeout cleanup and
different-font scaling. They do not establish graphical parity, live Lima
behavior or current macOS support. [2](#source-2) [7](#source-7) [10](#source-10)

## Sources

1. <a id="source-1"></a> [host_geometry.go](../../internal/tui/host_geometry.go#L12) — measurement lifecycle.
2. <a id="source-2"></a> [host_geometry_test.go](../../internal/tui/host_geometry_test.go#L13) — padded-window, coalescing and real-reader tests.
3. <a id="source-3"></a> [protocol.go](../../internal/session/protocol.go#L29) — geometry framing and validation.
4. <a id="source-4"></a> [holder_view.go](../../internal/session/holder_view.go#L132) — serialized `resizeView`, notification flush and ordered event.
5. <a id="source-5"></a> [bridge.zig](../../internal/terminal/wasm/bridge.zig#L42) — size callback and restore validation.
6. <a id="source-6"></a> [geometry.go](../../internal/session/geometry.go#L9) — explicit SSH requests.
7. <a id="source-7"></a> [geometry_test.go](../../internal/session/geometry_test.go#L12) — SSH and holder replies.
8. <a id="source-8"></a> [mouse.go](../../internal/tui/mouse.go#L128) — pixel origin/routing.
9. <a id="source-9"></a> [mouse.go](../../internal/terminal/mouse.go#L58) — measured input and scaling.
10. <a id="source-10"></a> [mouse_pixels_test.go](../../internal/tui/mouse_pixels_test.go#L17) — PTY and real-loop negotiation.
11. <a id="source-11"></a> [bridge.zig](../../internal/terminal/wasm/bridge.zig#L532) — already-routed pixel encoding.
12. <a id="source-12"></a> [session_client.go](../../cmd/yoloctl/session_client.go#L137) — cell-only compatibility resize.
13. <a id="source-13"></a> [geometry.go](../../internal/terminal/geometry.go#L8) — bounds and native resize.
14. <a id="source-14"></a> [geometry_test.go](../../internal/terminal/geometry_test.go#L9) — native query/snapshot contracts.
15. <a id="source-15"></a> [pty.go](../../internal/tui/term/pty.go#L73) — bounded kernel pixels.
16. <a id="source-16"></a> [geometry_test.go](../../internal/tui/term/geometry_test.go#L36) — actual local PTY dimensions.
17. <a id="source-17"></a> [ultraviolet.patch](../../third_party/ultraviolet.patch#L35) — signed decoder.
18. <a id="source-18"></a> [host_mouse_decoder_test.go](../../internal/tui/host_mouse_decoder_test.go#L17) — signed/fragmented host input.
19. <a id="source-19"></a> [native.patch](../../internal/terminal/wasm/native.patch#L1) — native report admission option.
20. <a id="source-20"></a> [session.go](../../internal/tui/term/session.go#L306) — `ResizeGeometry` after the child's exit; `TestResizeWhileTheSessionCompletesIsLocal` in geometry_test.go.
21. <a id="source-21"></a> [view.go](../../internal/session/view.go#L242) — bounded local resize failure publication and read-error precedence in `attachView`.
22. <a id="source-22"></a> [attach_geometry_test.go](../../internal/session/attach_geometry_test.go#L98) — local holder/protocol geometry and result controls in both attachment modes.
