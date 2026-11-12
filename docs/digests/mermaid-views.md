# Mermaid drawings in Markdown views

Verification: `b4a1c67f731092a5714834932418178d5483f31a` and accompanying changes.

[FACT] `markdownview.Parse` takes an optional non-blocking `Diagrams` lookup,
called (in rendered mode only) with the source of each closed fence whose info
string is exactly `mermaid`, case-insensitive. A drawing replaces that fence's
code rows with its rows: card-styled Code lines tagged with the fence number,
never highlighted or re-wrapped when requested at `DiagramWidth` (the layout
width minus both card paddings). The opener keeps the fence source in `Block`
for its copy control. A note (a fallback reason or `drawing…`) keeps the code
rows and adds one dim italic Decor row after the closer, which is never copied.
Source mode, other fences and unclosed fences never call the lookup.
[1](#source-1) [2](#source-2) [5](#source-5)

[FACT] Drag copy takes the drawing's text. With source maps, every drawing row
maps to the whole fence content as one unit: a Quote selection that covers the
whole drawing (alone or with prose around it) is exact and quotes the fence
source; a selection starting or ending inside the drawing is inexact.
[3](#source-3) [5](#source-5)

[FACT] `follow` draws in its background render job. A parse collects the
closed mermaid fences; only a document that has some is drawn and parsed again
with source maps, each fence occurrence getting its own outcome in order: a
refused one `mermaid: not drawn (<admission reason>)`, admitted ones past the
20th `too many diagrams` (repeated sources count too), the others a drawing at
the card's inner width or the renderer's reason. Each distinct source renders
once, within the refresh's 5 s total: a render gets only the time left, and one
cut by it is not attempted. Those show `mermaid: drawing…`, and the model
schedules a render-only continuation on the retained bytes. Continuations
repeat while each leaves fewer undrawn; one that does not stops the chain and
labels them `not drawn (refresh budget; r retries)`. Changed content or another
render request starts afresh; `r` also renders when fences are undrawn or fell
back for a transient reason (busy, unavailable, internal). With drawings, a
display-to-source line map keeps the paused reading position (a source byte) on
its line; one inside a fence lands on its drawing's top. A new render whose
lines differ at or above the selection's last line clears it.
[4](#source-4) [6](#source-6)

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
row its row. A highlight in a rebuilt body clears. One request runs at a time
(`Render` off the UI goroutine), for the first pending fence on the screen at
the body's card width; a result for an old width is not shown. Quote replays a
body's outcomes, so a whole drawing quotes its fence. [9](#source-9) [10](#source-10)


## Sources

1. <a id="source-1"></a> [Parse options](../../internal/markdownview/document.go#L33) — `ParseOptions.Diagrams`, `Diagram`, `DiagramWidth`, `IsMermaid`.
2. <a id="source-2"></a> [Drawing rows](../../internal/markdownview/body.go#L55) — `drawDiagram`, the fence-close branch and note row in `parseLines`.
3. <a id="source-3"></a> [Quote mapping](../../internal/markdownview/quote.go#L183) — `LineSource.Diagram`, `LineStart`, `LineEnd`.
4. <a id="source-4"></a> [Follow drawing](../../internal/followview/mermaid.go#L51) — `drawBudget`, `maxDiagrams`, `drawing.draw`; `renderFile`, `rendered.sourceLine`/`displayLine`, `captureAnchor`/`placeAnchor`, `work`, the continuation in `Model.Update` and `sameThrough` (model.go).
5. <a id="source-5"></a> [Hook regressions](../../internal/markdownview/diagram_test.go#L1) — drawing rows and width, fallback note, lookup scope, whole/partial Quote; `FuzzDiagramSourceMap`.
6. <a id="source-6"></a> [Follow regressions](../../internal/followview/mermaid_test.go#L1) — occurrence limit and dedupe, total budget, continuation progress, no-progress stop and `r`, transient retry, anchor across replacement, no renderer, selection clearing.
7. <a id="source-7"></a> [Preference](../../internal/config/tui.go#L75) — `TUIMarkdown`, `MermaidEnabled`, template; `newFollowCmd` in cmd/yoloctl/follow.go; `TestHostFollowNeverResolvesASpaceOrVM` in follow_test.go.
8. <a id="source-8"></a> [Chat wiring](../../cmd/yoloctl/chat_view.go#L1) — `configureChatDiagrams`; `TestChatDiagramsFollowPreferenceAndHost` in chat_browser_test.go; `Client.Lookup`, `OnHost` in internal/mermaid/client.go.
9. <a id="source-9"></a> [Chat drawing](../../internal/chatui/mermaid.go#L1) — `diagramFor`, `parseDiagrams`, `fenceLines`, `settleDiagrams`, `visibleFences`, `rebuildDiagramBodies`, `holdReadingPosition`, `carryLine`, `requestDiagram`, `diagramResult`; `body` in transcript.go, `buildTranscript` in view.go, Quote lookups in quote.go.
10. <a id="source-10"></a> [Chat regressions](../../internal/chatui/mermaid_test.go#L1) — drawing and Quote, twenty occurrences with repeats, scrolling across the boundary and inside a message, partly visible fence, tall drawings settle, resize while rendering, transient answer and height-only retry, reading position across drawings, no renderer; `FuzzCarryLine`.
