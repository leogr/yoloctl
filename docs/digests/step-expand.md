# Expanding a running step

Verification: `e05d46a47654617a90ab791905dafbf7f8a9b000` and accompanying changes; re-read cited sources when changing behavior.

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
sub-steps owns its title and output rows. A click toggles that step between
its latest line and as many of its kept lines (tracker `Retained`) as fit
the block's existing Height−2 limit. The earlier lines are counted on one
row and the other rows keep their place. A step that ended collapses as
before; nothing is saved and the View still reads no terminal.
[4](#source-4) [5](#source-5)

[FACT] When a root step that had output lines ends, a `Pointer` may name
where its full output is kept, and its answer is printed under the collapsed
line. yoloctl answers only for a host step with a valid space: output lines
under a host step are Lima launcher output, and `host.log` keeps each call's
last 64 KiB. The line is `full output: yoloctl space logs SPACE --host`.
Image build output is kept nowhere, so it gets no pointer. [5](#source-5)
[6](#source-6) [7](#source-7)

## Sources

1. <a id="source-1"></a> [Channel](../../internal/termio/stepclick.go#L1) — `StepClickFDEnv`, `TakeInheritedStepClicks`, `WriteStepClick`, `ReadStepClicks`; stepclick_test.go.

2. <a id="source-2"></a> [Pane sessions](../../internal/tui/term/session.go#L1) — `Options.StepClicks`, `SendStepClick`; tui.go `startPane`.

3. <a id="source-3"></a> [Click routing](../../internal/tui/mouse.go#L1) — `stepClick`, `mouseOwner.press`; step_click_test.go.

4. <a id="source-4"></a> [Steps block](../../pkg/prettylog/stepview/stepview.go#L1) — `Click`, `blockRows`, `blockRowsWith`, `owners`; tracker.go `Retained`; `TestClickExpandsARunningStep`, `TestTrackerRetainedKeepsAllLines`.

5. <a id="source-5"></a> [Command side](../../cmd/yoloctl/stepview.go#L1) — `installStepView`, `hostLogPointer`; stepview_pointer_test.go; stepview.go `Options.Pointer`, `hadOutput`, `TestPointerUnderACollapsedRoot`.

6. <a id="source-6"></a> [Host diagnostics](host-diagnostics.md) — launcher stderr capture into host.log.

7. <a id="source-7"></a> [Build progress](image-progress.md) — no durable build-log store.
