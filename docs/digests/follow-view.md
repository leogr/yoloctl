# Live file view

Verification: `d44b7e5ee21ea27b6f1b074972a0c9a07195dbe5` and accompanying changes.

[FACT] Host-only `yoloctl follow PATH [--space SPACE] [--format auto|text|markdown]`
opens one live pane. Default mode pins the selected space and absolute VM path;
relative paths use its controller-owned DefaultWorkdir. Each read or completion
rechecks the running VM and current bootstrap grants. It never boots a VM,
changes mounts, updates a controller or falls back to host reads. `--host`
explicitly reads an ordinarily readable host regular file, relative to launch
cwd, without a VM; it rejects an explicit `--space`. In-box and operator
invocations are refused. Auto mode recognizes `.md` and `.markdown` regardless
of case. [1](#source-1) [2](#source-2)

[FACT] VM reads use existing `vm.ShellCommand` with a fixed finite Python
reader and JSON request on stdin, without sudo or a new service. Directory
handles walk the grant and selected path with no-follow opens; every symlink
component and nonregular leaf is refused. Linux ancestors use search-only
handles, while listing opens only the selected directory for reading. A refusal
suggests a direct path within a current grant. Host mode uses ordinary symlink
semantics but refuses nonregular files. VM completion offers grant roots and
up to 512 guest directory entries, with a short timeout and `NoFileComp` on
success or error; selecting a complete grant root lists its guest children.
It never substitutes host enumeration. Explicit host mode
uses host completion. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Files through 2 MiB are read completely. Above that, only one active
1 MiB window is kept and shown as plain text, even for forced Markdown; the
footer names its byte range and omitted bytes. Page keys browse earlier/later
windows. UTF-8 edges are trimmed without scanning for a newline. Returning
below the threshold restores the selected format. Every refresh hashes the
complete small file or active window, even with unchanged size/mtime. Unchanged
VM bytes return metadata only; a changed response carries the same hashed
buffer. Offset/length/mode join the hash in the comparison key. This is a live
view, not lossless logging or an atomic concurrent-write snapshot.
[2](#source-2) [3](#source-3) [4](#source-4)

[FACT] One-second polling and manual `r` share a single background read/render
job. Resizes coalesce, obsolete results are ignored, and close cancels and
joins owned work; VM command cancellation kills/reaps its owned process group.
Initial display follows the end; scrolling up pauses end-follow while refreshing
the active bytes. `f`/End resumes; source-line position is retained best-effort
and clamped after rewrites. Rename-save follows the pinned name, failures keep
the last display marked unavailable until an actual successful read; cached
resize/source/theme rendering does not clear that state. Changed content clears
selection.
[2](#source-2) [4](#source-4)

[FACT] Markdown parsing/layout/copy geometry comes from `markdownview`, with
unlimited document rows for this caller; chat retains its default row bound.
Ctrl+S toggles source below the size threshold. Wide tables use the shared
[counted fallback ladder](markdown-renderer.md): labelled rows when squeezed
columns become unreadable, or an explicit limit notice with bounded text.
Labels/notices do not enter drag-copy text. As in chat, releasing a drag
(the pointer left the pressed cell) copies the selection's displayed text,
table cells as laid out, through the host `seams.clipboard` callback, with chat's
notices (copied characters/lines, clipboard off, over 1 MiB). A click alone copies
nothing; `y` or Ctrl+Shift+C copies the selection again; clearing the highlight
mid-drag (Esc, Ctrl+S) ends that gesture without a copy. [10](#source-10) In the
TUI an ended follow pane offers [Reopen](tui-reattach.md) with its exact launch
argv (path, `--host`/`--space`, `--format`); an implicit space is the one pinned
at launch. Code-copy controls
use the same policy. Terminal controls are sanitized; links have no opener,
and there are no chat identities, Quote actions or implicit effects. Close
ends the local follow view. Component tests cover current-grant refusal,
changed/unchanged wire payloads, edits/replacement/shrink, complete 2 MiB
Markdown beyond chat's row limit, window browsing, slow reads and cancellation.
These establish component behavior, not native terminal or live backend parity.
[4](#source-4) [5](#source-5) [6](#source-6) [9](#source-9)

[FACT] Body rows keep the terminal background behind and after their text, as
chat transcript rows do. Prose, headings, pipe text and tables have no row fill;
only code cards (and a selection) carry their own background. The title and
status rows keep their fills. A cell-level regression checks the default, dark
and light themes. [7](#source-7) [8](#source-8)

## Sources

1. <a id="source-1"></a> [CLI and completion](../../cmd/yoloctl/follow.go#L66) — `newFollowCmd`, `absoluteFollowPath`, `currentFollowGrants`, `completeFollowPaths`.
2. <a id="source-2"></a> [Sources and transport](../../internal/followview/source.go#L26) — limits, `HostReader.Read`, `VMReader.Read`, `VMReader.call`, `Within`.
3. <a id="source-3"></a> [VM descriptor reader](../../internal/followview/reader.py#L15) — `open_directory`, `open_selected`, `read`, `listing`.
4. <a id="source-4"></a> [View and job ownership](../../internal/followview/model.go#L51) — `renderFile`, `jobs`, `Model.Update`, `scroll`, `View`, `Run`.
5. <a id="source-5"></a> [Source regressions](../../internal/followview/source_test.go#L38) — temporary-file and fixed-reader transport tests; model_test.go covers rendering/input/state and cmd/yoloctl/follow_test.go covers selection/completion.
6. <a id="source-6"></a> [Shared layout option](../../internal/markdownview/document.go#L1) — `LayoutOptions.UnlimitedRows`; internal/tui/confirm_close.go `closeEffect` and tui.go `opensLiveSurface` own pane routing/closing.
7. <a id="source-7"></a> [Body row painting](../../internal/followview/model.go#L492) — `View`, body `Paint` fill.
8. <a id="source-8"></a> [Background regression](../../internal/followview/background_test.go#L18) — `TestBodyRowsKeepTerminalBackground`.
9. <a id="source-9"></a> [Reopen classification](../../internal/tui/reopen_args.go#L24) — `parseReopenView` `follow` case, `pinReopenArgs`; `viewEndAction` in reattach.go; follow cases in reopen_args_test.go, reopen_test.go and reopen_launch_test.go.
10. <a id="source-10"></a> [Copy on release](../../internal/followview/model.go#L394) — `copyText`, `clearSelection`, `mouse` (L433); `TestDragCopiesOnReleaseAndControlsStayInert`, `TestReleaseCopyFollowsChatPolicy`, `TestClearingMidDragEndsTheGesture` in model_test.go; host callback in cmd/yoloctl/follow.go applies `seams.clipboard` and OSC 52 evaluation.
