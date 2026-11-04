# Native chat input and message actions

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; code-block copy buttons `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

See [reading and selection](chat-reading.md) for drag copy and scrolling, and
[participants, settings and attachment](chat-participants.md) for people and
guarded session access.

[FACT] Layout: top band (space, channel, state, topic, unread, online count),
transcript, optional Participants panel, composer band (divider with channel and
[filter](chat-filter.md) control, centered jump and byte count; reply row; draft; blank row) and footer, plus a notice row
beside message actions. The band has dashboard colors, the `◈` icon and
right-aligned state, no action buttons. As the first row, it, the resize hint
and the browser prompt's top border use space view's explicit RGB black across
the full width, with dark-palette text in every theme. [1](#source-1)
[7](#source-7) [9](#source-9) [11](#source-11)

[FACT] `@` suggests mentions (panel order, a trusted account once under its
saved nick, then `@all`/`@here`, which notify everyone); a leading `/` suggests commands. ↑/↓ choose, Tab/Enter/click insert,
Esc dismisses that word; a missing or exact unique match closes the popup so
Enter sends. Whole-draft commands include `/help`, `/settings`, `/participants`,
`/source`, `/quit`, `/nick`, `/filter` and channel/topic actions via bounded host
operations, never raw IRC. Other text, folds and partial-send remainders are
sent, never run. [2](#source-2) [3](#source-3)

[FACT] Alt+↑/↓ or a click selects a message; its actions (reply, [quote](chat-quote.md)
while a highlight lies in it, copy, copy reference, copy code, open link, attach, mention) hold the footer until a send, Esc or another
pick, despite focus, typing, notices or arrivals; a pick clears info notices,
eviction ends it. Message focus gives arrows and Enter to them; typing returns
to the composer. Ctrl+R, Alt+Y, Alt+C and Ctrl+O act on
the selected (default latest) message or explain why not. [1](#source-1)
[3](#source-3) [9](#source-9) A click on a code block's `copy` button copies
that block under the same `seams.clipboard` check: off copies nothing and
says why, as its hover hint does. [12](#source-12)

[FACT] The default composer footer offers `Enter send` only when send-ready;
disconnected (`offline draft kept`), reconnecting (`connecting… draft kept`),
unjoined (`/join <room> to send`), deleted (`deleted read-only`) or unresolved
states show their blocking status and draft retention, while empty placeholders
mirror that condition. Drafts remain editable across every state. [1](#source-1)

[FACT] Regressions (fake callbacks only; no live terminal or controller)
cover drag text, clicks, clipboard-off, eviction, suggestions, commands,
message focus, attach lookups, Save/reconnect, notices, ordering, wheel rows,
the jump, padding and TUI guards. [5](#source-5) [4](#source-4)
[6](#source-6) [8](#source-8) [9](#source-9) [10](#source-10)

## Sources

1. <a id="source-1"></a> [View layout](../../internal/chatui/view.go#L75) — `View`, `place`, `topBar`, `composerDivider`, `composerStatus`, `inputRow`, `statusRow`, `footer`.
2. <a id="source-2"></a> [Suggestions](../../internal/chatui/suggest.go#L31-L230) — `commands`, `refreshSuggest`, `refreshOpenSuggest`, `acceptSuggestion`.
3. <a id="source-3"></a> [Keys and actions](../../internal/chatui/model.go#L736) — `key`, `escape`, `runAction`, `append`, `pick`, `jumpToLatest`.
4. <a id="source-4"></a> [TUI requests](../../internal/tui/tab_requests.go#L1) — `tabRequestHandler`, `handleTabRequest`; `tab_requests_test.go`.
5. <a id="source-5"></a> [Interaction regressions](../../internal/chatui/polish_test.go#L1) — selection, attach, reconnect, presence, scroll.
6. <a id="source-6"></a> [Host view regressions](../../cmd/yoloctl/chat_view_test.go#L1) — identities, agents, guarded request.
7. <a id="source-7"></a> [Shortcut regressions](../../internal/chatui/microfix_ui_test.go#L13) — header, `/help`, `/settings`, Ctrl+P, drafts.
8. <a id="source-8"></a> [Reading regressions](../../internal/chatui/reading_test.go#L25-L79) — jump control.
9. <a id="source-9"></a> [UI regressions](../../internal/chatui/ui_package_test.go#L71) — actions, jump, padding, lookups.
10. <a id="source-10"></a> [Lookup regressions](../../cmd/yoloctl/chat_participant_runtime_test.go#L173) — bound and recheck.
11. <a id="source-11"></a> [First-row regression](../../internal/chatui/first_row_test.go#L82) — `TestChatFirstRowIsBlackInEveryState`; `topBar`, `View`, `linkPromptView` (link_prompt.go L43) and spaceview `FirstRow` (theme.go L79-L93).
12. <a id="source-12"></a> [Copy button](../../internal/chatui/model.go#L1060) — `copyCode`, `activate` (`actCopyCode`) in mouse.go, the `hoverHint` case in hover.go; `codeblock_test.go`.
