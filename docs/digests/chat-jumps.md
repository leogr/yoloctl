# Chat jumps to messages

Verification: `0398f1f6638de276139c93675958ecade7782073` and accompanying changes.

[FACT] A click on a message reference or a reply label goes to that message
in its channel and selects it (top of the view, message focus, follow off),
clearing a filter that hides it. It looks in the list the channel shows, the
live list behind a window (which ends), then either list of the other open
channels (selected as a person does, a join carrying the target). Only then
does it use the [exact search](chat-references.md) of the joined channels.
[1](#source-1)

[FACT] A search hit requests one page (`AROUND msgid=… 50`) in that channel.
The stream collects its rows by batch tag and emits one `history-window` event
at batch close; interleaved live rows keep their path, and page rows convert
like live ones. The jump completes only with a page holding the exact ID;
anything else, or Esc meanwhile, leaves the source view and back list as
they were, and Alt+End or a send cancels it. A fetch shows "Opening that message…" or "Going
back…"; another unloaded click says "Still opening the previous message";
absence reads "That message is no longer in the history of your joined
channels". There is no quote panel. [2](#source-2) [6](#source-6)

[FACT] A page that meets the live list at its first row, with older rows that
fit the per-view bounds, goes in front of it. Otherwise it becomes the
channel's window and the live list is set aside, off its tail, so arrivals
count as unread. Each list owns its messages, selection, reading position,
highlight, older-page cursor and layout; the channel keeps draft, reply,
unread, members, filter and topic. Live messages, receipts (also a
disconnect's uncertain outcome), older pages and live notices go to the live
list; a window draws no notices. The whole-view trim counts both lists and
drops a hidden live list's oldest first, then window rows off screen.
[3](#source-3) [5](#source-5)

[FACT] A window's bottom is not the live tail: it keeps unread and asks for
one `AFTER` page; its top asks for `BEFORE`. A reply applies only for the
pending request ID; its rows (which exclude the anchor) are used minus held
ones. An empty `BEFORE` is the window's start; an empty `AFTER` ends that edge
until a live arrival; a lost anchor (`HISTORY_LOST`) is a failed read. A page
past the per-view bounds evicts, through the list's eviction, from the edge
farther from the reading position, never a row on screen; an offscreen pick
is released (keyboard focus moves only for the list on screen) and that edge
can be paged again; a pending read from an evicted edge row is void. A
replaced, closed or merged window releases the rendered bodies and table rows
neither list then holds; opened rows carry between a message's live and
history copies. After a newer page, and when
scrolling down, the window merges when it can, else switches to the live list
once the top row is in the run both share, in place. Jump to latest, sending
and Esc back end it; a channel switch keeps it; a reconnect drops its pending
page; a left or deleted channel stops its paging. [3](#source-3) [6](#source-6)

[FACT] Each jump records the view it left: channel, filter, focus, position
and selection by message key and ID, and whether it was a window. Esc cancels
a fetch, then clears a text highlight (in message focus only when Back is
available), then goes back one entry, then does its earlier steps. Going back
restores the filter, then position, selection and focus; for a replaced window
it fetches the page around its anchor, and the entry stays until that page
applies. The back list is dropped when the visible channel is at its latest
by any means, on a manual channel choice and on a reconnect. [4](#source-4)

[FACT] Regressions drive `Update` with recorded history requests: jumps and
back (tail, scrolled, filtered), chains, the old-message case, per-list state,
held targets, the dropped back list, fetch lines, cancels and failed returns,
channel events, full and whole-view bounds, sliding, merging and switching,
a hole after a gap, edge pages and failures, notices and receipts behind a
window. Stream tests cover page shapes and failures; a pinned-server test
checks that `AFTER` and `BEFORE` exclude their anchor. [7](#source-7)

## Sources

1. <a id="source-1"></a> [Click search](../../internal/chatui/mouse.go#L371-L443) — `showMessage`, `selectHeld`, `selectLoaded`, `channelHolding`; channels.go `showJoined` and `joinSwitch.back` (L530–L548).
2. <a id="source-2"></a> [Jump fetch](../../internal/chatui/reference_lookup.go#L22-L176) — `referenceLookup`, `lookupReference`, `nextReference`, `referenceResult`, `requestPage`, `windowPage`, `openPage`; the cancels in model.go `jumpToLatest` and `sendDraft`, staging.go `sendStaged`.
3. <a id="source-3"></a> [Window](../../internal/chatui/window.go#L22-L515) — `window`, `onLive`, `lists`, `holds`, `showWindow`, `endWindow`, `liveJoin`, `mergeable`, `mergeWindow`, `switchAtShared`, `scrollDown`, `windowEdge`, `edgePage`, `windowEvictOne`, `boundWindow`, `release`, `closeWindow`, `heldBeside`, `carryRows`; `showsList` in model.go `evict`; `moveScroll` in render.go L279; `addPresence` and `presenceRows` in presence.go L135–L214; live routing, the disconnect outcome and `scrollUp` in model.go.
4. <a id="source-4"></a> [Back list](../../internal/chatui/back.go#L13-L100) — `backEntry`, `snapshot`, `pushBack`, `dropBack`, `goBack`, `restore`; Esc in model.go `escape` (L1068–L1090) and message focus (L1147); the drop at the latest in `Update` (L460–L472); `chooseChannel` in channels.go L236.
5. <a id="source-5"></a> [Lists](../../internal/chatui/channels.go#L18-L90) — `messageList` embedded in `channelView`; `trimAggregate` and `trimOne` L801–L870.
6. <a id="source-6"></a> [Window pages](../../internal/chatstream/channel_history.go#L28-L294) — `requestHistory`, `wireRequest`, `failHistory`, `finishHistory`, `historyRecord` (a lost edge anchor fails); batch collection in channels_client.go L186; cmd/yoloctl/chat_view.go `history-window` (L895) and `chatViewMessage` (L955).
7. <a id="source-7"></a> [Regressions](../../internal/chatui/window_test.go#L1) — chatui window_test.go and reference_lookup_test.go; chatstream channel_window_test.go and `TestRealAIRCWindowPages`; cmd/yoloctl `TestWindowPageRequestAndConversion`.
