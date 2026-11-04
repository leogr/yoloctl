# TUI pane requests, attention and shared sessions

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] An attach request from a `space view` or `chat` pane resolves in this
order:
1. **Reuse:** a pane in any tab whose argv is identical is reused; its tab is
   selected and the pane focused.
   When the requesting pane's tab has an empty pane, the status line says
   `already open in tab N` and the empty pane stays.
2. **Empty pane:** otherwise, if the requesting pane's tab (where it is now)
   has an empty pane, the attach fills it
   ([empty panes and moves](tui-pane-moves.md)).
3. **New tab:** otherwise it opens in a new tab.

A request never moves an existing view. Each pane's request carries its
session, set when its child starts; a request that arrives before then names
no pane. [1](#source-1) [2](#source-2) [7](#source-7)

[FACT] Browser and image-paste requests are matched against every pane of
every tab. A browser prompt still opens at once, as for a background tab.
A tab other than the current one is badged, and a pane other than the
focused one is marked. [3](#source-3)

[FACT] Attention marks:
- **Triggers:** a bell, an exit or a browser request in a pane that is not
  the focused pane of the current tab (or while the palette has focus).
- **Mark:** that pane gets a mark, drawn as a bold orange `●` leading its bar
  ([pane chrome](tui-pane-chrome.md)).
- **Tab badge:** the tab is badged when not current, as before panes.
- **Clearing:** focusing the pane, by key, click or tab selection, clears the
  mark.
- **Bells:** they still reach the host live.
[4](#source-4) [5](#source-5) [8](#source-8)

[FACT] One managed session in two panes (D1) is two viewers of its single
holder. Each sends its own pane size, and the latest resize wins: the other
pane renders a short frame (blank rows below) or a clipped one (cut at its
border, a wide cell becoming a space). Its border stays intact until the pane
resizes again and takes the size back. A test checks this against a real holder
(`session.RunHolder`) and real managed views relayed by `session.Relay`;
only the OpenShell sandbox is faked. [6](#source-6) [9](#source-9)

[FACT] A pixel-mouse child in a pane the host is not reporting pixels for
(it was not focused) receives the cell's centre in measured pixels, relative
to its pane. With no measured cell size the event is not delivered.
An undivided tab keeps its previous behaviour. [10](#source-10)

[FACT] An opt-in acceptance driver (`YOLOCTL_PANES_ACCEPT_OUT=DIR`) runs the
real TUI program with real pane sessions: managed views of the real holder, a
space view sending real attach requests, shells, cell- and pixel-mouse
children and a bell. It feeds the bytes a host terminal sends:
- the palette key, the pane prefix and its keys;
- SGR cell and pixel mouse reports;
- the answers to the cell-size and pixel-mode queries.

It checks 56 steps, the close popup, prefix keys, picker, key sheet, empty pane, moves and dock drags included, and writes each composed screen
as plain text and ANSI.
Browser and image requests and real host rendering are not covered.
[11](#source-11)

## Sources

1. <a id="source-1"></a> [Tab requests](../../internal/tui/tab_requests.go#L1-L78) — `tabRequestMsg.from`, `tabRequestHandler`, `handleTabRequest`.
2. <a id="source-2"></a> [Empty pane](../../internal/tui/placeholder.go#L36-L54) — `fillPlaceholder`; `Layout.Empty` in panes/layout.go.
3. <a id="source-3"></a> [Browser requests](../../internal/tui/host_actions.go#L51-L83) — `browserRequest`: badge and mark.
4. <a id="source-4"></a> [Bells and marks](../../internal/tui/bell.go#L1-L48) — `paneBellMsg`, `paneBellHandler`, `markUnfocused`; exits in tui.go `Update` (`tabExitMsg`).
5. <a id="source-5"></a> [Mark drawing](../../internal/tui/panes/compose.go#L259-L319) — `drawBar`; cleared by `Layout.SetFocus` and `tabs.Model.Select`.
6. <a id="source-6"></a> [D1 test](../../internal/tui/pane_session_test.go#L17-L82) — `TestOneSessionInTwoPanesLatestResizeWins`; `TestPanePTYsHaveTheirPaneSize` follows.
7. <a id="source-7"></a> [Request tests](../../internal/tui/pane_input_test.go#L316) — `TestEmptyPaneTransitions`.
8. <a id="source-8"></a> [Mark tests](../../internal/tui/pane_input_test.go#L429) — `TestBellAndExitMarkAnUnfocusedPane`; composer `TestComposeAttentionMark`.
9. <a id="source-9"></a> [Holder fixture](../../internal/tui/holder_fixture_test.go#L1-L217) — fake sandbox, `startManagedHolder`, `TestManagedRelayChild`, `managedView`.
10. <a id="source-10"></a> [Pixel conversion](../../internal/tui/mouse.go#L131-L169) — `deliverMouse`; `TestPixelChildInAnUnfocusedPaneGetsTheCellCentre` in pane_input_test.go.
11. <a id="source-11"></a> [Acceptance driver](../../internal/tui/panes_acceptance_test.go#L1-L493) — `TestPanesAcceptanceDriver`, `fakeHost`, `driverScript`, `TestFakeSpaceViewChild`.
