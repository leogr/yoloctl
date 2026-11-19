# Expanding a running step

Verification: `dc857939b14bace057ca9c009367a0672facbaa3` and accompanying changes. Step ordering re-read at `0a1e47d47001eab39274faf83f3af36419b04f2b` and accompanying changes.

[FACT] Every command a TUI pane starts gets one more private inherited
socket, named by `YOLOCTL_TUI_STEP_FD`. It runs TUI→command and carries
only a click's row offset from the pane's cursor row, one decimal per line,
from 0 down to -4096. No terminal input, mouse mode or command line travels
on it. A malformed line ends the reader. A session that was not given the
channel drops the name from its child's environment. The TUI writes with
the image channel's one-second send bound. [1](#source-1) [2](#source-2)

[FACT] The TUI sends a click only for a plain left press and release on the
same cell, with no modifier, in a running pane at its live screen (scrollback
offset 0). The pane's program must have no mouse mode and no alternate
screen, and the row must be at or above the cursor. The local selection
gesture is delivered unchanged. [3](#source-3)

[FACT] At startup a yoloctl command always claims the inherited channel and
removes its name from the environment, so a subprocess cannot inherit it.
Without a steps block it closes the channel; otherwise a reader feeds
`View.Click`. The block ends at the cursor, so the View maps the offset onto
the step that owns that block row. Only a running step that has no
running sub-steps owns its title and output rows. A click toggles that step between
its latest line and as many of its kept lines (tracker `Retained`) as fit
the block's existing Height−2 limit. The earlier lines are counted on one
row and the other rows keep their place. Recent successful and skipped children
can appear as non-clickable rows while their parent runs. Their final summaries
follow the parent's line. Failed and uncertain children print with their bounded
tails as soon as output is writable. Suspended output waits for Resume or Close.
Nothing is saved and the View reads no terminal.
[4](#source-4) [5](#source-5)

[FACT] When a root step that had output lines ends, a `Pointer` may name
where output is kept, and its answer follows the completed step group.
For a host step with a valid space, yoloctl specifically names the separate
Lima launcher log, retaining each call's last 64 KiB:
`VM launcher log: yoloctl space logs SPACE --host`. Transient restart/npm
maintenance details are not saved there; the pointer does not claim full
maintenance retention. Image builds get no durable-output pointer. [5](#source-5)
[6](#source-6) [7](#source-7)

## Sources

1. <a id="source-1"></a> [Channel](../../internal/termio/stepclick.go#L1) — `StepClickFDEnv`, `TakeInheritedStepClicks`, `WriteStepClick`, `ReadStepClicks`; stepclick_test.go.

2. <a id="source-2"></a> [Pane sessions](../../internal/tui/term/session.go#L1) — `Options.StepClicks`, `SendStepClick`; tui.go `startPane`.

3. <a id="source-3"></a> [Click routing](../../internal/tui/mouse.go#L1) — `stepClick`, `mouseOwner.press`; step_click_test.go.

4. <a id="source-4"></a> [Steps block](../../pkg/prettylog/stepview/stepview.go#L1) — `Click`, `blockRows`, `blockRowsWith`, `owners`; tracker.go `Retained`; `TestClickExpandsARunningStep`, `TestTrackerRetainedKeepsAllLines`.

5. <a id="source-5"></a> [Command side](../../cmd/yoloctl/stepview.go#L1) — `installStepView`, `hostLogPointer`; stepview_pointer_test.go; stepview.go `Options.Pointer`, `hadOutput`, `TestPointerUnderACollapsedRoot`.

6. <a id="source-6"></a> [Host diagnostics](host-diagnostics.md) — launcher stderr capture into host.log.

7. <a id="source-7"></a> [Build progress](image-progress.md) — no durable build-log store.
