# TUI pane menu

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] The armed pane prefix shows the **pane menu**, a popup titled `panes`
over the tab area. Its rows come from the key table, grouped: splits, zoom,
spread, swaps, break out, bring a tab in, detach pane; focus, focus next,
resize border; new, next, previous, pick a tab, tab by number, detach whole
tab; send the prefix. Each row is a label with its keys at the right. Every
action row maps to exactly one `paneKeys` binding; the focus arrows, resize
keys, tab digits and the prefix are dimmed information rows whose keys still
act. A row whose binding cannot act now is dimmed (`bindingWhy`): splits that
`CanSplit` refuses or with an empty pane, zoom on an undivided tab, spread and
swaps unless divided, `j`/`w` without another tab, `c` while a palette command
runs, pane and tab actions with no tab. [1](#source-1)

[FACT] There is no highlight and no Tab navigation. A key acts exactly as the
key table dispatches and closes the menu; a dimmed row's key refuses instead;
plain arrows focus panes; Esc closes; Enter does nothing; `?` keeps it open;
the prefix again goes to the captured surface. A left press on an action row
runs it like its key; a press on an information row, separator or frame does
nothing; a press outside closes it. Presses are consumed through release, the
wheel scrolls the body, and motion and releases of earlier gestures finish at
their owners. The overlay takes mouse events before the prefix, chrome and
child routing. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] **Placement (one overlay viewport):** origin one cell inside the
focused pane's top-left, width the widest row plus the frame, rows as many as
fit, both bounded by the part of the pane on screen less 2 (an open palette
hides the tab area's last rows; panes keep their size). A visible part that
cannot hold 20×6, or no tab, centers it over the visible tab area, clamped;
the result is recomputed as the palette grows. The width is fixed first; the
height then follows the body and the footer wrapped at that width
(`fitOverlay`, `popup.rowsFor`), so a long notice never takes rows the body
could have. The overlay is drawn after every base path (divided tab, undivided
tab, welcome text), so opening it never changes palette focus or rows. The
footer is pinned; body rows scroll and hit tests use the rows `popup.layout`
fits. The host cursor is hidden while it shows. The tab picker and the close
confirmation use the same viewport and notice rules: the picker at the
focused pane, the confirmation at the pane being closed (centered when that
pane cannot fit its buttons on one row); only a browser prompt still uses
the palette area. Their refusals stay in their footers for at least 3 s; when
an owner becomes invalid (a changed join destination, a closed tab, a tab
switch) it closes and a live notice moves to the inert popup at its last
origin. [1](#source-1) [4](#source-4)

[FACT] **Notices (D5):** a refusal while the menu is open, or while one of its
actions runs (a split below the minimum, `already its own tab`, `no tab N`, a
busy console for `c`, `nothing takes input here` for a double prefix on an
exited pane, `no such pane key`), shows in the menu footer for at least 3 s
and the menu stays open; then the hint `press a key or click · Esc closes`
returns. Esc, a press outside or a completed action dismiss menu and notice
together. Host blur, a tab switch or a paste close the menu but move a live
notice to an **inert notice popup** at the menu's last origin (clamped to the
visible tab area, moved left only when under 20 columns remain), sized to its
wrapped text, which only shows the text for its remaining time; any key
(consumed) or press dismisses it. A refusal with no menu open opens that
popup at the focused pane. These notices never use the
palette line or a bar. [1](#source-1) [3](#source-3)

[FACT] PTY model tests cover the top-left placement without palette changes,
centered fallbacks (no tab, a narrow pane, a 10×4 host, a host resize), the
undivided path, an open and growing palette (also over a hidden lower pane),
keys acting and closing, Esc, Enter, arrows, row clicks,
information rows, a press outside, wheel scrolling with scrolled hit tests,
dimmed rows, a refusal shown for at least 3 s and renewed, refusals during an
action, the inert popup after blur, tab switch and paste, its kept origin and full
wrapped text after a switch to an unequal layout, and a plain-text
golden of the menu over two panes. [5](#source-5)

## Sources

1. <a id="source-1"></a> [Pane menu](../../internal/tui/pane_menu.go#L1-L532) — `menuRows`, `paneNotice`, `keepNotice`, `bindingWhy`, `runBinding`, `placeOverlay`, `menuOverlay`, `noticeOverlay`, `applyOverlay`, `spliceRow`, `overlayHit`, `overlayMouse`.
2. <a id="source-2"></a> [Prefix dispatch](../../internal/tui/pane_prefix.go#L78-L240) — `prefixRoute`, `prefixedKey`, `disarmPrefix`, `checkPrefix`; `paneKeys` at L421.
3. <a id="source-3"></a> [Mouse routing](../../internal/tui/mouse.go#L196-L203) — `handleMouse` calls `overlayMouse` before the prefix and chrome routes; `mouseTarget` gives no hover owner while it shows.
4. <a id="source-4"></a> [View](../../internal/tui/tui.go#L1776-L1784) — `applyOverlay` on the base rows; `hostCursor`.
5. <a id="source-5"></a> [Menu tests](../../internal/tui/pane_menu_test.go#L1-L622) — `TestPaneMenuOpensAtThePaneTopLeft`, `TestPaneMenuRefusalsStayInTheFooter`, `TestPaneMenuNoticeOutlivesItsMenu`, `TestPaneMenuGolden` and others; golden in internal/tui/testdata.
