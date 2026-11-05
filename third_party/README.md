# Pinned host renderer

`ultraviolet/` contains the production Go sources, module files and license from
[Ultraviolet 878653296cfd](https://github.com/charmbracelet/ultraviolet/tree/878653296cfd),
with only [ultraviolet.patch](ultraviolet.patch) applied. The module version and
checksum are pinned by [vendor-ultraviolet.py](../scripts/vendor-ultraviolet.py).
The root [go.mod](../go.mod) uses this copy for both yoloctl and Bubble Tea.
Normal builds remain Go-only.

The patch makes color equality preserve palette identity. Upstream compares
RGB values using its built-in palette, incorrectly merging different indices
or an index and a true color. A host theme can make those colors different.
Basic and indexed spellings of the same palette entry remain equivalent.
The fix covers foreground, background and underline colors, including updates
to existing cells. The regression runs the actual renderer:
[palette_identity_test.go](../internal/tui/palette_identity_test.go).

The input patch parses SGR mouse coordinates as signed decimal fields. The
generic CSI parser stops at a minus sign, causing the rest of a valid pixel
report to become keystrokes. The replacement preserves the decoder's existing
one-based-to-zero-based convention; callers must account for pixel protocols'
different origin. It retains button/modifier classification and bounds numeric
fields. [host_mouse_decoder_test.go](../internal/tui/host_mouse_decoder_test.go)
uses zero/negative reports from a native Ghostty capture and the real fragmented
Bubble Tea input path. It does not enable pixel reporting by itself.

The Kitty decoder also distinguishes key 0 with associated text from legacy
NUL/Ctrl+Space. Text-only events use `KeyExtended`; explicit modifier fields
are retained. Without associated text, the existing control-key decoding stays
unchanged. The real reader/model/PTY and palette regressions are in
[host_compatibility_test.go](../internal/tui/host_compatibility_test.go). The
[protocol](https://sw.kovidgoyal.net/kitty/keyboard-protocol/#text-as-code-points)
defines key 0 for text with no known key; the patch prevents IME text becoming
a shortcut. The yoloctl encoder emits that text according to child negotiation.

The reader patch waits when a read ends between the ESC and `\` of a 7-bit
string terminator. Upstream's OSC and APC/PM/SOS parsers cancel such a reply
and its DCS parser accepts it early, so the next read's `\` became a key (the
TUI's OSC 4 probe, re-sent on every focus-in, made this frequent). The rule
applies only before expiry, to ESC-introduced OSC, DCS, APC, PM and SOS events
longer than two bytes whose last consumed byte is the buffer's final ESC. Lone
Esc and Alt keys, the escape timeout, cancellation and bracketed paste keep
their outcomes. [stray_backslash_test.go](../internal/tui/stray_backslash_test.go)
splits OSC, DCS and APC replies at every byte through the real reader; PM and
SOS share the same guard.

Reproduce with `python3 scripts/vendor-ultraviolet.py --check`; omit `--check`
to regenerate. Maintenance regeneration requires Go and `patch`, and may fetch
the checksum-verified upstream module. Upstream tests are not vendored; yoloctl's
dependency and rendering regressions run in its normal test suite.

When upstream fixes these boundaries, evaluate a pinned upgrade using those
regressions and the terminal corpus, then remove this replacement and patch.
Do not edit other upstream files in this copy.

## Pinned terminal core

The embedded Ghostty WASM uses the archive, Zig version and bridge recorded by
[`build-terminal.py`](../scripts/build-terminal.py). Its
[`native.patch`](../internal/terminal/wasm/native.patch) adds the standard mouse
button 10/11 codes (130/131) missing from the pinned native encoder and adjusts
the upstream unknown-button test. It also encodes the button field as UTF-8
in mode 1005; extra buttons otherwise produced invalid UTF-8. An explicit
already-routed option skips physical-window admission for pixel reports whose
host padding is unknown, preserving valid signed coordinates. Tracking,
modifiers, format encoding and release rules remain native. The adapter tests independent wire bytes
in [`mouse_test.go`](../internal/terminal/mouse_test.go), including legacy
releases and X10 exclusion; TUI tests use a real PTY to check gesture ownership.

The patch also closes an open VT style before the row formatter counts an
unstyled empty cell as blank. Upstream wrote those spaces under the previous
cell's style, so an underline or background ran into cells that a renderer had
erased with ECH. [`terminal_test.go`](../internal/terminal/terminal_test.go)
checks both cases.

Finally the patch adds `resetKeepHistory`, a reset that keeps the primary
screen's rows: `Terminal.resetKeepHistory` repeats `fullReset` except
`Screen.reset`'s page reset (it resets the same cursor pen, hyperlink, semantic
content, charset, keyboard, protection, semantic-prompt, selection and Kitty
graphics state, removes the
alternate screen, and moves the cursor to a fresh line below the last text),
`Handler.resetTransient` drops in-progress APC/DCS/Kitty clipboard state and
grants, and the C wrapper's `resetKeepHistory` returns the stream to a new
parser, UTF-8 decoder and continuation. The Go caller also ends the separate
selection gesture first. Upstream has no such reset and DECSTR is
unimplemented. [`reset_keep_history_test.go`](../internal/terminal/reset_keep_history_test.go)
compares the next command against a fresh terminal after unfinished controls.

Regeneration requires the pinned Zig toolchain and `patch`; normal builds need
neither. The builder hashes the source archive, copied patch, bridge and
artifact, then `--check` requires byte-identical reproduction. Review this
patch when upgrading Ghostty and remove each part once upstream covers its
boundary. Keep the manifest check and focused mouse and row regressions passing.

## Pinned SFTP server

`sftp-v2/` contains production Go sources, module files and LICENSE from
[pkg/sftp v2.0.0-alpha2](https://github.com/pkg/sftp/tree/af6546591fe76b27784e97e5aa6bc11538b719b5),
with two patches. The root module replaces only `github.com/pkg/sftp/v2`;
Firecracker's host share uses its typed handle API. The stable v1 module is an
independent test client. Normal builds need only Go.

- [Count bounds](sftp-v2-count-bounds.patch) takes the decoder changes from
  [upstream PR 649 at a422f1388530](https://github.com/pkg/sftp/pull/649/commits/a422f1388530aaa616ebe3de224d9f6bb6a313cb).
  Counted attributes/names are bounded by available packet bytes before allocation.
- [Server cleanup](sftp-v2-cleanup.patch) registers cleanup before negotiation and
  closes the connection and all remaining file/directory handles on `Serve`
  exit, before releasing its wait group. It does not redesign `GracefulStop`.

Reproduce with `python3 scripts/vendor-sftp.py --check`; omit `--check` to
regenerate. Regeneration uses Go and `patch`, checks the upstream module checksum,
and may download it. Do not directly edit vendored files: keep source, patches
and reproduction synchronized. The license remains alongside the source.

This dependency is an alpha release. Upgrades must retain the independent wire,
v1-client and handle/cleanup/permission/fsync regressions in `internal/vmshare`
(including `go test -race ./internal/vmshare`) and the actual mounted-filesystem
lifecycle smoke. Upstream tests are not vendored. Remove patches only when a
verified pinned upstream version covers their cases. See the
[share contract](../docs/digests/vm-shares.md) for durability and filesystem limits.

## Pinned audit extraction plugin

The yoloctl-owned C++ plugin uses the pinned Falco C ABI and static C++ runtime
notices in [audit-plugin](audit-plugin/README.md). Ordinary Go builds embed the
qualified Linux artifacts; maintenance regenerates with Zig 0.15.2 and checks
both targets twice. See the [metadata digest](../docs/digests/audit-metadata.md)
for the snapshot and qualification limits.
