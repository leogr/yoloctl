# Native chat message filter and mention identities

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes. Nickname labels and suggestion selection verified at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes. Shared-source anchors checked at `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes.

[FACT] One resolver names who an `@token` means, case-folded, with the
existing mention boundaries (an `@` not after a nickname byte, then nickname
bytes). The trusted catalog binds account IDs, names and saved nicks
(`Identity.Nick`, from the room header). Nicks seen with an authenticated
account, in any channel's roster or loaded messages, bind only tokens the
catalog does not, so an unverified session cannot take a trusted name. A
token two accounts claim names nobody; a nick seen only on sessions without
an account is unbound. The host supplies its saved nickname as
`Options.Nick`. The component treats that supplied value as observed
evidence, subject to the same catalog precedence and conflict rules; live
aliases are learned from rosters and loaded messages. `@all`, `@here` and
`@N` name no one. [1](#source-1) [2](#source-2)
[3](#source-3)

[FACT] Suggestions, Tab completion and the message and card Mention actions
insert a trusted account's saved nick, one row per account however many
connections it has. A canonical name prefix still finds a trusted account,
but suggestion and filter-picker details omit that name. An unverified session keeps
its actual nick with no kind or qualifier detail (only `away`), here and in the
filter picker, and a trusted row replaces an unverified one with the same
`@name`; catalog and session refreshes rename an open suggestion list,
keeping the selected account or command when still listed, including after a
rename. Otherwise the old selection index is clamped to the remaining list.
A click on a mention opens the bound
account's card (offline trusted accounts too). A conflicting token only
explains itself; an unbound one keeps the exact connected-nick match. Self
highlighting, the "for you" mark and unread mention counts use the same
tokens, so a mention of the saved nick or of any of the human's live aliases
seen there counts for every view. Settings and `/nick` show, pre-fill and
compare the saved nick, never a stripped alias.
[3](#source-3) [4](#source-4) [5](#source-5) [6](#source-6) [7](#source-7)

[FACT] A channel view may show only messages any selected authenticated
account sent (a local pending send is mine) or that mention one through the
resolver, and replies to those; no selection shows all. A thread view
replaces any selection (see the [thread digest](chat-thread-filter.md)). Each channel keeps its own. The divider chip
follows the channel name: `For me` (click or Alt+M switches exactly For me
with all; another selection becomes For me), or the name or `N people` (click
opens the picker), then hidden arrivals `+N` and a separate clearing `✕`. It
uses only cells the centred jump and count leave, shortening, then hiding.
Alt+F, `/filter` or that click opens a picker on a copy of the selection (All
messages, a shown thread, For me, connected accounts once, selected departed accounts): Space
or click checks, Enter applies, Esc or Alt+F cancels; text and pastes never
act, and a repeated Alt+M or Alt+F does nothing. `/filter me|all` stand
alone; `/filter NAME …` resolves every name first and refuses wholly on an
unknown or conflicting one. Cards offer Filter messages. The band gains no
button.
[1](#source-1) [6](#source-6) [8](#source-8) [9](#source-9) [15](#source-15)

[FACT] One pass per rebuild decides every loaded message: a reply is shown
when the message with exactly its `ReplyTo` server ID, the first loaded in
this channel's view, is shown, to any depth. Sibling branches and the root
of a thread stay hidden unless they match themselves. `ReplyTo` is
participant-supplied: an unloaded, evicted or other-channel parent shows
nothing until loaded, nothing is fetched, and self-replies, cycles and
unknown IDs terminate unshown. All filter call sites share the pass; a live
reply it shows is not a hidden arrival, and self marks and unread mentions
ignore it. [1](#source-1) [10](#source-10) [13](#source-13)

[FACT] Filtering hides transcript rows only. Messages, IDs, receipts, drafts,
the reply, unread counts and the history cursor stay. Sends that are pending,
refused or uncertain stay shown, as do gap, own-membership and guidance
notices; roster and topic notices hide. Scope rows head the transcript:
`N of M loaded messages`, the history state (Page Up loads older, beginning
of retained history, or not shown) with any gap count, and an `✕ All
messages` target. Page Up at the top requests the page before the oldest
underlying message, matched or not; a page never starts another.
[1](#source-1) [10](#source-10) [11](#source-11)

[FACT] A change keeps the reading anchor on the nearest shown message, clears
a drag highlight and ends a pick it hides. After any roster, message,
delivery, catalog or connection event, changed bindings rebuild every
channel's rows, so evidence from a hidden channel updates the visible filter
and self marks on the next frame; a filtered view's pick that is no longer
shown ends rather than moving to a neighbour. Ctrl+R and the other actions'
implicit latest target, Alt+↑/↓ and a drag copy skip hidden messages. A reply
label or reference click on a loaded hidden message clears the filter, then
selects that ID. [1](#source-1) [5](#source-5) [10](#source-10) [12](#source-12)

[FACT] Focused fixture regressions cover sender or mention matching and its
boundaries, aliases, conflicts and untrusted collisions, channel isolation,
empty-filter paging, arrivals, targets and copy, send outcomes and drafts,
hidden references, divider placement and clicks, Alt+M/Alt+F, the
multi-person picker, command and card, replies under shown messages
(depth, siblings, mention roots, unloaded parents, channels, forged chains,
arrivals and wording),
canonical suggestions, self marks, the Settings nickname, evidence from a
hidden channel, picks the filter comes to hide and a supplied nickname used
earlier by another account. They use fake callbacks, not a live room. A
synthetic component probe against the base API (fake callbacks; unlike the
host, it supplied an alias as `Options.Nick`) showed the base API suggesting
both aliases and not counting a mention of the human's other alias. [13](#source-13) [14](#source-14)

## Sources

1. <a id="source-1"></a> [Filter and resolver](../../internal/chatui/filter.go#L13-L767) — `messageFilter`, `nameBindings`, `bindings`, `resolve`, `selfNames`, `mentionToken`, `isTransport`, `matches`, `shown`, `reconcile`, `arrivalShown`, `setFilter`, `shownAnchor`, `filterScopeRows`, `filterChip`, `filterCommand`, `filterChoices`.
2. <a id="source-2"></a> [Mention tokens](../../internal/chatui/body.go#L385-L422) — `eachMention`, `mentions`, `selfMention`; `decorateTokens` marks self.
3. <a id="source-3"></a> [Host identities](../../cmd/yoloctl/chat_view.go#L28-L156) — `runChatView` supplies `Options.Nick` from `openChatHeader`'s saved nick; `chatIdentities` carries the saved `Nick`; `Identity.Nick` in chatui/model.go.
4. <a id="source-4"></a> [Suggestions](../../internal/chatui/suggest.go#L61-L242) — `refreshSuggest`, `refreshOpenSuggest`, `accountAway`, `joinDetail`; `complete` and mention actions in model.go. `TestMentionsUseTheSavedNickPerAccount` (filter_test.go L440–487) checks name-prefix matching without displaying the name; nickname_display_test.go `TestIdentityRefreshKeepsSuggestionSelection`, `TestCatalogRefreshTracksSelectedAccountWhenRowsMove` and `TestCatalogRefreshClampsMissingSuggestion` cover selection retention and fallback.
5. <a id="source-5"></a> [Mouse actions](../../internal/chatui/mouse.go#L205-L350) — `clickHit` (`hitFilter`), `activate` (`actFilter`), `showMessage`, `mentionCard`.
6. <a id="source-6"></a> [Cards and picker](../../internal/chatui/overlay.go#L386-L451) — `cardActions`, `cardAction`; `overlayFilter` in `overlayActivate` and `overlayLines`; `cardRows` shows the saved nick and a connection alias; `savedNick` serves Settings and `saveNick`.
7. <a id="source-7"></a> [Transcript marks](../../internal/chatui/transcript.go#L121-L237) — `body` keyed by `selfNames`, `transcript`; unread mentions in model.go `incoming`.
8. <a id="source-8"></a> [Divider](../../internal/chatui/view.go#L406-L501) — `composerDivider`, `withChip`.
9. <a id="source-9"></a> [Channel state](../../internal/chatui/channels.go#L58-L60) — `channelView.filter`.
10. <a id="source-10"></a> [Model paths](../../internal/chatui/model.go#L567-L1629) — `historyPage`, `slashCommand`, `incoming`, `selectedMessage`, `selectMessage`.
11. <a id="source-11"></a> [Notices](../../internal/chatui/presence.go#L187-L200) — `presenceRows` keeps transport notices.
12. <a id="source-12"></a> [Selection](../../internal/chatui/selection.go#L69-L124) — `selectedText` skips hidden messages.
13. <a id="source-13"></a> [Filter regressions](../../internal/chatui/filter_test.go#L87-L844) — twenty-three focused tests, `TestForMeShowsRepliesUnderMyMessages` through `TestFilterWordingNamesReplies` for replies; `TestOverlayControlsReceiveClicksAndHover` and `TestChatHeaderSharesDashboardStyle` keep card and band behavior.
14. <a id="source-14"></a> [Host identity regression](../../cmd/yoloctl/chat_view_test.go#L34-L49) — `TestChatIdentitiesComeFromTheTrustedMap` asserts the saved nick.
15. <a id="source-15"></a> [Selection regressions](../../internal/chatui/filter_selection_test.go#L1-L210) — chip placement, Alt+M, Alt+F picker, several people's rules including a reply.
