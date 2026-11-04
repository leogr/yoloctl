# AIRC staged attachments (pinned upstream contract)

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; upstream source verified at AIRC `7da41509514601ec60d4b7a97ec3dedf5ad090db` (codeload archive SHA-256 `f2a3d16f1fe99957fe84dda10aacba520856ac2bbb9f6a0f462eec90b5d749ac`).

[FACT] The bundled pin lists the `message-attachments` feature. Its archive is
byte-identical to the reviewed AIRC tree `fccdda76`, and it changes no
`Cargo.toml`, `Cargo.lock`, toolchain or licence file relative to the previous
pin `7cb0c646`. [1](#source-1) [2](#source-2)

[FACT] Staging is a separate route, `POST /v1/channels/<channel>/staged`, so a
server without staging refuses it before any mutation. A staged artifact is
neither announced nor downloadable, even by its owner, until a message
publishes it. Limits:
- 10 minutes of life, surviving restarts;
- at most 8 live stagings and 8 × `max_bytes` per account;
- 64 MiB across the server.

Only answers carrying `X-AIRC-Refusal: <code>` prove that nothing was stored;
any other status leaves the outcome unknown. [3](#source-3) [4](#source-4)

[FACT] The ordinary upload route `POST /v1/channels/<channel>/artifacts` shares
that admission. Since this pin, its refusals before the store also carry
`X-AIRC-Refusal: <code>` and a short reason body; the previous pin answered 404,
408, 413 and 503 with bare statuses. Successful uploads are unchanged (201 with
the JSON reply), and a lost actor reply stays a bare 503 without the marker.
[3](#source-3) [8](#source-8)

[FACT] A channel PRIVMSG publishes 1–8 staged IDs through the client tag
`+airc-attach=UUID@OFFSET,…`. Each offset must start the marker `[Image #k]`
in the logical text, in order and without overlap. In one transaction, before
delivery, the server:
- checks ownership, channel and speak/read access;
- stores the message;
- moves the artifacts out of staging;
- sets the reserved `airc-artifacts` tag.

Any failure stores nothing and is a definite `FAIL … ATTACHMENTS` with reason
`invalid`, `unavailable`, `unknown k=K`, `consumed k=K msgid=M`, `too-long`,
`store` or `channel-only`. [5](#source-5) [6](#source-6)

[FACT] The server database moves from schema 3 to 4, which adds staged rows
and artifact ownership. The upgrade runs on first start and is forward-only:
an older binary refuses a newer schema. [7](#source-7)

[FACT] Retention makes `unknown k=K` ambiguous. An ID that is not staged
answers `consumed k=K msgid=M` only while its published artifact still exists
for the same account and channel; otherwise it is `unknown`, including an
artifact that count retention removed. [6](#source-6)

## Sources

1. <a id="source-1"></a> [lib.rs](https://github.com/leogr/airc/blob/7da41509514601ec60d4b7a97ec3dedf5ad090db/src/lib.rs#L37) — `message-attachments` in the feature table.
2. <a id="source-2"></a> [Source pin](../../internal/features/airc-source/manifest.json#L1) — release source commit and archive checksums.
3. <a id="source-3"></a> [protocol.md](https://github.com/leogr/airc/blob/7da41509514601ec60d4b7a97ec3dedf5ad090db/docs/protocol.md#L220-L233) — staging route, refusal marker, quotas and expiry.
4. <a id="source-4"></a> [attachments.rs](https://github.com/leogr/airc/blob/7da41509514601ec60d4b7a97ec3dedf5ad090db/src/attachments.rs#L13-L27) — limits; route in `server/http.rs` L37, `refused` L99.
5. <a id="source-5"></a> [protocol.md](https://github.com/leogr/airc/blob/7da41509514601ec60d4b7a97ec3dedf5ad090db/docs/protocol.md#L64-L82) — publication, reserved tag and refusals.
6. <a id="source-6"></a> [core.rs](https://github.com/leogr/airc/blob/7da41509514601ec60d4b7a97ec3dedf5ad090db/src/server/core.rs#L1275-L1310) — `publish`, including the `unknown`/`consumed` choice at L1298–1308; positions in `attachments.rs` `valid_positions` L118.
7. <a id="source-7"></a> [store.rs](https://github.com/leogr/airc/blob/7da41509514601ec60d4b7a97ec3dedf5ad090db/src/store.rs#L75-L98) — newer-schema refusal L79, staged table L88, `user_version=4` L96, expiry sweep L98.
8. <a id="source-8"></a> [http.rs](https://github.com/leogr/airc/blob/7da41509514601ec60d4b7a97ec3dedf5ad090db/src/server/http.rs#L99-L212) — `refused`, `upload` (L103) and `stage` share `accept` (L122); success at L196, unmarked lost reply at L209.
