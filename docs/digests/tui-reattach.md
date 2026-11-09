# TUI re-attach and view reopen

Verification: `2e70c9288f335135add2780e2607fa818ce8341c` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] Ended panes offer explicit actions:
- `attach`/`box attach`: `[ Re-attach ]`, regardless of exit status.
- Lifecycle auto-attach: Re-attach when a validated host target was retained.
- Interactive chat/space view, followed box/space logs, interactive
  `space ssh/shell` without a remote command, and `box exec` of plain
  sh/bash/zsh/fish with interactive/login flags: `[ Reopen ]`.
- Help, `--box-use`, suppressed lifecycle attachment, finite/remote commands,
  shell scripts/`-c`/stdin modes, editors, secret prompts and standalone views
  are excluded. Running followers are not restarted. [1](#source-1)
  [10](#source-10)

[FACT] A running space view retries telemetry without booting a VM.
Still-live IRC chat instead has its own explicit
[disconnected action](chat-interaction.md). A reopened shell is new, not
restoration of its process/cwd/history; logs may repeat their normal initial tail.
[10](#source-10) [11](#source-11)

[FACT] Every pane privately copies its launch argv (`Pane.Args`), never parsing
`Pane.Command`. Eligible implicit spaces are pinned once: trusted native context,
then the existing saved context/default resolver. The target is inserted as the
proper positional or `--space` before `--`, passed to the child and recorded.
Reopen never resolves it again. Context errors preserve the initial CLI error
path without offering unbound replay. Direct re-attach strips only new-session;
space, passthrough and chat identity/start-consent guards remain.
[1](#source-1) [2](#source-2) [10](#source-10)

[FACT] Lifecycle create/copy/start/restart/recreate retains an ordinary attach
target, not lifecycle argv. After local preparation/recreate offers, host
dispatch publishes resolved space, validated destination (copy NEWNAME), and
applicable no-autoupdate/passthrough. Operator/noninteractive routes publish
none. This means “try box attach”, not “setup succeeded”: remote errors and
uncertainty remain visible; no lifecycle operation is replayed. [7](#source-7)

[FACT] Typed `resume-target` uses the same-binary private request framing,
separate from open-tab actions. Startup claims the descriptor into command
context, marks it close-on-exec and clears its environment variable before
helpers run. Session retains its first valid class-matching target through
completion. Only metadata readers join Done; ordinary callbacks stay asynchronous
to avoid pane-close/UI-loop deadlock. Metadata launches nothing; replacements
have separate Sessions. PTY output is not metadata. [8](#source-8) [9](#source-9)

[FACT] `r` and a left press on the last-row action button both call
`reattachPane`; Caps/Num Lock are ignored. Enter/Ctrl+W still close, scroll/tab
navigation remains available, and other keys are inert. Buttons require enough
width and an on-screen row; palette clipping can hide them, popups stay above,
and clicks remain chrome-owned through release. [1](#source-1) [3](#source-3)
[4](#source-4)

[FACT] Replacement preserves rectangle, split and zoom, focuses the new pane,
drops old gesture owners and closes its Session. Local start failure keeps the
old pane and reports why; stale exits match no replacement. No timer retries.
The end banner uses `EndHint` (`r to re-attach to api` or `r to reopen`).
[1](#source-1) [2](#source-2) [5](#source-5) [6](#source-6)

## Sources

1. <a id="source-1"></a> [Re-attach](../../internal/tui/reattach.go#L1) — `reattachArgs`, `viewEndAction`, `viewActionForPane`, `reattachHint`, `reattachable`, `reattachPane`, `reattachSlots`, `applyReattachButtons`, `reattachClick`.
2. <a id="source-2"></a> [Pane start and keys](../../internal/tui/tui.go#L1567) — `handleExitedTabKey`; `startPane` (L854) sets `Args` and `EndHint`; `Pane.Args` in panes/layout.go.
3. <a id="source-3"></a> [Mouse routing](../../internal/tui/mouse.go#L236-L241) — `handleMouse` calls `reattachClick` after popups and before chrome.
4. <a id="source-4"></a> [Drawing order](../../internal/tui/tui.go#L1801) — `applyReattachButtons` before `applyOverlay`.
5. <a id="source-5"></a> [Tests](../../internal/tui/reattach_test.go#L17) — `TestReattachArgs`, `TestEndedAttachReattachesInPlace`, `TestReattachOnlyEndedAttachViews`, `TestReattachButton`, `TestReattachKeyIgnoresLockModifiers`; `reopen_test.go`, `reopen_launch_test.go` and `reopen_args_test.go` cover non-box actions, actual child argv/context changes, exclusions and clipping.
6. <a id="source-6"></a> [End banner](../../internal/tui/term/session.go#L322) — `writeExitBanner`, `ExitCloseHint`; `TestExitBannerEndHint` in term_test.go; `attachedSetupOutput` in attach.go.

7. <a id="source-7"></a> [Host publication](../../cmd/yoloctl/resume_target.go#L35) — `publishControllerResumeTarget`, `publishResumeTarget`; `callControllerTerminal` in controller_client.go publishes before dispatch; `TestHostLifecycleResumeTargetThroughClientRoot` in resume_target_test.go.
8. <a id="source-8"></a> [Typed framing](../../internal/termio/tabrequest.go#L15) — `TabRequest`, `ValidateResumeTarget`, `ReadTabRequests`; `claimTabRequests` in cmd/yoloctl/resume_target.go and startup in main.go; chat_view.go/space_view.go consume that context channel.
9. <a id="source-9"></a> [Retained target](../../internal/tui/term/session.go#L120) — `Start`, `pump`, `ResumeTarget`; `resumeTargetKind` and `reattachArgsForPane` in internal/tui/resume_target.go; resume_target_test.go in term and tui, and reattach_completion_test.go.

10. <a id="source-10"></a> [View classification and target pinning](../../internal/tui/reopen_args.go#L1) — `parseReopenView`, `plainInteractiveShell`, `pinReopenArgs`; `reopenSpace` in reopen_space.go mirrors CLI context resolution; `startPane` passes and records pinned argv. Regressions in reopen_args_test.go, reopen_space_test.go and reopen_launch_test.go.
11. <a id="source-11"></a> [Space view reconnect](../../internal/tui/spaceview/run.go#L17) — `Run`, `keepConnected` retry an unavailable stream without booting a VM.
