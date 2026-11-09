# TUI re-attach of an ended box view

Verification: `37fa8447df51ccbea03fea6ce802418e42e3f30e` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] Which ended views offer re-attach:
- **Qualifies:** a pane or tab started by `attach …` or `box attach …`, once
  its child has exited, regardless of exit status; a lifecycle auto-attach
  pane also qualifies when it retained a validated host request target.
- **Does not qualify:** a help request, the structured `--box-use` interface,
  suppressed lifecycle attachment (`--no-attach`, `--manifest-only`), other
  commands (`box logs -f`, `box exec`), and a standalone attach outside the TUI.

Every pane keeps a private copy of its launch argv (`Pane.Args`); re-attach
reads that copy and never re-splits the display string `Pane.Command`. [1](#source-1) [2](#source-2)

[FACT] Re-attach runs that argv again with every `--new-session` spelling
removed, so the box's own conversation resumes. Every other argument is kept
verbatim: `--space`, `--passthrough` and the chat guards, including
`--chat-allow-start`. The box name, when valid, is shown in the hint.
[1](#source-1)

[FACT] Lifecycle `box create/copy/start/restart/recreate` panes retain only
an ordinary attach target, not lifecycle argv. Immediately before host
controller dispatch, after local preparation/recreate offers, the CLI publishes
resolved space and validated destination (copy uses NEWNAME), plus
`--no-autoupdate` and `--passthrough` when applicable. Noninteractive and operator routes publish
none. This means “try ordinary box attach”, not “setup succeeded”: a remote
setup failure, failed attachment or uncertain result retains its original
error and may still offer this explicit action. No lifecycle operation is
replayed. [7](#source-7)

[FACT] Typed `resume-target` metadata uses the same-binary private request
socket, separately from open-tab requests. Startup claims it into command
context, marks it close-on-exec and clears its environment variable before
external tools run. The Session accepts the first valid target for its original
command class, retains a copy across fast completion, and joins the bounded
metadata reader before Done. Ordinary open-tab model callbacks remain
asynchronous so closing a pane cannot wait on its own UI event loop.
Metadata never launches anything; a replacement has a
separate Session. PTY output is not metadata. [8](#source-8) [9](#source-9)

[FACT] Two actions run the same function (`reattachPane`):
- **`r`:** on the focused ended pane, with Caps Lock and Num Lock ignored as
  in the other TUI keys (kitty-protocol terminals report them as modifiers).
  Enter and Ctrl+W still close it, the
  scroll and tab-navigation keys still work, and every other key stays inert.
- **Button:** a left press on the yoloctl-drawn `[ Re-attach ]` button, in
  reverse video at the first cells of the ended view's last row. It works on
  an unfocused pane too, and it is consumed through its release. A press
  beside it takes its normal route.

The button is drawn only when that row is on screen and the pane is wide
enough. An open palette clips the tab area's bottom rows, which can hide a
bottom pane's button but not an upper split pane's. Popups stay on top. [1](#source-1)
[3](#source-3) [4](#source-4)

[FACT] In place: the new view starts with the ended view's rectangle and
`Layout.Replace` puts it in the same slot. Layout and zoom are kept; the
re-attached view is focused (a click focuses the pane it lands on).
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
2. <a id="source-2"></a> [Pane start and keys](../../internal/tui/tui.go#L1567) — `handleExitedTabKey`; `startPane` (L854) sets `Args` and `EndHint`; `Pane.Args` in panes/layout.go.
3. <a id="source-3"></a> [Mouse routing](../../internal/tui/mouse.go#L236-L241) — `handleMouse` calls `reattachClick` after popups and before chrome.
4. <a id="source-4"></a> [Drawing order](../../internal/tui/tui.go#L1801) — `applyReattachButtons` before `applyOverlay`.
5. <a id="source-5"></a> [Tests](../../internal/tui/reattach_test.go#L17) — `TestReattachArgs`, `TestEndedAttachReattachesInPlace`, `TestReattachOnlyEndedAttachViews`, `TestReattachButton`, `TestReattachKeyIgnoresLockModifiers`.
6. <a id="source-6"></a> [End banner](../../internal/tui/term/session.go#L322) — `writeExitBanner`, `ExitCloseHint`; `TestExitBannerEndHint` in term_test.go; `attachedSetupOutput` in attach.go.

7. <a id="source-7"></a> [Host publication](../../cmd/yoloctl/resume_target.go#L35) — `publishControllerResumeTarget`, `publishResumeTarget`; `callControllerTerminal` in controller_client.go publishes before dispatch; `TestHostLifecycleResumeTargetThroughClientRoot` in resume_target_test.go.
8. <a id="source-8"></a> [Typed framing](../../internal/termio/tabrequest.go#L15) — `TabRequest`, `ValidateResumeTarget`, `ReadTabRequests`; `claimTabRequests` in cmd/yoloctl/resume_target.go and startup in main.go; chat_view.go/space_view.go consume that context channel.
9. <a id="source-9"></a> [Retained target](../../internal/tui/term/session.go#L120) — `Start`, `pump`, `ResumeTarget`; `resumeTargetKind` and `reattachArgsForPane` in internal/tui/resume_target.go; resume_target_test.go in term and tui, and reattach_completion_test.go.
