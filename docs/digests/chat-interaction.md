# Native chat input and message actions

Verification: `77a14b6864848cababd79a2d98e231f8c9575dd8` and accompanying changes; re-read affected cited symbols when changing behavior.

See [reading and selection](chat-reading.md) for drag copy and scrolling (the
follow view shares its copy-on-release policy and notices), and
[participants, settings and attachment](chat-participants.md) for people and
guarded session access.

[FACT] Layout: top band (space, channel, state, topic, unread, online count),
channel navigation (side rail or one-row strip above the transcript),
transcript, optional Participants panel, composer band (divider with channel and
[filter](chat-filter.md) control, centered jump and byte count; reply row; draft; blank row) and footer, plus a notice row
beside message actions. The band has dashboard colors, the `◈` icon and
right-aligned state, no action buttons. Below minimum size, channel rows precede
the resize hint. As the first row, the band, compact channel list, resize hint
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

[FACT] Alt+↑/↓ or a click selects a message; its actions ([quote](chat-quote.md)
first while a highlight lies in it, reply, copy, copy reference, copy code, open link, attach, mention) hold the footer until a send, Esc or another
pick, despite focus, typing, notices or arrivals; a pick clears info notices,
eviction ends it. Message focus gives arrows and Enter (from the first action) to them; typing returns
to the composer. Ctrl+R, Alt+Y, Alt+C and Ctrl+O act on
the selected (default latest) message or explain why not. A drag picks the
message where it starts, at its first motion (a header, gutter or separator
press snaps to it; a room notice row picks none), so Enter quotes a highlight
made in it at once. [1](#source-1)
[3](#source-3) [9](#source-9) [13](#source-13) A click on a code block's `copy` button copies
that block under the same `seams.clipboard` check: off copies nothing and
says why, as its hover hint does. [12](#source-12)

[FACT] Explicit channel selection (picker, rail or Alt navigation) requests JOIN
when the human is not joined, using the same typed action and server checks as
`/join`, then shows the channel on stream confirmation. Draft/reply/history stay
with their views. Joined selection remains local; `/part`, kicks, catalog
refreshes and internal switches do not themselves rejoin. A later deliberate
selection can request rejoining. [14](#source-14)

[FACT] The default composer footer offers `Enter send` only when send-ready;
disconnected (`offline draft kept`), reconnecting (`connecting… draft kept`),
unjoined (`/join <room> to send`), deleted (`deleted read-only`) or unresolved
states show their blocking status and draft retention, while empty placeholders
mirror that condition. Drafts remain editable across every state. [1](#source-1)

[FACT] After IRC disconnection, a supplied reconnect callback enables the footer's
`[ Reopen ]`: click, or Shift+Tab to focus then `r`/Enter; Tab/Esc returns to
the draft. Literal `r` in the editor, paste and extended committed text remain
text. Overlays and browser prompts take priority. Reconnect retains this model's
drafts, replies, history and uncertain-delivery markers; it sends nothing and
cannot automatically take over a nickname or revive a deleted channel. Pending
reconnect disables another activation; a failure shows its error and allows an
explicit retry. Initially connecting, connected, and telemetry-only loss do not
offer IRC reopen. The host's ended-pane action separately reopens exited chat
and followed logs. [15](#source-15) [16](#source-16)

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
13. <a id="source-13"></a> [Drag pick](../../internal/chatui/selection_focus_test.go#L1-L168) — `mouseDown` sets `dragState.pick`, `mouseMove` picks at the first motion (mouse.go); six regressions.

14. <a id="source-14"></a> [Human channel selection](../../internal/chatui/channels.go#L218) — `selectChannel` and `cycle`; user routes in model.go, mouse.go and overlay.go; `channel_select_test.go` exercises commands and membership results.

15. <a id="source-15"></a> [Reopen action](../../internal/chatui/reopen.go#L10) — `reopenOffered`, `reopen`, `reopenKey`, `reopenFooter`; `key`/`connection` in model.go, `footer` in view.go and `activate` in mouse.go; focused regressions in `reopen_test.go`.
16. <a id="source-16"></a> [Existing stream owner](../../cmd/yoloctl/chat_view.go#L374) — `reconnect`.
