# TUI empty panes, tab moves and docking

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] An **empty pane** is a pane with no session (`Pane.Empty`); a tab
holds at most one. The pane prefix with `|` or `-` splits the focused pane at
once with one, which takes focus; bars carry no split buttons. A second is refused with `fill or close the
empty pane first`; a split below 20×5 is refused as any split. Its bar says
`empty` with only ✕; its content is a dim hint naming the ways to fill it.
Focus landing on it, or a press on it, opens the palette: its input.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Transitions:
- **Live palette command:** fills the current tab's empty pane, whichever
  pane is focused, in place; prefix `c` bypasses it for a new tab.
- **Other command:** runs in the overlay; the empty pane stays.
- **Attach request:** from a pane of its own tab, an identical open pane is
  reused first (status `already open in tab N`), else the attach fills it;
  from another tab, the normal route.
- **Tab moves:** prefix `j` or a dock drag fill it with a whole tab.
- **Remove:** Esc on its empty palette line with no suggestion shown, its
  ✕, prefix `x` or `Ctrl+W`; the sibling takes the space. Esc in a draft
  only hides the palette; the next key opens it again.
- **Unchanged:** a failed start (status line), a tab switch and back.
- A tab left with only its empty pane closes.
[1](#source-1) [4](#source-4) [5](#source-5)

[FACT] Every session path checks `Empty`: rendering, the tab title (the
first live pane's), cursor, focus reports, host mouse mode, wheel, selection,
copy, paste (`nothing takes input here`), errors, colours, resize, close
(no `Session.Close`) and shutdown (`eachSession`). [3](#source-3)
[6](#source-6)

[FACT] A **move** grafts a tab's whole pane tree into the current tab as one
operation (`tabs.Model.Join`, `Layout.Graft`): beside a target pane with
half its space, or in place of the empty pane. The preflight lays the joined
tree out as if unzoomed, bars included, and needs every pane at 20×5. A
refusal changes nothing: `that is this tab`, `no tab N`, `tab N has an
empty pane`, `too small for tab ❷'s panes`. A commit closes and starts no
session and keeps every pane; zoom clears, the joined tree's focused pane
takes focus, the source tab leaves the strip and the destination stays
active. Exits, bells and requests find their pane by session where it is now.
[7](#source-7) [8](#source-8)

[FACT] Prefix `j` opens the tab picker in join mode: below the focused pane,
or into the empty pane, as its title says. Rows the move would refuse are
grey with their reason. A digit or Enter acts with the preflight repeated; a
refusal stays in the footer for at least 3 s. The destination (tab, and its
empty or focused pane) is captured at open; a change cancels it with an
inert notice ([pane menu](tui-pane-menu.md)).
Prefix `!` moves the focused pane of a divided tab to a new last tab, which
becomes active (`already its own tab` otherwise). [4](#source-4) [9](#source-9)

[FACT] **Dock drag** (a chrome gesture): a press on a tab label that then
moves by a cell keeps the shown tab and drags the pressed one, its label in
reverse video. The target is the visible pane under the pointer and its
nearest edge, a column counting half a row; the empty pane is whole. Each
motion runs the preflight: a passing half is tinted 256-colour 94 with the
pane's border orange; a refused one is hatched grey with the reason on the
status line, otherwise `drop on a pane: left · right · top · bottom · Esc
cancels`. Borders, bars, the strip and the palette are no target; the active
tab gets `drop needs another tab`. The release re-resolves and re-checks. Esc, host blur, a tab switch, another button, the
dragged tab going or its layout changing cancel. No child receives it. [2](#source-2) [10](#source-10) [11](#source-11)

[FACT] Tests cover graft sides, filling, refusal, drop sides and drawing
(goldens) and the bars; PTY model tests cover the transitions, the audit,
`j`, `!`, the picker, a held key and a gesture across a move, and dock
targets and cancels. The acceptance driver adds the empty pane, a break, a
join and dock drags.
[12](#source-12) [13](#source-13)

## Sources

1. <a id="source-1"></a> [Empty pane](../../internal/tui/placeholder.go#L1-L103) — `openPlaceholder`, `fillPlaceholder`, `inChooser`, `syncPlaceholder`, `placeholderRows`.
2. <a id="source-2"></a> [Bar buttons and drop](../../internal/tui/panes/layout.go#L273-L439) — `Pane.Empty` at L51; `Layout.Empty`, `Replace`, `barButtons` at L701, `barSegs`, `DropAt`, `SetDrop`.
3. <a id="source-3"></a> [View and keys](../../internal/tui/tui.go#L1728-L1819) — `View`, `hostCursor`; `handleKey`, `handleChromeClick`, paste in `Update`.
4. <a id="source-4"></a> [Pane keys](../../internal/tui/pane_prefix.go#L393-L461) — `splitAct`, the `!` and `j` rows of `paneKeys`.
5. <a id="source-5"></a> [Requests](../../internal/tui/tab_requests.go#L39-L78) — `handleTabRequest`; `dispatch` in tui.go.
6. <a id="source-6"></a> [Session paths](../../internal/tui/frames.go#L20-L126) — `eachSession`, `tabFrame`, `scroller`, `syncFocus`; guards in selection, mouse and geometry files.
7. <a id="source-7"></a> [Graft](../../internal/tui/panes/layout.go#L305-L372) — `CanGraft`, `Graft`, `graft`, `fits`.
8. <a id="source-8"></a> [Tab moves](../../internal/tui/tabs/tabs.go#L27-L238) — `Join`, `CanJoin`, `Break`, `ClosePane`, `Tab.Title`; `tabExitMsg` in tui.go.
9. <a id="source-9"></a> [Join picker](../../internal/tui/tab_picker.go#L46-L245) — `openJoinPicker`, `checkPicker`, `pickerOverlay`, `pickTab`; `joinTab`, `canJoin`, `breakPane` in placeholder.go (L105-L175); `popup.Dimmed`.
10. <a id="source-10"></a> [Dock gesture](../../internal/tui/chrome_gestures.go#L106-L234) — `chromeMotion`, `dockTarget`, `dockMotion`, `dockRelease`, `endDock`; the reversed label in `tabStrip`.
11. <a id="source-11"></a> [Drop drawing](../../internal/tui/panes/compose.go#L43-L156) — `drawDrop`, the orange target border in `Compose`.
12. <a id="source-12"></a> [Layout tests](../../internal/tui/panes/move_test.go#L1-L209) — graft, replace, drop and bar tests; goldens `drop-tint`, `drop-hatch`.
13. <a id="source-13"></a> [Model tests](../../internal/tui/pane_moves_test.go#L1-L479) — moves, picker, break, audit, dock; `TestEmptyPaneTransitions` in pane_input_test.go; `TestPanesAcceptanceDriver`.
