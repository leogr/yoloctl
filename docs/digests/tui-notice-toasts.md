# TUI notice history and auto-popup

Verification: `bed24c41066911ae40a5f9c835122716c00f9263` and accompanying changes. Palette arrow routing re-verified at `f75fb9b22c2a9db0644f328c05d55e426bbe7d18` and accompanying changes. Retained bell counts verified at `ca5bf102bdbb658d2f41b8ead982c005ed2b2218` and accompanying changes.

[FACT] Outer feedback is an in-memory notice queue, separate from the header,
standalone result and palette command output. Consecutive identical kind, text
and captured origin/owner merge with a repeat count. Different tab identities
stay distinct. Dismiss removes the selected entry; closing a popup never removes
history. No disk history or queue eviction is added. [1](#source-1)

[FACT] A new dashboard notice opens an unfocused top-right dropdown below the
header, at the same anchor as the space switch, for eight seconds. Keys continue
to their pane/palette. Expiry hides the dialog while retaining its unread entry;
a newer arrival renews the interval. Timer generations reject stale expiry,
including after manual opening. A notice emitted before the event loop gets
its expiry from Init. During focused review, arrivals only update history/the
bell: they do not retarget selection, steal focus or restart expiry.
[1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The top bar shows circled counts of retained notices (`❶!` through
`❿!`) before the space switch. A merged repeat counts as one entry. Above
ten it shows circled buckets (`❿+!`, `❶⓿⓿+!`, and so on). Dismissing the last
entry hides the badge. Its cells are reserved from palette editing and shared
by paint and hit testing. Click the bell, or Ctrl+N while the palette is open,
to open focused history. Ctrl+N is not intercepted in an agent pane or standalone
attachment. Viewing or paging marks the current entry read. Unread entries and
popup focus control emphasis. [2](#source-2) [3](#source-3)

[FACT] Notice history and the space switch share popup opening/focus restoration:
opening replaces a sibling popup, preserves the palette draft and respects
browser/confirmation priority. Esc or an outside left press closes and restores
caller focus. Outside closure consumes its press/release. The visible welcome
text beside the popup keeps its own click and drag actions.
Clicking an auto-popup focuses it and cancels expiry.
The old prefix bracket/scroll notice routes are removed. [1](#source-1)
[2](#source-2) [4](#source-4) [5](#source-5)

[FACT] Focused Left/Right and Tab/Shift+Tab select enabled buttons, including
Dismiss; Enter runs the selected action. `‹`/`›` page notices; PgUp/PgDn scroll
body content. Palette Ctrl+Left/Right and Alt+Left/Right close the popup and switch tabs.
[9](#source-9)
The popup consumes other focused keys, modifiers and paste. Per-key
ownership keeps consumed repeats/releases consumed after closing; earlier
child-owned keys and mouse/selection gestures finish at their original owners,
without granting new focused input to the child. Existing modal/prefix owners
retain precedence. Go to tab resolves the exact live owner and closes/visits;
a closed owner cannot target a replacement tab. [1](#source-1) [2](#source-2)
[5](#source-5)

[FACT] The dialog shares participant frame/button theme, about 70%
wide (maximum 100 columns). Its title shows position and repeat count. Severity
and bounded escaped Box/Space, source Tab, Detail and When fields wrap at
words/path separators under the value column; oversized tokens break to fit.
Captured typed resume targets supply box/space, never a guessed title or retained
closed session. Only the body scrolls; controls/hint stay fixed. Short viewports
use bounded compact geometry; wrapped fields can still require scrolling.
Dashboard placement uses the space switch’s shared top-right header anchor;
standalone retains its centered layout. Unknown pixel geometry remains refused.
[1](#source-1) [6](#source-6) [8](#source-8)

[FACT] Tab-owned notices retain separate `!` attention. Visiting clears the
badge, while removing a notice does not pretend the tab was visited. Standalone
has auto-popup/click/expiry but no top-bar bell, palette shortcut or history
reopen route. Startup error/exit results remain independent of popup closure.
Source/PTy controls are not native graphical/macOS parity evidence.
[1](#source-1) [3](#source-3) [5](#source-5) [7](#source-7)

## Sources

1. <a id="source-1"></a> [Queue and dialog](../../internal/tui/notices.go#L17) — capture, arrival, repeat, geometry, keys, mouse and dismissal.
2. <a id="source-2"></a> [History popup](../../internal/tui/notice_popup.go#L1) — timer, choice selection, popup focus, `noticeChip` retained count, `headerChipsStyled` emphasis and `noticeBellHit` geometry.
3. <a id="source-3"></a> [TUI](../../internal/tui/tui.go#L1) — Init, Update expiry/paste, palette Ctrl+N, header/cursor. `TestNoticeBellCountsRetainedEntriesUntilDismissed` in notice_bell_test.go covers reading, closing, reopening and dismissal.
4. <a id="source-4"></a> [Space popup lifecycle](../../internal/tui/space_switch.go#L90) — beginPopup, restorePopupFocus, openSpacePicker and closeSpacePicker.
5. <a id="source-5"></a> [Input regressions](../../internal/tui/notices_test.go#L1) — notice_dialog_test.go and notice_bell_test.go add focus, expiry, button, gesture, standalone and retained history controls.
6. <a id="source-6"></a> [Shared dialog frame](../../internal/tui/spaceview/dialog.go#L9) — participant/notice border, background and button faces.
7. <a id="source-7"></a> [Standalone](../../internal/tui/attach.go#L21) — attachment prefix, input ownership and final result.
8. <a id="source-8"></a> [Header dropdown anchor](../../internal/tui/popup.go#L9) — shared headerPopupOrigin for notice and space switch.
9. <a id="source-9"></a> [Palette arrow routing](../../internal/tui/pane_prefix.go#L138) — the notice yields to the palette shortcuts after earlier input owners.
