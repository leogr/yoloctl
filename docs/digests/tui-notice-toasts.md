# Persistent TUI notices

Verification: `16684468ac1f193e1ee81155621dd08fec236391` and accompanying changes.

[FACT] Outer-model feedback uses a persistent, in-memory top-right toast queue,
never the header or standalone attachment's last row. A new notice selects the
newest entry; previous/next stop at the ends. Dismiss removes the current entry
and selects its successor, or its predecessor at the end. Ordinary typing,
successful commands, palette/tab/space changes and ticks do not erase notices.
Consecutive identical kind, rendered text and origin may share a repeat count;
different tab identities remain distinct even with identical titles. No disk
history or automatic expiry/eviction is added. [1](#source-1) [2](#source-2)

[FACT] Each captured notice retains bounded, escaped prettylog text and its
space/tab display context. It does not retain a closed session. Pane input,
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

[FACT] One geometry function drives paint, hits and cursor occlusion. The toast
is below the fixed dashboard chrome, over palette output too; standalone uses
the full viewport. Text wraps and scrolls without resizing the child. Prev/next,
`i/N` and close controls stay above the body; tiny viewports use a clipped
compact form. A fresh bare Esc dismisses the current toast when no stronger
modal, prefix or held gesture owns it; with no toast it keeps its ordinary
route. Mouse body/close clicks dismiss; prev/next clicks navigate; the wheel
inside scrolls. Outside clicks retain the queue. A consumed key/press keeps its
owner through repeats/releases and new arrivals; an earlier child gesture
finishes at its original child. Unknown pixel geometry is still refused.
[1](#source-1) [2](#source-2) [6](#source-6)

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
