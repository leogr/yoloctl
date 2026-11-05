# TUI re-attach of an ended box view

Verification: `3736b1cf9ac349827782dcbe01070d3e5d922a15` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] Which ended views offer re-attach:
- **Qualifies:** a pane or tab started by `attach …` or `box attach …`, once
  its child has exited.
- **Does not qualify:** a help request, the structured `--box-use` interface,
  any other command (`box logs -f`, `box exec`, lifecycle auto-attach after
  `box start`/`recreate`), and a standalone attach outside the TUI.

Every pane keeps a private copy of its launch argv (`Pane.Args`). The display
string `Pane.Command` is never re-split. [1](#source-1) [2](#source-2)

[FACT] Re-attach runs that argv again with every `--new-session` spelling
removed, so the box's own conversation resumes. Every other argument is kept
verbatim: `--space`, `--passthrough` and the chat guards, including
`--chat-allow-start`. The box name, when valid, is shown in the hint.
[1](#source-1)

[FACT] Two actions run the same function (`reattachPane`):
- **`r`:** on the focused ended pane. Enter and Ctrl+W still close it, and
  every other key stays inert.
- **Button:** a left press on the yoloctl-drawn `[ Re-attach ]` button, in
  reverse video at the first cells of the ended view's last row. It works on
  an unfocused pane too, and it is consumed through its release. A press
  beside it takes its normal route.

The button is drawn only when that row is on screen (an open palette hides
it) and the pane is wide enough. Popups stay on top. [1](#source-1)
[3](#source-3) [4](#source-4)

[FACT] In place: the new view starts with the ended view's rectangle and
`Layout.Replace` puts it in the same slot. Split, zoom and focus are kept.
The ended view's gesture owners are dropped and its session is closed. If the
new view cannot start locally, the ended view stays and the status line says
why. A late exit message for the replaced session matches no pane. There is
no polling and no automatic retry: `box attach` itself starts a stopped box or
refuses with a recreate hint, and the new view shows that outcome.
[1](#source-1) [5](#source-5)

[FACT] The end banner's close line gains the hint after ` · `, for example
`Press Enter to close this tab · r to re-attach to api`, through
`term.Options.EndHint`. Other panes keep the unchanged line. A standalone
attach's setup tail drops the close line by its `ExitCloseHint` prefix.
[2](#source-2) [6](#source-6)

## Sources

1. <a id="source-1"></a> [Re-attach](../../internal/tui/reattach.go#L1-L190) — `reattachArgs`, `reattachHint`, `reattachable`, `reattachPane`, `reattachSlots`, `applyReattachButtons`, `reattachClick`.
2. <a id="source-2"></a> [Pane start and keys](../../internal/tui/tui.go#L1548-L1570) — `handleExitedTabKey`; `startPane` (L852) sets `Args` and `EndHint`; `Pane.Args` in panes/layout.go.
3. <a id="source-3"></a> [Mouse routing](../../internal/tui/mouse.go#L236-L241) — `handleMouse` calls `reattachClick` after popups and before chrome.
4. <a id="source-4"></a> [Drawing order](../../internal/tui/tui.go#L1787-L1790) — `applyReattachButtons` before `applyOverlay`.
5. <a id="source-5"></a> [Tests](../../internal/tui/reattach_test.go#L16-L190) — `TestReattachArgs`, `TestEndedAttachReattachesInPlace`, `TestReattachOnlyEndedAttachViews`, `TestReattachButton`.
6. <a id="source-6"></a> [End banner](../../internal/tui/term/session.go#L248-L277) — `writeExitBanner`, `ExitCloseHint`; `TestExitBannerEndHint` in term_test.go; `attachedSetupOutput` in attach.go.
