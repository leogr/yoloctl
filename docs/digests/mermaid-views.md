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
client and closes it on exit. Chat does not draw diagrams yet. [7](#source-7)

## Sources

1. <a id="source-1"></a> [Parse options](../../internal/markdownview/document.go#L33) — `ParseOptions.Diagrams`, `Diagram`, `DiagramWidth`, `IsMermaid`.
2. <a id="source-2"></a> [Drawing rows](../../internal/markdownview/body.go#L55) — `drawDiagram`, the fence-close branch and note row in `parseLines`.
3. <a id="source-3"></a> [Quote mapping](../../internal/markdownview/quote.go#L183) — `LineSource.Diagram`, `LineStart`, `LineEnd`.
4. <a id="source-4"></a> [Follow drawing](../../internal/followview/mermaid.go#L51) — `drawBudget`, `maxDiagrams`, `drawing.draw`; `renderFile`, `rendered.sourceLine`/`displayLine`, `captureAnchor`/`placeAnchor`, `work`, the continuation in `Model.Update` and `sameThrough` (model.go).
5. <a id="source-5"></a> [Hook regressions](../../internal/markdownview/diagram_test.go#L1) — drawing rows and width, fallback note, lookup scope, whole/partial Quote.
6. <a id="source-6"></a> [Follow regressions](../../internal/followview/mermaid_test.go#L1) — occurrence limit and dedupe, total budget, continuation progress, no-progress stop and `r`, transient retry, anchor across replacement, no renderer, selection clearing.
7. <a id="source-7"></a> [Preference](../../internal/config/tui.go#L75) — `TUIMarkdown`, `MermaidEnabled`, template; `newFollowCmd` in cmd/yoloctl/follow.go; `TestHostFollowNeverResolvesASpaceOrVM` in follow_test.go.
