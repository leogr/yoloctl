# Chat image fetch

Verification: `0a9b201a03c990d62371b0078060ba5ced116914` and accompanying changes.

[FACT] A fixed, embedded Python relay reads one artifact inside the confined
chat runtime: it runs only `airc artifact download --config CONFIG UUID PATH`
into a new private `/tmp` directory, as its one child in a new process group
under a 4 MiB file-size limit. Standard input is cancel-only (end of input
means cancel); its own 15 s deadline, a TERM then KILL of the group and a
bounded reap stop a stuck child; each output is capped at 8 KiB. It replies
with a 4-byte length and the bytes only after exit 0, a JSON reply naming the
expected size and hash, an owned no-follow regular 0600 file of exactly those
bytes, and removal of its exact file and directory; otherwise it writes
nothing and names the failure (and any residue it kept). Nothing calls it yet.
[1](#source-1)

## Sources

1. <a id="source-1"></a> [chat_artifact_relay.py](../../cmd/yoloctl/chat_artifact_relay.py#L1) — relay program, embedded by chat_artifact_relay.go; chat_artifact_relay_test.go runs it against fake clients (success, mismatches, symlink, mode, failure, output flood, cancel, deadline with a TERM-ignoring child, invalid arguments).
