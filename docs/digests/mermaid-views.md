# Mermaid drawings in Markdown views

Verification: `b4a1c67f731092a5714834932418178d5483f31a` and accompanying changes. Independent publication and diagram scheduling verified on `032a18bd0874673d7fb61c33f3e6bef2b0d15c19` and accompanying changes.

[FACT] `markdownview.Parse` takes an optional non-blocking `Diagrams` lookup,
called (in rendered mode only) with the source of each closed fence whose info
string is exactly `mermaid`, case-insensitive. A drawing replaces that fence's
code rows with its rows: card-styled Code lines tagged with the fence number,
never highlighted or re-wrapped when requested at `DiagramWidth` (the layout
width minus both card paddings). The opener keeps the fence source in `Block`
for its copy control. A note adds one dim italic Decor row after the closer and is omitted from
copy. A pending diagram can suppress the code rows while keeping that note
and the opener’s original-source copy control.
Source mode, other fences and unclosed fences never call the lookup.
[1](#source-1) [2](#source-2) [5](#source-5)

[FACT] Drag copy takes the drawing's text. With source maps, every drawing row
maps to the whole fence content as one unit: a Quote selection that covers the
whole drawing (alone or with prose around it) is exact and quotes the fence
source; a selection starting or ending inside the drawing is inexact.
[3](#source-3) [5](#source-5)

[FACT] Follow publishes Markdown with cached drawings independently of
worker replies. It admits fences in source order, counts repeated occurrences
toward the first 20, and gives later occurrences a reason note. Eligible
cache misses first show a pending card. At the original five-second publish
point, the source rows appear with `mermaid: drawing…`, even for queued
fences. A late placeholder layout or unchanged read keeps an overdue wake
pending until the installed view exposes the source. A still-wanted source,
card width and width method keep the same
attempt and deadline across file updates. The valid drawing or final reason
arrives as a view event. Link checks keep their own publish path and
unchecked links stay inert. A display-to-source map keeps a paused reading
position across replacements. A changed row at or above the selection's last
line clears it. A current transient reason can be retried with `r`.
[4](#source-4) [6](#source-6) [11](#source-11)

[FACT] `markdown.mermaid` in `tui.yaml` defaults on; `false` gives `follow` no
renderer, so fences stay code cards with no note. `follow` uses the process
client and closes it on exit. Chat gets the renderer only when the preference
is on and the process is on the host (`mermaid.OnHost`), so a box or VM keeps
code cards with no note. [7](#source-7) [8](#source-8)

[FACT] Chat parses on the UI goroutine and only looks drawings up
(`Client.Lookup`). After every update, `settleDiagrams` lays out the current
channel and counts the admitted fence occurrences whose code or drawing rows
are visible, top to bottom, repeats included: the first 20 may draw, the
visible ones past them show `mermaid: not drawn (too many diagrams)`, cached or
not. A new screen (width, height, follow, a user scroll or message selection,
a new message while following, filter) recounts from that one layout and
clears transient answers; layout changes from drawings never make a new
screen, and within one the counts only grow, so they settle. Each body records
its fences' class, pending and transient state; one built for another class,
waiting for a drawing now cached, or showing an earlier screen's transient
answer is rebuilt. Before that a paused view anchors its top row, and the
rebuild carries a reading position over: a line outside the fences keeps its
place among them, a fence's opener, closer or note stays that line, a content
row its row. A highlight in a rebuilt body clears. Pending chat fences show their source immediately. Quote replays a
body's outcomes, so a whole drawing quotes its fence. Image chips and the literal table fallback both use the same drawing-aware
parse and fence map. [9](#source-9) [10](#source-10)


[FACT] Both views pick the shortest source among their eligible queued fences,
with the original order breaking ties. An active render keeps the single
client slot while it is still wanted. Superseded widths, removed fences and
view closure cancel the work. A stale reply leaves the current attempt
alone. Transient results advance the queue and stay visible until an explicit
follow refresh or a new eligible chat screen. Smaller fences arriving after
a current slow one begins can still wait for it. [4](#source-4) [9](#source-9)


## Sources

1. <a id="source-1"></a> [Parse options](../../internal/markdownview/document.go#L33) — `ParseOptions.Diagrams`, `Diagram`, `DiagramWidth`, `IsMermaid`.
2. <a id="source-2"></a> [Drawing rows](../../internal/markdownview/body.go#L55) — `drawDiagram`, the fence-close branch and note row in `parseLines`.
3. <a id="source-3"></a> [Quote mapping](../../internal/markdownview/quote.go#L183) — `LineSource.Diagram`, `LineStart`, `LineEnd`.
4. <a id="source-4"></a> [Follow drawing](../../internal/followview/mermaid.go#L21) — `drawBudget`, eligibility, attempt lifetimes, fallback timer and queue.
5. <a id="source-5"></a> [Hook regressions](../../internal/markdownview/diagram_test.go#L1) — drawing rows and width, fallback note, lookup scope, whole/partial Quote; `FuzzDiagramSourceMap`.
6. <a id="source-6"></a> [Follow regressions](../../internal/followview/mermaid_test.go#L1) — occurrence limit, source and draw timing across appends, late fallback delivery, unchanged poll before a wake, stale cancellation, transient retry, anchor and selection.
7. <a id="source-7"></a> [Preference](../../internal/config/tui.go#L75) — `TUIMarkdown`, `MermaidEnabled`, template; `newFollowCmd` in cmd/yoloctl/follow.go; `TestHostFollowNeverResolvesASpaceOrVM` in follow_test.go.
8. <a id="source-8"></a> [Chat wiring](../../cmd/yoloctl/chat_view.go#L1) — `configureChatDiagrams`; `TestChatDiagramsFollowPreferenceAndHost` in chat_browser_test.go; `Client.Lookup`, `OnHost` in internal/mermaid/client.go.
9. <a id="source-9"></a> [Chat drawing](../../internal/chatui/mermaid.go#L1) — `diagramFor`, `parseDiagrams`, `fenceLines`, `settleDiagrams`, `visibleFences`, `rebuildDiagramBodies`, `holdReadingPosition`, `carryLine`, `requestDiagram`, `diagramResult`; `body` in transcript.go, `buildTranscript` in view.go, Quote lookups in quote.go.
10. <a id="source-10"></a> [Chat regressions](../../internal/chatui/mermaid_test.go#L1) — drawing and Quote, twenty occurrences with repeats, scrolling across the boundary and inside a message, partly visible fence, tall drawings settle, resize while rendering, transient answer and height-only retry, reading position across drawings, no renderer; `FuzzCarryLine`.
11. <a id="source-11"></a> [Follow publication](../../internal/followview/model.go#L312) — `work`, link admissions and result updates.
