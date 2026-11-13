# Chat export

Verification: `fc48bca38d7777c07e13bb1610611f9a15c8c17d` and accompanying changes.

[FACT] `yoloctl chat export` is host only: an operator endpoint is refused.
It reads `chat.status` and refuses unless the room exists and its server is
ready; it never runs `chat.ensure`. It then opens one human stream through the
existing `chat.stream` operation, as the chat view does, so the server's
access rules apply unchanged. A chat view of the same human holds the
nickname: the command then stops and names closing the view or `--take-over`,
which closes it. [1](#source-1)

[FACT] Before connecting, the command checks the flags and fixes every
target:
- every catalog channel by default, or the named ones (a leading `#` may be
  omitted); an unknown channel is an error;
- `--format text|jsonl`;
- `--since` and `--until` as RFC 3339 or `YYYY-MM-DD` at 00:00 UTC, selecting
  `since ≤ time < until`;
- one file per channel, `chat-<space>-<channel>-<UTC time>.txt|.jsonl` in the
  current directory, or `-o` (a file for one channel, an existing directory for
  several, `-` for standard output of one channel);
- an existing target is refused before reading unless `--force`. [1](#source-1)

[FACT] `chatstream.Export` drives the stream as its only consumer:
- It waits for the default channel's startup join and initial page. A channel
  the session has not joined is joined for the read and parted afterwards; only
  such joins are parted. A join or part refused only for pending setup
  (`setup-pending`) was not sent and is repeated after 100 ms; the driver's
  setup deadline bounds that.
- A channel's history is its LATEST page, which the driver now reports as
  `history-latest` when it finishes or fails, then BEFORE pages from the oldest
  received message ID until a page holds zero messages. Live messages are not
  collected.
- A gap, a refused or failed page, a page whose count differs from its
  messages, a repeated message ID or losing the channel fails that channel.
  Later channels continue; a closed connection or interrupt fails the rest.
  A failed part is reported, never assumed undone. [2](#source-2) [3](#source-3)

[FACT] Each channel is written as soon as it completes, with mode 0600, from a
flushed sibling temporary file, so a channel whose history read fails
publishes no file; a directory sync failure after publication can leave the
published file, as `atomicfile`'s `CommitError` states. Without
`--force` it is published with `atomicfile.Create`: a hard link that fails if
anything exists at the target, even a file that appeared during the export,
which is then reported and kept; the target directory must support hard links.
With `--force`, `atomicfile.Write` replaces the target by rename. Standard output receives a channel only once
it is complete; the summary then goes to standard error. Any failed channel
makes the command exit non-zero. [1](#source-1)

[FACT] `chatexport` writes both formats from the same records. Text has a
header with the space, channel, export time, received count with oldest and
newest times, the selected count and range, and the packaged retention policy
(10,000 messages and 30 days per channel), stated as policy. Each entry follows
as `TIME  nick: text`, continuation lines indented. A reply shows its parent's
sender and first words when the parent is in the file, otherwise its ID.
Published files show as `[file NAME, SIZE]` instead of an artifact
announcement or at each staged marker. Control characters become U+FFFD.
JSON lines keep every record field exactly: channel, id, RFC 3339 UTC time,
account, nick, text, reply_to, the artifact and the attachments with their
offsets. No attachment bytes are fetched. [4](#source-4)

## Sources

1. <a id="source-1"></a> [chat_export.go](../../cmd/yoloctl/chat_export.go#L1) — `newChatExportCmd`, `planChatExport`, `runChatExport`, `exportChat`, `writeChatExport`; chat.go registration; chat_export_test.go; atomicfile.go `Create`, `Write`, atomicfile_test.go.
2. <a id="source-2"></a> [export.go](../../internal/chatstream/export.go#L1) — `Export`, `SetupPendingCode`, `exporter.connect`, `control`, `channel`, `read`; export_test.go (fake stream); export_airc_integration_test.go (real aircd, `YOLOCTL_TEST_AIRC_BIN_DIR`).
3. <a id="source-3"></a> [channel_history.go](../../internal/chatstream/channel_history.go#L1) — `emitLatest` from `requestHistory`, `finishHistory`, `historyGap`, `cancelChannelHistory`; channels_client.go `endPending`; channel_events.go setup-pending refusal code.
4. <a id="source-4"></a> [chatexport.go](../../internal/chatexport/chatexport.go#L1) — `Select`, `Span`, `WriteText`, `WriteJSONL`, `Retention`; chatexport_test.go.
