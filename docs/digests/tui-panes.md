# TUI panes

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] A tab holds a binary split tree of panes. Each pane hosts exactly one
palette subprocess under a PTY, the unit a tab hosts. A tab with one pane is a
tab as before. Splits are vertical (left | right) or horizontal (top / bottom)
and nest. Sizes are in cells; yoloctl draws the borders and, once a tab
holds two panes, each pane's bar ([pane chrome](tui-pane-chrome.md)).

- **Re-flow:** a host resize re-flows splits proportionally.
- **Minimum:** a pane is at least 20×5 cells of content, bars counted. A split
  that would go below that is refused on the status line before any session
  starts. There is no fixed pane count.
- **Zoom:** shows the focused pane alone with its bar and keeps the tree.
- **Remove:** a removed pane's sibling takes its space; the last pane closes
  the tab.
- **No new mechanism:** panes add no holder, program or persistence.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The focused pane of the current tab is the single source of every
window-level fact:
- the keys a tab takes;
- the window title;
- the host cursor;
- focus reports, which go to that pane only.

Other panes show **no cursor**. The host cursor is the focused pane's frame
cursor, offset by its pane's rectangle. [4](#source-4) [5](#source-5)

[FACT] Each visible pane's session is resized to its own rectangle with the
shared measured cell pixels; unknown pixels stay unknown per pane. A zoomed
tab's hidden panes keep their last size. [4](#source-4)

[FACT] View takes one frame per visible pane. An undivided tab keeps the
single-frame line-truncation path. A tab with two or more panes, zoomed or not,
composes cell by cell:
- each pane's selection-styled rows are clipped to its rectangle;
- a wide cell crossing a pane's right edge becomes a space in its background;
- every pane and border run ends with a style reset, so no style crosses an
  edge;
- borders and bars draw with junctions in grey 243; the focused pane's bar
  and the vertical borders beside it are the active tab's orange 215;
- rows past a short frame are blank, with the border intact.
[5](#source-5) [6](#source-6) [7](#source-7)

[FACT] Pane keys follow the pane prefix (`Ctrl+Q` by default), from a child,
the palette or an exited pane alike: `|`/`-` open an empty pane beside the
focused one, `j` and `!` move tabs and panes ([empty panes and
moves](tui-pane-moves.md)), arrows and `o` move
focus, `z`, `=` and `x` zoom, equalize and detach, `{`/`}` swap panes,
`Ctrl`/`Alt`+arrows move a border by 1 or 5 cells with the 20×5 minimum kept
recursively ([pane prefix](tui-pane-prefix.md)). Every pane key returns focus
to the pane. In an undivided tab the arrows, `z` and `=` are no-ops. Without
the prefix the palette keeps every key, a leading `-` included.
[9](#source-9) [10](#source-10)

[FACT] Mouse input is routed per pane:
- **Press:** a press records its pane as the owner. Each delivery offsets the
  pane's current rows and columns; pixel reports subtract the same origin in
  measured cells. A gesture keeps its pane across focus, palette and size
  changes.
- **Unfocused pane:** a press there focuses it, then is delivered.
- **Hover and wheel:** hover reaches only the focused pane, whose tracking and
  pixel unit the host uses. The wheel acts on the pane under the pointer.
- **Selection:** in a divided tab, local selection is clamped to its pane and
  scrolls at the pane's edges.
- **Close:** closing a pane drops its owners, so a release is never retargeted.
[11](#source-11) [12](#source-12) [13](#source-13)

[FACT] Tests cover the layout (split, refusal, re-flow, zoom, equalize, resize,
arrow focus, removal, hit-testing) and composition goldens rendered through
the real terminal core. PTY-backed model tests cover the undivided path seam,
the per-pane geometry, the focused-only cursor and the title. Those tests skip
without a PTY (`fs.pty.allocate`). [3](#source-3) [6](#source-6) [8](#source-8)

## Sources

1. <a id="source-1"></a> [Layout](../../internal/tui/panes/layout.go#L1-L950) — `Pane`, `Layout`, `CanSplit`, `Split`, `Remove`, `ToggleZoom`, `Equalize`, `Resize`, `MoveFocus`, `FocusNext`, `SwapFocused`, `ResizeFocused`, `At`.
2. <a id="source-2"></a> [Tabs](../../internal/tui/tabs/tabs.go#L1-L238) — `Tab.Layout`, `Focused`, `CloseCurrent`, `ClosePane`, `Owner`, `Select`.
3. <a id="source-3"></a> [Layout tests](../../internal/tui/panes/layout_test.go#L1) — geometry, refusal, focus, resize, removal, hit-testing.
4. <a id="source-4"></a> [Per-pane geometry and focus](../../internal/tui/host_geometry.go#L57-L91) — `resizeSurfaces`, `laidOut` (pane rectangles on demand), `layoutTab` (also resizes sessions); `syncFocus` and `eachPane` in frames.go; `closeFocusedPane`, `startPane` in tui.go.
5. <a id="source-5"></a> [View](../../internal/tui/tui.go#L1726-L1819) — `composePanes`, `View`; `hostCursor` at L1952.
6. <a id="source-6"></a> [Composer](../../internal/tui/panes/compose.go#L1-L346) — `Compose`, `placeRow`, `drawBorders`, `renderRow`; goldens in panes/testdata via compose_test.go.
7. <a id="source-7"></a> [Selection rows](../../internal/tui/selection.go#L120) — `selectionLines` per pane frame.
8. <a id="source-8"></a> [Model tests](../../internal/tui/panes_view_test.go#L1) — undivided path seam, divided composition, cursor, title, benchmarks.
9. <a id="source-9"></a> [Pane keys](../../internal/tui/pane_prefix.go#L356-L498) — `paneKeys`, `paneAct`, `returnToPane`; `dispatch` in tui.go.
10. <a id="source-10"></a> [Input tests](../../internal/tui/pane_input_test.go#L1) — `TestPrefixPaneKeys`, mouse offsets (cell and pixel), D2, owners across focus, resize and close, selection, focus reports, wheel.
11. <a id="source-11"></a> [Mouse routing](../../internal/tui/mouse.go#L23-L169) — `mouseOwner`, `tabTop`, `paneAt`, `paneOwner`, `focusPaneAt`, `dropPaneOwners`, `mouseTarget`, `deliverMouse`; press, hover and wheel in `handleMouse`.
12. <a id="source-12"></a> [Selection](../../internal/tui/selection.go#L21-L81) — `selectionScrollDirection`, `selectionTarget`, `selectMouse`.
13. <a id="source-13"></a> [Host mouse mode](../../internal/tui/host_mouse_encoding.go#L56-L96) — `wantedMouseTracking`, `wantedMousePixels` from the focused pane.
