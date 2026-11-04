# Account storage and authentication

[FACT] The VM controller owns account manifests and secrets on the same dedicated
native data disk as agent state. Account commands require a running controller.
Only the selected account/agent leaf is mounted into a box. Access verifies
ext4, UUID, owner marker and the live bind. A missing or inconsistent disk is an
error; normal reuse does not format or substitute empty state. Interrupted
initialization permits formatting only through the guarded all-zero check.
An explicit `space set --data-disk` grows the disk offline (Firecracker): the
host extends it while stopped, guest init grows and checks the filesystem
before mounting it, and nothing formats it [7](#source-7). [1](#source-1)

[FACT] Secrets projected at container creation and agent-owned native files
are independent auth paths. Import copies once and refuses active consumers;
status reports presence/unavailability rather than provider validity. Codex's
builtin uses native auth; Claude, Gemini, agy, Pi and OpenCode additionally declare API-key injection.
[2](#source-2)

[FACT] Account content is not reconcile drift. Changed environment secrets need
explicit recreation; changing the selected identity is itself a recreate reason.
Forced account destruction stops users and removes native state before its
manifest. [2](#source-2)

[DERIVED] Diagnose missing auth in order: selected space/account, adapter delivery
path, native-root verification or create-time environment, then provider behavior.
A present credential file alone cannot establish a valid login, and a changed
account manifest alone cannot refresh an existing container's environment.
[1](#source-1) [2](#source-2)

[FACT] Synthetic history regressions test reopening/appending across processes
and box/VM replacement. They do not establish provider login or host-share
correctness. Use disposable storage for these checks. [1](#source-1)

[FACT] Gemini supports native Google OAuth and account-secret API-key delivery.
Credential import does not select its auth method or export host keychains;
headless OAuth needs a saved selection or an explicit environment selection.
[3](#source-3)

[FACT] Antigravity uses the `agy` account leaf independently of Gemini, persists
both its CLI and shared configuration, and imports only its native token file.
Optional API-key injection needs a separate provider setting. [4](#source-4)

[FACT] agy reports state only through the status line in the account's own
settings. A new account is seeded with the reporter; for an existing one,
`account observe NAME agy --enable` is the explicit one-time opt-in. Like
import it refuses active consumers, and it refuses an account whose settings
do not exist yet, which the launcher seeds at the first start. It keeps every other setting and saves the
replaced status line, which still runs after each report; `--disable` restores
it. It is never implicit and never runs at launch. [8](#source-8)

[FACT] Pi keeps its own native `.pi` leaf, imports the whole multi-provider
`agent/auth.json` file once, and declares four provider API-key secrets. Stored
Pi credentials take precedence over environment keys. [5](#source-5)

[FACT] OpenCode persists its XDG config/cache/state and native auth.json in one
account leaf. It supports declared provider secrets or whole-file native import;
the headless ChatGPT device method is available for remote login. [6](#source-6)

## Sources

1. <a id="source-1"></a> [Native disk, recovery, and regression evidence](../digests/account-storage.md).
2. <a id="source-2"></a> [Authentication and mutation evidence](../digests/auth.md).

3. <a id="source-3"></a> [Gemini adapter](../digests/gemini.md).

4. <a id="source-4"></a> [Antigravity adapter](../digests/agy.md) — native installation, auth and runtime contracts.

5. <a id="source-5"></a> [Pi adapter](../digests/pi.md) — install, state, provider and runtime contracts.

6. <a id="source-6"></a> [OpenCode adapter](../digests/opencode.md) — native runtime, account and validation contracts.
7. <a id="source-7"></a> [Data disk size and offline grow](../digests/data-disk-grow.md).
8. <a id="source-8"></a> [Antigravity status line reporter](../digests/agy-observation.md) — routes A and B.
