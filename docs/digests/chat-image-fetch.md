# Chat image fetch

Verification: `765d227c0610c02b4d68071cd34552086b32eab1` and accompanying changes.

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

## Sources

1. <a id="source-1"></a> [chat_artifact_relay.py](../../cmd/yoloctl/chat_artifact_relay.py#L1) — relay program, embedded by chat_artifact_relay.go; chat_artifact_relay_test.go runs it against fake clients (success, mismatches, symlink, mode, failure, output flood, cancel, deadline with a TERM-ignoring child, invalid arguments).
2. <a id="source-2"></a> [chat_artifact_read.go](../../cmd/yoloctl/chat_artifact_read.go#L1) — `chatArtifactReadRequest`, `decodeChatArtifactReadRequest`, `matchesRoom`, `runControllerChatArtifactRead`, `executeChatArtifactRead`, `runChatArtifactRelay`, `verifyChatArtifactFrame`; registry and dispatch in controller_internal.go; chat_artifact_read_test.go.
