# Persistent TUI notices

Verification: `351da19df8eb0c537435095db835d13678bc5372` and accompanying changes.

[FACT] Outer-model feedback uses a persistent, in-memory centered notice-dialog queue,
never the header or standalone attachment's last row. A new notice selects the
newest entry; previous/next stop at the ends. Dismiss removes the current entry
and selects its successor, or its predecessor at the end. Ordinary typing,
successful commands, palette/tab/space changes and ticks do not erase notices.
Consecutive identical kind, rendered text and origin may share a repeat count;
different tab identities remain distinct even with identical titles. No disk
history or automatic expiry/eviction is added. [1](#source-1) [2](#source-2)

[FACT] Each captured notice retains bounded, escaped prettylog text and its
space, source-pane title and emission time. Typed resume targets supply box/space
fields; a title is never interpreted as a box identity. It does not retain a
closed session. Pane input,
key, mouse, selection, focus, resize and holder-closed notices resolve their
actual session's current tab, including image-input wrappers and background
errors. Invalid tab requests and existing-target feedback belong to the
requesting tab. Other command-result errors keep the palette path. Global
feedback has no tab owner. [1](#source-1) [3](#source-3) [4](#source-4)

[FACT] A tab-owned notice sets separate `NoticeAttention`, including on the
current tab. Its `!` takes precedence over the existing bell/exit/request dot;
pane marks keep their semantics. Explicit tab selection/activation clears
notice attention, including selecting the current tab. Rendering does not.
Visiting clears the badge only; dismissing clears the toast only. Closed-tab
notices retain captured labels without badging replacement tabs. [2](#source-2)
[5](#source-5)

[FACT] One geometry function drives paint, hits and cursor occlusion. The dialog
is centered below dashboard chrome; standalone uses the full viewport. Width is
about 70%, capped at 100 columns, with a usable narrow minimum. It shares the
participant dialog's frame, title and button theme. The title shows `Notice N
of M` and `×count` for repeats. Severity and captured Box/Space, Tab, Detail and
When fields wrap at words/path separators under the value column; oversized
tokens break to fit. Only the body scrolls, without resizing the child. Buttons
and the bottom-border hint stay fixed; buttons flow onto rows when needed.
Short viewports whose wrapped controls leave fewer than three body rows use
a clipped compact form. Wrapped fields may still need scrolling. [1](#source-1) [8](#source-8)
[9](#source-9)

[FACT] Notices appear unfocused: all keys, including Enter, Esc and arrows,
keep their pane/palette routes. Clicking the dialog or prefix `[`/`]` focuses
it, highlighted by the border (compact form: title). Only then Enter runs the
first action, Esc dismisses and Left/Right or `‹`/`›` navigate. Ordinary typing
still follows its pane/palette route. A new arrival, dismissal or outside press
returns focus. Go to tab appears only for the captured live owner; it visits
without dismissing. Dismiss removes the current entry; Previous/Next stop at
the ends. Buttons work without prior focus. Body clicks focus without dismissal;
the wheel inside scrolls. Consumed presses keep ownership through repeats and
releases; earlier child gestures finish at their original child. Existing
modal/prefix owners win; unknown pixel geometry remains refused.
[1](#source-1) [2](#source-2) [6](#source-6) [8](#source-8)

[FACT] Dashboard prefix `[`/`]` navigates and prefix PgUp/PgDn scrolls the
notice. Browser, space/close/tab pickers and pane-menu/inert popups temporarily
suppress toast painting and hits. Standalone uses its existing attachment
prefix for the same commands while notices exist, preserving double-prefix,
detach and no-notice forwarding. Startup errors remain separate from the
presentation queue and command exit still ends attachment. Dock motion hints
are ephemeral content overlays; a refused drop is a persistent global notice.
[1](#source-1) [6](#source-6) [7](#source-7)

## Sources

1. <a id="source-1"></a> [Queue and geometry](../../internal/tui/notices.go#L16) — emission, captured origin, layout, navigation, dismissal, scroll, hits and dock hint.
2. <a id="source-2"></a> [Regressions](../../internal/tui/notices_test.go#L14) — persistence, origin/badges, requests, key/mouse ownership, pixel/tiny viewports and standalone results.
3. <a id="source-3"></a> [Outer routes](../../internal/tui/tui.go#L1195) — `sendInput`, `reportInputErrors`, `reportViewErrors`; frames.go `syncFocus`, host_geometry.go `layoutTab`, terminal_keys.go `sendKey`, mouse.go `deliverMouse`, selection.go `selectMouse`/`copySelection`.
4. <a id="source-4"></a> [Request ownership](../../internal/tui/tab_requests.go#L44) — `handleTabRequest`.
5. <a id="source-5"></a> [Tab attention](../../internal/tui/tabs/tabs.go#L16) — `Tab`, `Select`; tui.go `tabStripLabels`.
6. <a id="source-6"></a> [Prefix routes](../../internal/tui/pane_prefix.go#L183) — `prefixedKey`, `paneKeys`; tui.go `Update`, `View`; mouse.go `handleMouse`.
7. <a id="source-7"></a> [Standalone](../../internal/tui/attach.go#L21) — `RunAttached`, `attachedKey`; chrome_gestures.go `dockMotion`, `dockRelease`, `endDock`.
8. <a id="source-8"></a> [Dialog regressions](../../internal/tui/notice_dialog_test.go#L1) — geometry, field wrapping, repeated notices, exact owner actions and input ownership.
9. <a id="source-9"></a> [Shared dialog frame](../../internal/tui/spaceview/dialog.go#L9) — participant/notice border, background and button faces; chatui/overlay.go uses the same components.
