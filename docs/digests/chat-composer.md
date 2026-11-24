# Native chat composer

Verification: `6e7df48d9682ee224e6a013066f45a710eae6f4b` and accompanying changes; re-read affected cited symbols when changing behavior.

[FACT] Enter submits once; Alt+Enter, Shift+Enter or Ctrl+J insert newlines.
Bracketed paste never submits; committed text cannot impersonate shortcuts. The
editor keeps newlines, tabs and indentation, normalizes CRLF/CR to LF, rejects
invalid UTF-8 and strips other controls. Movement and deletion use pinned ANSI
grapheme boundaries. Ctrl+A/E/K/U/W/Y edit lines, kill and yank;
Ctrl+Z/Ctrl+Shift+Z undo and redo. Vertical movement shares the rendered wrapped
layout. Drafts default to 64 KiB (at most 1 MiB, 8192 lines); oversized edits
fail atomically. Undo keeps at most 64 snapshots/4 MiB. [1](#source-1)
[2](#source-2) [3](#source-3)

[FACT] A draft remains editable while pending. Confirmation clears it only if
its text and editor/reply revisions still match the submitted snapshot.
Rejection preserves it; uncertainty or disconnect prevents another send until an
explicit receipt resolves the outcome. The component never retries or reconnects automatically. A disconnected view
can explicitly reopen its stream without replacing the model. [5](#source-5) A correlated full echo must also match the original text. Replay
overlap is deduplicated by message ID without counting as unread. When
disconnected, reconnecting, unjoined, deleted or resolving actions, empty draft
placeholders and the default footer truthfully display blocking status and draft
retention rather than advertising send, while local slash commands remain
executable. [1](#source-1) [4](#source-4)

[FACT] `/away [reason]` (default `away`) and `/back` control this human
connection, independently of the selected channel. Reasons are at most 200
UTF-8 bytes; `/back` refuses arguments. Pending/refused/uncertain commands keep
the draft; confirmation clears only the unchanged origin draft, even after a
channel switch. The top row shows `you: away`, pending or unknown independently
of the panel; settings and the exact self roster entry include its reason.
Typing/sending never marks back. Offline is unavailable; explicit reconnect
starts back with no replay. [6](#source-6)

## Sources

1. <a id="source-1"></a> [Model and integration types](../../internal/chatui/model.go#L34) — messages, `Options`, `Update`, `submit`, `delivery`, `incoming`, `append`, `evict`.
2. <a id="source-2"></a> [Editor](../../internal/chatui/editor.go#L12) — bounds, `cleanInput`, grapheme editing and undo.
3. <a id="source-3"></a> [Draft layout](../../internal/chatui/render.go#L137) — `draftLayout`, shared by cursor and composer.
4. <a id="source-4"></a> [Composer readiness](../../internal/chatui/view.go#L534-L742) — `composerStatus`, `inputRow`, `footer`.
5. <a id="source-5"></a> [Disconnected reopen](../../internal/chatui/reopen.go#L10) — `reopenOffered`, `reopenKey`, `reopen`, `reopenFooter`; `connection` in model.go marks uncertain delivery before explicit reconnect. `reopen_test.go` covers draft/reply/history retention, input priority, errors, duplicate activation and telemetry/deleted-channel exclusion.
6. <a id="source-6"></a> [Own availability](../../internal/chatui/availability.go#L1) — `availabilityCommand`, `availability`, `availabilitySummary`; `controlDone` in channels.go preserves the original draft.
