# Chat image fetch

Verification: `b3b2cf92659f79545ec4cc8d405d5f1b0a3c122e` and accompanying changes.

[FACT] A fixed, embedded Python relay reads one artifact inside the confined
chat runtime: it runs only `airc artifact download --config CONFIG UUID PATH`
into a new private `/tmp` directory, as its one child in a new process group
under a 4 MiB file-size limit. Standard input is cancel-only (end of input
means cancel); its own 15 s deadline, a TERM then KILL of the group and a
bounded reap stop a stuck child; each output is capped at 8 KiB. It replies
with a 4-byte length and the bytes only after exit 0, a JSON reply naming the
expected size and hash, an owned no-follow regular 0600 file of exactly those
bytes, and removal of its exact file and directory; otherwise it writes
nothing and names the failure (and any residue it kept).
[1](#source-1)

[FACT] `chat.artifact.read` is a host-only, read-only controller operation (no
effect gate, not box- or operator-allowed). It accepts only typed metadata of
at most 2 KiB: room, disk, human and channel identities, channel name, message
ID and the artifact UUID, size (1 B–4 MiB) and SHA-256. Under a 20 s bound it
opens the verified managed chat connection, requires the room, disk and human
to match and the human to be a current member of that channel, requires the
client's `artifact-cli` feature, re-verifies the peer and the room, then runs
the relay once with a dedicated pipe as its input: the write end stays open
until the run returns and cancellation closes it. Output is bounded to the
frame, notices to 8 KiB. Only exactly one frame of the requested size and hash,
with nothing after it, is accepted; its bytes are the operation's output. A
relay failure is reported with its one printable line. It never stages,
uploads, joins or retries. [2](#source-2)

[FACT] In the TUI, a preview request from a chat pane (which also carries its
chip text, one printable line of at most 512 bytes) starts the single fetch
job of the whole TUI when previews are supported; a second request meanwhile
gets `busy`. The job (30 s) calls the reader that `cmd/yoloctl` installs: it
resolves the pane's space like the CLI (its `--space`, else the TUI's pinned
space, else the default) and runs `chat.artifact.read` with output bounded to
the requested size, accepting only those exact bytes. `chatimage.Prepare`
then checks the PNG dimensions (at most 16 Mi pixels) before one full decode,
scales it by area averaging to fit 512×512 and the pane budget of 60×12 cells
at the measured cell size, never enlarging, and re-encodes it (at most 2 MiB).
[3](#source-3) [4](#source-4)

[FACT] Back on the event loop a result is dropped if its pane is gone, and
answered `stale` if the generation, support or cell size changed; decode
failures answer `not_png` or `too_large`, others `failed`. Otherwise the host
allocates an ID, registers the placement and chip for that pane (deleting a
replaced one), writes one Kitty sequence through Bubble Tea's raw output
(direct PNG, `a=T`, virtual placement `U=1`, quiet, 4 KiB base64 chunks) and
answers `ready` with a fresh token, the ID and the cell size. A release with
that token or the pane's end deletes exactly that ID. Nothing is cached yet:
another request reads again. [3](#source-3) [4](#source-4)

## Sources

1. <a id="source-1"></a> [chat_artifact_relay.py](../../cmd/yoloctl/chat_artifact_relay.py#L1) — relay program, embedded by chat_artifact_relay.go; chat_artifact_relay_test.go runs it against fake clients (success, mismatches, symlink, mode, failure, output flood, cancel, deadline with a TERM-ignoring child, invalid arguments).
2. <a id="source-2"></a> [chat_artifact_read.go](../../cmd/yoloctl/chat_artifact_read.go#L1) — `chatArtifactReadRequest`, `decodeChatArtifactReadRequest`, `matchesRoom`, `runControllerChatArtifactRead`, `executeChatArtifactRead`, `runChatArtifactRelay`, `verifyChatArtifactFrame`; registry and dispatch in controller_internal.go; chat_artifact_read_test.go.
3. <a id="source-3"></a> [prepare.go](../../internal/chatimage/prepare.go#L1) — `Prepare`, `downscale`, `TransmitSequence`, `DeleteSequence`; prepare_test.go.
4. <a id="source-4"></a> [image_broker.go](../../internal/tui/image_broker.go#L1) — `ReadChatArtifact`, `answerPreview`, `finishImageJob`, `flushImageDeletes`, `chatPaneSpace`; chat_artifact_host.go `readChatArtifactForPreview`; image_broker_test.go.
