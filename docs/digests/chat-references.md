# Chat message references

Verification: `88971b185d6b7e9a7e33c16325d90c48c477f33c` and accompanying changes.

[FACT] In prose, a shared reference is `m:` plus exactly 22 base64url
characters whose decoding is a server ID's 16 bytes in display order and
re-encodes to the same text, or `msgid:` followed by a lowercase canonical UUID
(the server's message ID form). Each must stand alone: neither follows a
nickname byte; `m:` is not followed by a base64url byte or `=`, and `msgid:`
not by a nickname byte. Padded, aliased, partial, uppercase or other-alphabet
forms stay plain. Inline code, fenced code, source mode and Markdown or bare
link targets stay inert; the text shown and copied is unchanged. The codec
passes a byte-identical copy of the packaged AIRC source's vector file.
[1](#source-1) [5](#source-5) [8](#source-8)

[FACT] A click on a reference or a reply label selects the message with exactly
that ID among those loaded in the current channel view, leaves follow mode and
anchors it. Otherwise, when another open channel view has that ID loaded, the
click selects that channel as a person does (`selectChannel`: at once when
joined or deleted, else the ordinary typed join) and selects the message there.
When a join is needed, the message travels with that join intent and is shown
only by its own switch; a refused, uncertain, superseded or never-started join,
a part, a new stream or a move elsewhere before confirmation drops it, so a
later ordinary selection keeps that channel's reading position. Only channel views this client
already shows are searched, so nothing outside existing membership and history
becomes readable. Nothing matches by number, offset or prefix. Nothing is
fetched or sent, and no join starts unless the holding channel was left. A
target loaded nowhere keeps the position and says that it is not loaded in any
open channel and that Page Up in its channel may load older history. A loaded
target a [message filter](chat-filter.md) hides first shows all messages. A
deleted channel's retained history still navigates, read-only.
[2](#source-2) [5](#source-5)

[FACT] A marked reference whose exact ID is loaded in this channel view shows
in its place as one dimmed span, `↗ author: snippet…` (name cut to 16 cells,
the target's text to 24 runes with whitespace collapsed); one loaded only in
another open channel shows as `↗ #channel · author: snippet…`. The span is the
reference's own click target. Every such reference is previewed. The visible
transcript also rebuilds when another channel's loaded set changes in count or
at its first/last ID (a cheap fingerprint, not a content hash). An unloaded, evicted
or unknown target, a reference to the message itself,
a table cell (aligned before any preview) and source view keep the token.
The target-independent lines stay cached; a substitution step records each
span's token, and the substituted lines are keyed on the previews they show,
so a target that loads, leaves or is renamed rebuilds that message on the
next frame, clearing a highlight with an end in it. A pointer inside a span,
also wrapped over rows, covers it whole; drag copy and Quote yield the token,
Alt+Y the original text. [9](#source-9) [10](#source-10)

[FACT] Nicknames start with a letter or `_`, so `@` followed only by digits is
not a mention. It is plain text: no style, hover hint, action or explanation;
it never opens a card or navigates. Named mentions and self-mention marking
resolve through the [mention identity resolver](chat-filter.md), which names no
one for `@N`. [1](#source-1) [3](#source-3) [6](#source-6) [5](#source-5)

[FACT] A selected message whose ID has the UUID form offers Copy reference. It
copies the ID's `m:` reference through the explicit Copy callback only when
`seams.clipboard` allows it. Pending messages and other ID forms have no such
action; running it explains why. [4](#source-4) [5](#source-5)

[FACT] The packaged AIRC named client names records, `reply=` targets and
confirmed sends by the same `m:` references; `--reply`/`show` take them or a
full server message ID and refuse local `@N`. Saved older records show the
reference or `unavailable`; saved pending batches and `read --again` keep their
bytes, so older numbers there stay plain text. Images built before this pin keep
`@N`. The native codec, copy and loaded-channel-only navigation above are
unchanged; arbitrary opaque IDs still have no copy or navigation action.
[7](#source-7) [8](#source-8)

## Sources

1. <a id="source-1"></a> [Body tokens](../../internal/chatui/body.go#L16) — `decorateTokens`, `refPrefix`, `compactRef`, `refID`, `msgidPrefix`, `messageID`, `nickByte`; shared `Linked` prevents overriding link targets.
2. <a id="source-2"></a> [Navigation](../../internal/chatui/mouse.go#L322-L403) — `activate`, `showMessage`, `selectLoaded`, `channelHolding`; channels.go `joinSwitch.ref` carries a deferred message with its join and `showJoined` shows it, `selectChannel` owns join admission.
3. <a id="source-3"></a> [Hover hints](../../internal/chatui/hover.go#L213-L214) — `actRef`; kinds in rows.go L15–L23.
4. <a id="source-4"></a> [Copy reference](../../internal/chatui/model.go#L1004-L1152) — `messageActions`, `runAction`; Help row in overlay.go L106.
5. <a id="source-5"></a> [Regressions](../../internal/chatui/reference_test.go#L22-L459) — `TestReferencesReachOtherOpenChannels`, `TestCrossChannelReferenceJoinsLeftChannel`, `TestCrossChannelReferenceEndsWithItsJoin`; shared vectors and unused bits, tokens, inert numbers, wrapped and missing targets, reply labels, copy, channels and deletion.
6. <a id="source-6"></a> [Nickname grammar](../../internal/managedchat/settings.go#L8-L20) — `ValidateNickname`; mirrored by `validNick` in chatui/overlay.go.
7. <a id="source-7"></a> [Packaged AIRC references](https://github.com/leogr/airc/blob/8a03beadfa18dfcc958711652547e1acdc3569c8/skills/airc/SKILL.md#L188-L196) — shared references, refused `@N`, full msgids; codec and saved-record presentation in `src/agent_session/reference.rs` and `state.rs` L319–593.
8. <a id="source-8"></a> [Shared vectors](../../internal/chatui/testdata/message-references.json#L1-L53) — byte-identical copy of AIRC `tests/data/message-references.json` (SHA-256 `ee5a9cc849c6c3b1a603fdc6aa390500b987876da734f9e79876969e0f47a0ed`); this component checks the canonical entries, AIRC also the opaque ones.
9. <a id="source-9"></a> [Previews](../../internal/chatui/preview.go#L19) — `previewLabel`, `previewLabelIn`, `loadedElsewhere`, `otherLoaded` (rebuild key in view.go), `previewKeys`, `substitute`, `refreshPreviews`; pure `ApplyReplacements`, `ReplacementBounds`, `ReplacementSourceMap` and `DisplayLine.CopyText` live in markdownview; transcript and selection remain chat-owned.
10. <a id="source-10"></a> [Preview regressions](../../internal/chatui/preview_test.go#L1-L159) — span, copy, click, Quote, loaded-only, table cell, source view, budget, later targets, rename, wrapped atomic span and hover; spans in `quoteFixtures`.
