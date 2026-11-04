# Managed chat native desired state

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; destroy revocation `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited symbols when changing behavior.

[FACT] This foundation exposes host-admin-only `chat.state.get`, `.ensure`,
`.enroll` and `.revoke` controller recovery operations. These remain desired-only;
the separate [runtime service](chat-runtime.md) implements public chat behavior.
State API responses always include
`runtime_applied: false` and the missing runtime dependencies. Mutations use
ordinary controller admission/receipts and the native space lock; they require
the authoritative runtime inventory. No operation restarts a box or deletes
data. An interrupted RPC must be inspected, never automatically replayed.
[1](#source-1) [2](#source-2)

[FACT] `config/chat.json` on the verified native disk reserves one room, default
server name `chat` and channel `#space`. The caller supplies the human name.
An existing declaration or runtime with that name cannot be adopted. The
reservation records a separate server declaration ID; future creation must use
it. Storage preparation is distinct from runtime readiness. A reservation is
published before its private leaves, retaining identities after preparation
failure. Prepared storage with missing credentials or an inconsistent owner
marker fails closed instead of minting a replacement identity.
[3](#source-3) [4](#source-4) [10](#source-10)

[FACT] Native paths are `chat-state/<room-id>/owner.json`,
`server/config`, `server/data` and `members/<member-id>/token`. Controller parents
are private to root; projected leaves are private to the space UID/GID, with
0600 credential files. Tokens never appear in declarations or state API replies.
Writes set ownership before publication, fsync the file and parent, and walk
parents without following links. The helper for future volatile endpoints is
`/run/yoloctl/controller/chat/<room-id>`; the reserved controller namespace avoids
the default server box's teardown directory. The runtime service creates and
projects narrow endpoint leaves. [3](#source-3) [4](#source-4)
[5](#source-5) [11](#source-11) [12](#source-12)

[FACT] Enrollment mints an optional box declaration `id` on demand. Ordinary
legacy saves omit it; existing IDs cannot be replaced or removed by editing.
`box copy` builds new create options without the ID. Identity means the pair
of native disk ID and declaration ID, never the ID alone across spaces.
Membership uses separate random member IDs/tokens, independent of shared
account names. Desired mutations compare the current generation. Name reuse
requires explicit revocation of the old membership before enrollment; a fresh
member gets a fresh token. [1](#source-1) [3](#source-3) [6](#source-6) [13](#source-13)

[FACT] Revocation resolves its argument inside the mutation. A 32-lowercase-hex
argument is always a member ID, revoked or not. Anything else names the one
active member: validation keeps active names unique across members, the human
and the server. The human and the server are not box memberships and are
refused by name; their IDs are not declared members. An unknown or revoked name
gives a quoted refusal that points to `chat status`. A destroyed or replaced
box's membership still resolves by name. Desired-only revocation retains recovery files and
does **not** invalidate loaded ACLs; public runtime revocation separately reloads
and verifies the server. [3](#source-3) [9](#source-9) [10](#source-10)

[FACT] `box destroy` without `--keep-manifest`, after removing the declaration,
revokes the active membership with the destroyed box's name and immutable ID
under the destroy's space lock: desired state, then server application, then
role publication at release, with a notice that the name can be enrolled again.
Its effect footprint adds the room and server. Without a room or a member of
that name it prints nothing. A member of another or unknown identity is kept with a
warning naming `chat revoke`; in an unprepared room, `chat ensure` then
`chat revoke`.
A revocation that is saved but not applied by the server warns and names
`chat ensure`; the destroy succeeds, and role publication at release does not
replay the reload. Release-time role publication is unchanged, so an unreadable
chat store or inventory still fails the command there, as for every box
lifecycle command. [19](#source-19) [20](#source-20)

[FACT] Archives include the room declaration and server data, after the existing
drain/stop/export gates. They exclude projected credentials, generated server
configuration and volatile endpoints. Chat data accepts private directories
and regular files, rejecting links/special files, missing roots, duplicate
members and undeclared room paths. Import into a fresh disk deterministically
remaps room, server, member and enrolled box IDs, generates fresh credentials
inside that VM, and retains desired enrollment/history/artifacts. A restored
room is unprepared until explicitly ensured. Without a room, inert box IDs need
no remap because their identity scope includes the new disk ID. Identical partial
data can resume; differing data is retained with an error. [7](#source-7)
[8](#source-8) [14](#source-14) [15](#source-15)

[FACT] Focused tests cover ownership, partial inventories, strict/no-follow I/O,
interrupted and uncertain writes, name reuse, copy, on-demand IDs and independent
archive restoration. These use private temporary files and synthetic data;
they establish no live VM, SQLite, Unix-socket or AIRC revocation behavior.
[9](#source-9) [16](#source-16) [17](#source-17) [18](#source-18)

## Sources

1. <a id="source-1"></a> [Controller state dispatcher](../../cmd/yoloctl/controller_chat.go#L20) — `chatStateRequest`, `chatInventory`, `runControllerChatState`.
2. <a id="source-2"></a> [Registry and worker](../../cmd/yoloctl/controller_internal.go#L36) — `controllerOperations`, `runControllerWorker`.
3. <a id="source-3"></a> [Native state store](../../internal/managedchat/store.go#L16) — `Store`, `Ensure`, `Prepare`, `Enroll`, `RevokeTarget`, `Revoke`.
4. <a id="source-4"></a> [Ownership verification](../../internal/managedchat/verify.go#L14) — `Verify`.
5. <a id="source-5"></a> [Chat paths](../../internal/layout/chat.go#L24) — path helpers.
6. <a id="source-6"></a> [Declaration identity](../../internal/agentbox/identity.go#L14) — `NewIdentity`, `preserveIdentity`.
7. <a id="source-7"></a> [Chat archive](../../internal/spacearchive/chat.go#L20) — `writeChatState`, `validateChatEntry`, `restoreChatState`.
8. <a id="source-8"></a> [Controller integration](../../internal/controller/chat.go#L12) — `NativeChatStore`, `ReidentifyImportedChat`.
9. <a id="source-9"></a> [Store regressions](../../internal/managedchat/store_test.go#L1) — ownership, interrupted writes, name reuse, revoke by name or ID, paths.
10. <a id="source-10"></a> [Model](../../internal/managedchat/model.go#L33) — `Room`, `Validate`, `Reidentify`.
11. <a id="source-11"></a> [No-follow directories](../../internal/safepath/directory.go#L14) — `VerifyPrivateDirectory`, `PrivateDirectory`.
12. <a id="source-12"></a> [Atomic writes](../../internal/safepath/safepath.go#L166) — `WriteFileOwned`.
13. <a id="source-13"></a> [Copy options](../../cmd/yoloctl/agentbox_copy.go#L190) — `copyOptionsFromSource`.
14. <a id="source-14"></a> [Native archive](../../internal/spacearchive/state.go#L30) — `readDeclarations`, `WriteState`, `inspectState`, `RestoreState`.
15. <a id="source-15"></a> [Import preparation](../../cmd/yoloctl/space_archive.go#L260) — `prepareImportedSpace`.
16. <a id="source-16"></a> [Archive regressions](../../internal/spacearchive/chat_test.go#L1) — independent identities, interrupted restore, unsafe inventories.
17. <a id="source-17"></a> [Identity regressions](../../internal/agentbox/identity_test.go#L1) — ordinary saves, immutability, name reuse.
18. <a id="source-18"></a> [Controller/copy regressions](../../cmd/yoloctl/controller_chat_test.go#L1) — authorization, inventory, copy boundary.
19. <a id="source-19"></a> [Destroy revocation](../../cmd/yoloctl/agentbox_destroy_chat.go#L1) — `revokeDestroyedChatMember`, `activeChatMember`; caller in agentbox_destroy.go; `chatMemberServer` and the `box destroy` case of `spaceOperationIntent` in operation_scope.go.
20. <a id="source-20"></a> [Destroy revocation regressions](../../cmd/yoloctl/agentbox_destroy_chat_test.go#L1) — reuse after destroy, other or unknown identities, warn-only cleanup, footprint, wrapped release (unapplied revocation succeeds without replay; unreadable store keeps the release failure).
