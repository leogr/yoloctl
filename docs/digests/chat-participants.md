# Native chat participants, settings and attachment

Verification: `704a65d4dde24e4b67df90907d7725c8cfe1a86d` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Participants sort operators, voiced, then others, case-folded; observed
`@`/`+` modes, never inferred, precede names. The panel opens from 80 columns
(22 wide, 28 from 100), keeping 48 transcript columns; narrower, Ctrl+P overlays
it. Each participant is a two-row card (glyph tile, mark, name and presence dot,
then an identity line: model and effort for agent boxes) whatever the room size;
a closing note shows only with room. When the cards do not fit, the panel
scrolls by whole cards below its fixed heading, with dim `▲ N above` / `▼ N more`
rows: the wheel over the panel moves one card, Alt+PgUp/PgDn (or Shift+PgUp/PgDn)
a page (the cards shown while more are below, so pages never skip one), and
nothing scrolls while every card fits; a panel left only its heading shows no card.
[1](#source-1) [16](#source-16)

[FACT] Roster changes (joins, departures, same-account nicks, operator/voice,
live topic) become display-only notice rows after the latest message and a blank
row; bursts within 10 s fold, the first snapshot after connecting is no change,
and 200 are kept. Another connection of an account that stays connected (a
second chat view) is neither a join nor a departure; the panel still lists each
connection. Notices follow the prior message only for scroll position: no
selection or mention mark, and clicks select nothing. [2](#source-2)
[8](#source-8)

[FACT] `/help` and `/settings` open Help and Settings; Ctrl+P or `/participants`
toggles Participants (no F1/F2; a host `palette_key: ctrl+p` takes that chord).
Settings shows the saved nickname from the identity map, signed-in account
(read-only), panel, view and
copy-on-select toggles, and read-only `seams.clipboard`/`seams.browser` naming
their config. Nickname saves are refused during an unresolved send or for the saved name; after
`SaveNick` succeeds the view requests one explicit reconnect, keeping draft,
reply and position, resending nothing. Reconnecting, the host ends `chat.stream`
and waits, runs `chat.ensure` for the new header, sends the new nick and
identities, then streams again. After reconnect or a catalog refresh, current
author, panel, open card and suggestion labels use the new saved nickname;
the selected suggestion follows its account or command while listed, otherwise
its index is clamped. Retained presence notices keep their event-time text. [15](#source-15)
Bounded serial `box.inventory` reads add agent
glyphs and [model and effort](chat-participant-runtime.md). [4](#source-4)
[3](#source-3) [9](#source-9) [10](#source-10) [5](#source-5)

[FACT] Clicking a participant or author resolves by authenticated account only;
humans, unverified sessions and boxless members get an explanatory card. One
lookup runs at a time and stale answers open nothing; the host bounds
`chat.attach-target` ([lock-free](chat-settings.md), member ID only) to 10 s and
refuses it after a membership change. A running match attaches, a stopped one
asks first, a deleted, replaced or revoked member explains itself. Mentions open
only the card of the account they name ([identities](chat-filter.md)); shared `m:` or `msgid:` references and reply labels select loaded messages
([references](chat-references.md)). Attachment carries room, member and box IDs. Native `chat` tabs may send the TUI only complete guarded requests
(32-hex identities, valid names), `space view` tabs only plain ones; tabs are
reused only for identical guarded argv. A plain terminal runs the same guarded
`box attach`, then resumes. [4](#source-4) [5](#source-5) [6](#source-6)
[7](#source-7) [11](#source-11) [12](#source-12)

[FACT] Only the attach waits for the space lock. The lookup
(`resolveAttach` → `callChatAttachTarget`) takes no lock and has its 10 s
deadline; the chat-opened `box attach` then waits for the exclusive space lock
like any attach: no deadline, polling every 25 ms, printing `Waiting for
another operation in space …; Ctrl-C cancels.` after 1 s, and cancelling on
its context. Verified live on 2026-10-28 with the opt-in test on a pre-release build:
the lookup answered in 194 ms while a declined `box recreate` prompt held the
lock. [5](#source-5) [13](#source-13) [14](#source-14)

## Sources

1. <a id="source-1"></a> [Participants](../../internal/chatui/roster.go#L17) — `sortedMembers`, `participantRows`, `scrollRoster`, `rosterShown`, `memberCard`; wheel routing `inRoster` in mouse.go, page keys in model.go `key`.
2. <a id="source-2"></a> [Presence notices](../../internal/chatui/presence.go#L42) — `observeMembers`, `addPresence`, `presenceRows`.
3. <a id="source-3"></a> [Keys and actions](../../internal/chatui/model.go#L736) — `key`, `escape`, `runAction`, `append`, `pick`, `jumpToLatest`.
4. <a id="source-4"></a> [Overlays](../../internal/chatui/overlay.go#L386) — `savedNick`, `saveNick`, cards, `requestAttach`, `attachDone`, `attachResolved`; help/settings in the same file.
5. <a id="source-5"></a> [Host chat view](../../cmd/yoloctl/chat_view.go#L375) — `reconnect`, `loadAgents`, `resolveAttachWith`, `chatAttacher`.
6. <a id="source-6"></a> [Tab requests](../../internal/termio/tabrequest.go#L12-L80) — chat guard fields, `ValidateChat`, `ChatAttachArgs`.
7. <a id="source-7"></a> [TUI requests](../../internal/tui/tab_requests.go#L1) — `tabRequestHandler`, `handleTabRequest`; `tab_requests_test.go`.
8. <a id="source-8"></a> [Interaction regressions](../../internal/chatui/polish_test.go#L1) — selection, attach, reconnect, presence, scroll.
9. <a id="source-9"></a> [Shortcut regressions](../../internal/chatui/microfix_ui_test.go#L13) — header, `/help`, `/settings`, Ctrl+P, drafts.
10. <a id="source-10"></a> [Host forwarding](../../internal/tui/microfix_ui_test.go#L43) — `TestChatCtrlPForwardsThroughHostInput`.
11. <a id="source-11"></a> [UI regressions](../../internal/chatui/ui_package_test.go#L71) — actions, jump, padding, lookups.
12. <a id="source-12"></a> [Lookup regressions](../../cmd/yoloctl/chat_participant_runtime_test.go#L173) — bound and recheck.
13. <a id="source-13"></a> [Attach lock wait](../../internal/spacelock/lock.go#L21-L64) — `AcquireWithWait`; the notice in cmd/yoloctl/space_operation.go L217-L224; the lookup in `callChatAttachTarget` (chat.go L110) and `resolveAttach`/`resolveAttachWith` (chat_view.go L650–L694).
14. <a id="source-14"></a> [Opt-in live lookup](../../cmd/yoloctl/chat_attach_lock_live_test.go#L54) — `TestLiveChatAttachTargetAnswersWhileSpaceLockIsHeld`; run recipe in scripts/smoke/README.md.
15. <a id="source-15"></a> [Nickname refresh](../../internal/chatui/overlay.go#L70-L80) — `refreshParticipantLabels` and suggest.go `refreshOpenSuggest` (L161–189), called by `SessionMsg` (model.go L559–575) and `catalog` (channels.go L358–370); `TestNicknameSaveReconnectRefreshesOpenLabels` and `TestCatalogRenameRefreshesOpenCardAndSuggestions` in nickname_display_test.go; retained names in presence.go `observeMembers`/`addPresence` (L42–145).
16. <a id="source-16"></a> [Scrolling regressions](../../internal/chatui/participants_scroll_test.go#L1) — `TestParticipantsScrollInsteadOfShrinking`, `TestParticipantsFitWithoutScrolling`, `TestParticipantsTinyPanelWithChannelRail`, `TestParticipantsPageKeysNeverSkip`: many participants at small heights, side panel and overlay, rail-squeezed panels, gap-free paging.
