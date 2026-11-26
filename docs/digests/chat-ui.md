# Native chat UI component

Verification: `6e7df48d9682ee224e6a013066f45a710eae6f4b` and accompanying changes; re-read affected cited symbols when changing behavior.

Editing, submission, drafts and `/away` are in [composer](chat-composer.md);
Markdown display, code cards, tables and widths in [message display](chat-display.md).

[FACT] `internal/chatui` is a transport-neutral Bubble Tea model. Its caller
supplies connection events, logical messages, correlated receipts, roster/topic
snapshots and send/copy/link/attach/nickname/reconnect callbacks. It opens no
connection, reads no credentials and owns no server lifecycle; it starts
disconnected. `Outgoing.ID` correlates Pending, Confirmed, Rejected and
Uncertain states; queue acceptance is not confirmation. [1](#source-1)

[FACT] Trusted participant labels use the caller's saved room nickname
(`Identity.Nick`), falling back to the live nick; canonical identity names are
not displayed. Human role, box membership and agent also come from that
account-keyed identity map. Known nicknames carry no mark; an unknown account shows
only its dim plain nick with no glyph or qualifier label (in cards and the
Participants panel too), and source mode shows raw nick(account). Delivery state is a
separate label. Times are local; replies show author and snippet; the human gets
"you". Agent glyphs and colours come from the TUI `agent_icons`; humans use the
two-cell 👤 emoji in its own colors. Glyphs get a two-cell slot plus a gap, like
tab icons. [2](#source-2) [10](#source-10)

[FACT] Native chat applies host `seams.browser`: `on` opens on Ctrl+O or a link
click; `off` disables opening; `ask` and unknown values require another
non-repeat Ctrl+O in a local prompt displaying the fixed destination. Esc/Ctrl+C
cancels; text, paste and Enter neither approve nor edit the obscured draft. URLs
are revalidated before opening. [3](#source-3) [4](#source-4)

[FACT] PgUp, Ctrl+↑ or the wheel at the top calls `LoadOlder` with the oldest
ID. Older messages insert in order without unread; one page is outstanding, none
while disconnected; an empty page ends paging with a top marker. Past display
bounds a page's oldest messages give way, keeping history contiguous. Display
eviction past 256 messages/2 MiB/32768 logical lines is visible and keeps
unresolved correlation. A body past 16384 rows shows a limit notice; its
original stays copyable. [1](#source-1) [5](#source-5)

[FACT] The `chatstream` adapter reports the v2 `ready` limits, then requests
history, NAMES and WHO. The roster follows NAMES, JOIN/PART/QUIT/KICK/NICK and
op/voice MODE; WHO rows and AWAY events add authenticated accounts and away
state. RPL_TOPIC/NOTOPIC/TOPICWHOTIME and live TOPIC for the room become bounded
topic events. History requests resolve in order by batch or FAIL CHATHISTORY;
after a read gap, paging stays off and late history is discarded.
[6](#source-6) [7](#source-7)

[FACT] Logical line/offset anchors preserve reading position across resize, the
participants panel, arrivals and eviction; in a wrapped table the row holding
the anchor's cell byte wins, else the row showing the next cell text.
Disconnects flag an uncertain last send and offer explicit Reopen when the caller supplies the reconnect callback; otherwise they advise rerunning `yoloctl chat`. [11](#source-11) [1](#source-1) [5](#source-5)

[FACT] Component regressions cover paste, grapheme/kill editing, receipts,
replay, callbacks, Unicode bounds, Markdown, code cards, source mode, body
limits and anchors, with fake callbacks, not AIRC, a VM or a graphical terminal;
host key consumption, IME and CLI/stream integration are outside that evidence.
An opt-in contract test runs the real AIRC JSONL v2 client and server through
the adapter from `YOLOCTL_TEST_AIRC_BIN_DIR`. [8](#source-8) [9](#source-9)

## Sources

1. <a id="source-1"></a> [Model and integration types](../../internal/chatui/model.go#L34) — messages, `Options`, `Update`, `submit`, `delivery`, `incoming`, `append`, `evict`.

2. <a id="source-2"></a> [Transcript](../../internal/chatui/transcript.go#L48-L424) — `humanGlyph`, `identify`, `transcript`, `header`; `iconSlot` in hover.go.
3. <a id="source-3"></a> [Browser confirmation](../../internal/chatui/link_prompt.go#L18) — fixed destination and modal input.
4. <a id="source-4"></a> [Host browser policy](../../cmd/yoloctl/chat_view.go#L733-L751) — `configureChatBrowser`, validation and cancellation.
5. <a id="source-5"></a> [Scrolling and anchors](../../internal/chatui/render.go#L279) — `moveScroll`, `captureAnchor`; `place` in view.go.
6. <a id="source-6"></a> [Stream adapter](../../internal/chatstream/client.go#L160) — `Run`, `topicEvent`, `rosterEvent`, `record`, `historyGap`.
7. <a id="source-7"></a> [Roster, paging and topic regressions](../../internal/chatstream/roster_test.go#L9) — counts, refusals, gaps, `TestTopicEvents`.
8. <a id="source-8"></a> [Component regressions](../../internal/chatui/model_test.go#L15) — with `render_test.go`, `review_test.go`, `client_ux_test.go`, `link_prompt_test.go`, `markdown_test.go`, `reading_test.go` (code cards), `table_wrap_test.go` (wrapped tables), `table_spacing_test.go` (table spacers), `send_readiness_test.go`.
9. <a id="source-9"></a> [Real AIRC contract test](../../internal/chatstream/airc_integration_test.go#L1) — opt-in end-to-end adapter test.

10. <a id="source-10"></a> [Nickname display regressions](../../internal/chatui/nickname_display_test.go#L40) — `TestParticipantDisplaysUseSavedNicknames`, `TestNicknameCardsKeepSavedNickAndConnectionAlias`, `TestNicknameFallbackAndRawSourceIdentity`; generated labels, aliases, source and untrusted sessions.

11. <a id="source-11"></a> [Disconnected reopen](../../internal/chatui/reopen.go#L10) — `reopenOffered`, `reopenKey`, `reopen`, `reopenFooter`; `connection` in model.go marks uncertain delivery before explicit reconnect. `reopen_test.go` covers draft/reply/history retention, input priority, errors, duplicate activation and telemetry/deleted-channel exclusion.
