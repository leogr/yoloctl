# Native chat hover and collapsed pastes

Verification: `d9aec4ac854ffd5b4f60a93817b8c5afce10847d` and accompanying changes.

[FACT] The view requests all-motion mouse reporting so a buttonless pointer
can highlight click targets. Hover is an identity, not a cell: a frame target (area, item, id) or
a transcript action (message key plus link, author, mention or reply). It is
recomputed on motion and wheel scrolling, and dropped on resize or press. A drag, the link prompt, or an open overlay (for anything
but its own rows and buttons) suppresses it. Terminals report no pointer leave,
so a hover lasts until the next motion, as in space view. [1](#source-1)
[2](#source-2)

[FACT] Styling follows space view without importing a helper: a hovered text
target is bold and underlined in its own colors, padding excluded, and a
wrapped link is underlined in every row of its message. Rows (channel rail,
participants, suggestions) underline their name; overlay rows and buttons
underline too. Styling is applied while painting. When no message actions,
notice, suggestion or highlight is showing, the footer names a target drawn in
the current frame and what a click does (attach or details, link policy,
expand, jump); a used jump is no longer named. Motion never activates, copies
or selects. A hovered participant whose detail line is clipped scrolls that
line on its own tick chain, or with NoMotion the footer names it in full; see
[participant model and effort](chat-participant-runtime.md). [1](#source-1)
[3](#source-3) [10](#source-10)

[FACT] An overlay's own click target sits beneath its rows and buttons, which
win because later hits take precedence; Settings rows map to the row that
shows each setting, below the selected row's wrapped hint. [4](#source-4)

[FACT] One bracketed paste of at least 6 lines or 1 KiB collapses into a chip
such as `[Pasted #1 · 7 lines · 35 B]`. The text stays in the draft: a fold
only records its byte range, so the payload, byte count, line check, limits and
receipt matching are those of the full text, and oversized pastes are still
refused atomically. Folds are part of undo snapshots. Cursor stops are grapheme
boundaries outside folds plus both fold ends; movement, Backspace, Delete and
kills treat a fold as one unit, and an edit overlapping it removes it whole.
Home/End ignore newlines inside a fold, mention suggestions and Tab completion
stop at its edges, and yanking killed text inserts plain text. [5](#source-5) [6](#source-6)

[FACT] The composer draws a fold as one unbreakable chip that moves to the
next row and shortens its label to fit. Alt+P beside a chip or a click on it
expands it in place without changing text or revision, so a pending send still
clears on confirmation; Ctrl+Z collapses it again. A draft containing a fold is
always a message, never a slash command. From 16 rows a blank composer row
sits above the draft, and one always follows its last visible row; both are
padding, never draft text, and a click there only focuses the composer. A
notice beside message actions takes a row of its own; at 20×7 while replying,
the transcript yields its last row rather than overflow. The first row's
prompt is `❯`, one cell. [6](#source-6) [7](#source-7) [9](#source-9)

[FACT] Ctrl+V images are inline `[Image #n]` folds in the same draft; see
[composer image drafts](chat-composer-images.md).

[FACT] Regressions cover the attached operator mark, glyph slots, hover
styling, hints, modal capture, resize and drag, overlay clicks, Settings row
mapping, chip rendering and labels, cursor stops, undo, expansion, the exact
sent payload, command suppression, combining marks and composer padding. They
use fake callbacks, not a graphical terminal; real hover delivery in nested and
standalone views needs a host check. [8](#source-8) [9](#source-9)

## Sources

1. <a id="source-1"></a> [Hover state and styling](../../internal/chatui/hover.go#L13-L228) — `hoverKey`, `hoverAt`, `activeHover`, `hoverRuns`, `hoverRow`, `hoverHint`.
2. <a id="source-2"></a> [Mouse routing](../../internal/chatui/mouse.go#L10-L140) — `mouse`, `mouseDown`, `mouseMove`; `View` sets `MouseModeAllMotion` (view.go).
3. <a id="source-3"></a> [Painted targets](../../internal/chatui/view.go#L141-L742) — transcript rows, `topBar`, `composerDivider`, `replyRow`, `inputRow`, `footer`.
4. <a id="source-4"></a> [Overlay hits](../../internal/chatui/overlay.go#L406-L543) — `overlayLines`, `settingsView`.
5. <a id="source-5"></a> [Folds in the editor](../../internal/chatui/editor.go#L23-L452) — `fold`, `stops`, `widen`, `shifted`, `replace`, `lineStart`, `word`, `wordStart`, `kill`, `restore`, `expandFold`, `foldPaste`.
6. <a id="source-6"></a> [Composer layout](../../internal/chatui/render.go#L15) — `draftRow`, `stopAt`, `chipLabel`, `fitLabel`, `draftLayout`.
7. <a id="source-7"></a> [Paste, keys and commands](../../internal/chatui/model.go#L367) — `tea.PasteMsg`, `alt+p`, `slashCommand`; `hitFold` in mouse.go `clickHit`; padding in view.go `View`.
8. <a id="source-8"></a> [Checkpoint regressions](../../internal/chatui/checkpoint_ui_test.go#L45) — mark, glyphs, hover, overlays, pastes and joins.
9. <a id="source-9"></a> [Composer regressions](../../internal/chatui/composer_test.go#L139) — `TestComposerHasPaddingAboveThePrompt`; `TestComposerKeepsOneBlankRowBelowTheDraft` in ui_package_test.go.
10. <a id="source-10"></a> [Participant line scrolling](../../internal/chatui/marquee.go#L146-L202) — `roleText`, `settleMarquee`, `marqueeAdvance`; the NoMotion hint in hover.go `hoverHint` L192.
