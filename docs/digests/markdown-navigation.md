# Markdown file and browser navigation

Verification: `dc857939b14bace057ca9c009367a0672facbaa3` and accompanying changes; follow link relayout re-read at `ff82fb098295a0c75de64d0e778ecc6b6b239afa` and accompanying changes.

[FACT] Native chat and follow classify only explicit Markdown file destinations.
Chat pins the selected space's controller-owned default workdir as base/root.
Follow resolves from the source file's directory: its root is the pinned workdir
when that contains the source, otherwise the source directory. Explicit host
follow uses launch cwd by the same rule. Chained navigation retains that original
root and mode; it never switches between host and VM. Unavailable chat settings
disable only local navigation. [1](#source-1) [2](#source-2)

[FACT] Relative paths are URL-decoded once, normalized and confined to that root.
Schemes (including file:), authorities, absolute/home paths, queries, controls
and backslashes are refused. Original control-bearing destinations are denied
before display sanitization can turn them into a different path. [9](#source-9) File-plus-fragment opens the file without heading
navigation; fragment-only destinations stay inert. Bare prose/code never become
local file actions. [1](#source-1)

[FACT] Admission batches at most 64 coalesced destinations from visible rows off
the input loop; later visible batches continue after refusals. Readability is an observation: each explicit open rechecks it,
and linked follow reads retain the boundary. VM metadata/read operations recheck
running-space grants and use descriptor-based no-follow walks; linked host reads
also refuse symlink components and nonregular leaves (the macOS system /tmp alias
is normalized). Ordinary manually selected host follow retains its existing
symlink semantics. No VM boot, grant change, shell/opener command or automatic
mutation retry is involved. Stale document/channel/closed-view outcomes do not
activate a target. [2](#source-2) [3](#source-3) [4](#source-4)

[FACT] The pure renderer receives admission metadata, never reader authority.
Clickable explicit links display the label once in prose and tables; action
metadata retains the destination. Inert destinations retain their target text;
bare HTTP URLs and source mode retain their visible bytes. Copy uses displayed
text; whole-link Quote retains original Markdown through a replacement source
map and matching re-render. Drag release copies; a completed unchanged click
activates. [5](#source-5) [6](#source-6) [8](#source-8)

[FACT] Trusted native panes register one immutable navigation context over their
private tab channel. The TUI binds it to launch mode/space/explicit source and
root, rejects changed boundaries and stale source/space generations, and accepts
only typed follow targets. A local file opens a NEW tab, preserving the source
and any empty attach placeholder; an identical live mode/space/path/root is
reused. The absolute source, original navigation root and actual reader mode are pinned
independently in reopen arguments. Ordinary manual readers are never reused as
bounded linked readers.
Standalone local-file opening gives the TUI remedy; it never substitutes an OS
application. Guarded participant attaches keep their existing route.
[2](#source-2) [7](#source-7)

[FACT] HTTP(S) activation in follow (host/VM/standalone) uses the same host URL
validation and seams.browser callback as chat. On opens explicitly; ask snapshots
the URL and requires deliberate Ctrl+O with a visible destination; off never
calls the opener. Esc cancels. Paste, committed text and repeat events cannot
approve, and browser policy does not govern local file opening. Follow lays a
view out again for links only when the visible-destination probe admits a new
target; that relayout reuses the current Mermaid drawing outcome. Clipboard
behavior is unchanged. [2](#source-2) [4](#source-4) [6](#source-6)

[FACT] The bare URL scanner searches for the first ASCII space or TAB in
the original line suffix and uses the line end on a miss. Trailing punctuation
is trimmed from the intent without moving the scan boundary. [10](#source-10)

## Sources

1. <a id="source-1"></a> [Navigation context](../../internal/filelink/navigation.go#L20) — Context, Resolve, Accept, AdmitDestinations.
2. <a id="source-2"></a> [CLI binding](../../cmd/yoloctl/file_navigation.go#L22) — fileNavigation; follow.go newFollowCmd and chat_view.go runChatView/configureChatBrowser.
3. <a id="source-3"></a> [Linked readers](../../internal/followview/navigation.go#L17) — openBounded, HostProbe, VMReader.Probe; source.go HostReader.Read/VMReader.call and reader.py open_selected/probe.
4. <a id="source-4"></a> [Follow actions](../../internal/followview/links.go#L16) — activateLink, browserKey, browserView; model.go work/Update/mouse/View.
5. <a id="source-5"></a> [Pure link mapping](../../internal/markdownview/inline.go#L38) — InlineMarkdownMap; document.go ParseOptions.Links and body.go parseLines/TableLines; the adapter is described in source 8.
6. <a id="source-6"></a> [Chat admission](../../internal/chatui/file_links.go#L58) — linkPolicy, settleFileLinks, receiveFileLinks, checkedFile; quote.go quoteSource/rendered and mouse.go mouseUp.
7. <a id="source-7"></a> [Typed parent routing](../../internal/tui/tab_requests.go#L108) — handleFollowRequest, pinFollowSource, navigationForSource; termio/tabrequest.go strict request decoding and follow context/target validation.

8. <a id="source-8"></a> [Admission adapter](../../internal/linkview/policy.go#L10) — Policy adapts already-admitted targets without coupling transport/path types to Markdown or TUI.

9. <a id="source-9"></a> [Raw destination analysis](../../internal/markdownview/unsafe_links.go#L15) — UnsafeDestinations reuses the raw block/inline grammar and retains denial before Plain sanitization; linkview.Policy applies it to original native-view source.

10. <a id="source-10"></a> [Bare URL scan](../../internal/markdownview/document.go#L108) — `URLAt` and `TestBareURLBoundaries`, verified at `4624036` and accompanying changes.
