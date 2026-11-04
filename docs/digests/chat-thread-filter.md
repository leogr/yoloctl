# Native chat thread view

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes. Shared-source anchors checked at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] A selected message's Thread action (confirmed messages only, beside
Copy reference, without a shortcut) and `/thread` show one thread in the
visible channel view. `/thread` takes no arguments and acts on the selected
message, else on the latest shown one, like the other actions' implicit
target. Choosing walks `ReplyTo` up through loaded messages with exactly that
server ID (the first loaded with it) to the top-most loaded message and keeps
that top's ID, so any message of a thread chooses the same thread. A reply
label click still only shows its parent. [1](#source-1) [2](#source-2)

[FACT] The thread is a mode of the existing filter: `messageFilter.thread`
replaces the account picks. The same one pass decides every loaded message:
the current top is the chosen top or the top-most loaded ancestor an older
page has since loaded, and a message is shown when its reply chain reaches
that top, all branches, in time order. A chain whose loaded part ends in a
reply to the chosen top, evicted since, is still shown. A self-reply or a
cycle ends the walk; an unknown parent ID is a thread of its loaded part.
Nothing is fetched. Pending, refused and uncertain sends stay, as in every
filter. Arrivals outside the thread count as `+N`; replies into it show.
[1](#source-1) [3](#source-3)

[FACT] One filter at a time: Thread replaces For me or people, and Alt+M, a
card, `/filter …` or the picker's people replace it; `✕`, All messages and
`/filter all` clear it. Esc does not clear any filter. The divider chip reads
`Thread` (click opens the picker) with `+N` and `✕`. While a thread is shown
the picker adds a checked Thread row after All messages; checking a person
unchecks it, and Enter on it keeps the thread. The scope row names the
current top's author and time, or `the loaded part of a thread` when it is
evicted, and states `from the start of the thread` when the top replies to
nothing, else `the thread may start earlier: Page Up loads older history`
unless loading, a full display history or retained-history state applies.
[1](#source-1) [4](#source-4) [5](#source-5)

[FACT] Help (Reading and Commands), the suggestion list and the picker
footer describe Thread and `/thread`. [5](#source-5) [6](#source-6)

[FACT] Fixture regressions cover the whole tree from a leaf, the root and
either branch; the top moving up after one explicit Page Up with an older
branch; an evicted top; cycles, self-replies and unknown IDs; replacing and
being replaced by For me, people and the picker; live arrivals and channel
isolation; `/thread` targets, refusal and unconfirmed messages; and wording.
They use fake callbacks, not a live room. [7](#source-7)

## Sources

1. <a id="source-1"></a> [Thread filter](../../internal/chatui/filter.go#L13-L303) — `messageFilter.thread`, `matches`, `shown`, `parents`, `rootOf`, `threadTop`, `threadMayStartEarlier`.
2. <a id="source-2"></a> [Action and command](../../internal/chatui/model.go#L1004-L1323) — `messageActions`, `runAction` (`thread`), `messageName`, `slashCommand` (`/thread`).
3. <a id="source-3"></a> [Arrivals](../../internal/chatui/model.go#L1380-L1446) — `incoming` counts `hiddenNew` through `arrivalShown`.
4. <a id="source-4"></a> [Setting and surfaces](../../internal/chatui/filter.go#L392-L767) — `setThread`, `applyFilter`, `threadName`, `filterScopeRows`, `filterChip`, `filterChoices`, `toggleFilterChoice`, `applyFilterChoices`, `filterPickerRows`; `hoverHint` in hover.go.
5. <a id="source-5"></a> [Help and picker state](../../internal/chatui/overlay.go#L41-L131) — `overlayState.stagedThread`, `helpSections`; `overlayActivate` applies the picker.
6. <a id="source-6"></a> [Suggestions](../../internal/chatui/suggest.go#L31-L43) — `commands`.
7. <a id="source-7"></a> [Thread regressions](../../internal/chatui/thread_filter_test.go#L1-L342) — eight tests, `TestThreadViewShowsTheWholeTreeFromAnyMessage` through `TestThreadViewWording`.
