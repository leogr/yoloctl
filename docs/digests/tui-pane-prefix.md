# TUI pane prefix

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] `tui.yaml pane_prefix` (default `ctrl+q`) is a second reserved key
beside the palette key. It is parsed as a chord: modifiers in any order, then
one printable key; a chord that would type text (no modifier but Shift) is
refused. It matches with lock bits masked, by a layout's base key, by Kitty's
shifted symbol (`Ctrl+Shift+6` for `Ctrl+^`) and by the raw C0 byte of any
Ctrl chord. An unparsable prefix, or one equal to the palette key as a chord,
falls back to the first of `Ctrl+Q`, `Ctrl+^` that differs from the palette
key, with a status notice at startup. [1](#source-1) [2](#source-2)

[FACT] Key precedence, first match wins: a browser prompt; the close popup;
an open tab picker; the inert notice popup; an armed prefix; a resize repeat
window; the prefix key; then the ordinary routes. It arms from a child, a busy
console, the palette or an exited pane, not standalone, and opens the
[pane menu](tui-pane-menu.md), which owns the keys, clicks, refusals and
closing; the input line keeps its idle legend. Modifier reports do not
disarm. A tab switch, host blur or a paste close it and then take their
normal route. No timeout. A key these layers take stays taken through its repeats and release,
after its state ends or focus moves; a key a child already owns keeps its
release, and a repeat is never a pane key. [3](#source-3) [4](#source-4)

[FACT] A second prefix goes, as a press and paired release, to the surface
captured at arming: the child or the busy console. The editor ignores
it; an exited pane refuses in the menu; a gone surface consumes it. [3](#source-3)

[FACT] `paneKeys` is the one key table behind dispatch, the menu and the
key tests:

| After the prefix | Action |
|---|---|
| `\|`, `-` | Open an empty pane right of or below the focused pane ([empty panes and moves](tui-pane-moves.md)). |
| arrows, `o` | Focus that way; the next pane in tree order. |
| `z`, `=`, `x` | Zoom; equalize; detach the focused pane ([close confirmation](tui-close-confirm.md)). |
| `{`, `}` | Swap with the previous or next pane, wrapping; focus moves with it. |
| `Ctrl`+arrows, `Alt`+arrows | Move the focused pane's border by 1 or 5 cells. |
| `!`, `j` | Break the focused pane out to a new tab; bring a tab in (picker in join mode). |
| `c` | New-tab command entry. |
| `n`, `p`, `1`…`9`, `0` | Next, previous, tab N (`0` is tab 10; a missing N is refused). |
| `w`, `d`, `?` | Tab picker; detach the whole tab; keep the menu open. |

Pane keys return focus to the pane. A border moves as far as a trial layout
keeps every (nested) pane at 20×5. For a second after a resize
(renewed by each), Ctrl/Alt+arrows need no prefix; any other key ends that
window and takes its route; nothing is replayed. [4](#source-4)
[5](#source-5)

[FACT] `c` stashes the draft and opens an empty line showing the dim
placeholder `new tab: type a command · Esc cancels` inside the input (then a
dim `new tab · Esc cancels` legend once typing); a busy console refuses it. A live command opens a
new tab and leaves an empty pane as it is; an ordinary one runs in the overlay.
The draft returns after the submission and on Esc. A tab switch or the prefix
also ends it: an empty line takes the draft back, typed text keeps it in
history.
[3](#source-3) [6](#source-6)

[FACT] The picker draws in the [pane overlay](tui-pane-menu.md) at the focused
pane, without a host cursor or palette changes. Its highlight binds to a `*Tab` and follows renumbering; a closed tab
hands it to the tab on its row. Arrows move, Enter switches, a digit switches
at once, a missing number is refused in its footer for at least 3 s whatever
keys follow; Esc closes; other keys
and pastes are consumed, host blur leaves it open and a browser prompt closes
it. Scrolling and hitboxes use `popup.layout` (wrapped footer, unframed
size). A press on a shown row switches; any other press
closes it; both are consumed through release. [7](#source-7)

[FACT] Tests cover chords, fallback and host forms; no leak to a Kitty child;
the double prefix per surface; each disarm event; the repeat window; the
table mapping onto menu rows; the pane and tab keys, `c`, the picker at
narrow sizes, the idle legend and standalone. PTY tests skip without `fs.pty.allocate`. [6](#source-6) [8](#source-8)

## Sources

1. <a id="source-1"></a> [Config](../../internal/config/tui.go#L19-L130) — `PanePrefix`, `DefaultPanePrefix`, `GetPanePrefix`; the documented default in `defaultTUIYAML`.
2. <a id="source-2"></a> [Chords](../../internal/tui/keys.go#L56-L170) — `chord`, `parseChord`, `matches`, `controlByte`, `resolvePanePrefix`; `Run` reports the notice.
3. <a id="source-3"></a> [Prefix](../../internal/tui/pane_prefix.go#L1-L355) — `prefixLayer`, `armPrefix`, `disarmPrefix`, `checkPrefix`, `prefixedKey`, `forwardPrefix`, `takeConsumedRelease`, `resizeBorder`, `startNewTab`, `cancelNewTab`, `endNewTab`; `dispatch` and `inputLine` in tui.go.
4. <a id="source-4"></a> [Key table](../../internal/tui/pane_prefix.go#L356-L498) — `paneBinding`, `paneKeys`, `splitAct`; disarm calls in `Update` and `handleMouse`.
5. <a id="source-5"></a> [Layout](../../internal/tui/panes/layout.go#L580-L660) — `FocusNext`, `SwapFocused`, `ResizeFocused`; `fits` at L912.
6. <a id="source-6"></a> [Prefix tests](../../internal/tui/pane_prefix_test.go#L1-L969) — `TestConsumedKeysStayConsumedThroughRepeatAndRelease`, `TestResizeRepeatWindow`, `TestOverlaysFollowTheRenderedLayout`, `TestPaneKeyTableIsTheMapping` and others.
7. <a id="source-7"></a> [Tab picker](../../internal/tui/tab_picker.go#L1-L268) — `tabPicker`, `checkPicker`, `pickerOverlay`, `pickerLayout`, `pickerKey`, `pickerClick`.
8. <a id="source-8"></a> [Layout and input tests](../../internal/tui/panes/layout_test.go#L129-L176) — `TestKeyboardResizeKeepsNestedMinimum`, `TestFocusNextAndSwap`; `TestPrefixPaneKeys` in pane_input_test.go.
