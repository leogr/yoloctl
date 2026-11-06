# Account authentication and mutation safety

Verification: `702c6ac7087f9c18a86edb0427fd4349cd82e788` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Account secrets and native state are separate delivery paths. At container
creation, `userEnvironmentJSON` reloads the selected account and projects only
nonempty secrets named by the agent's injection declarations. The JSON map
reaches the supervisor through `OPENSHELL_USER_ENVIRONMENT`. A malformed/missing
account fails creation even for an agent with no secret-injection rules; a
missing secret value is nonfatal. [1](#source-1)

[FACT] The built-in Claude adapter declares `ANTHROPIC_API_KEY`, a native
`.credentials.json`, and `CLAUDE_CONFIG_DIR` pointing into its mounted leaf.
The Codex adapter declares native `auth.json` and no secret-injection rules.
Storing `OPENAI_API_KEY` in an account alone therefore does not configure the
shipped Codex adapter. Native credentials are written by the agent or imported
once. [2](#source-2) [3](#source-3)

[FACT] Gemini declares native `oauth_creds.json` and `GEMINI_API_KEY` injection.
Its first-run auth selection, browserless Google login and headless import
requirements are described in the adapter evidence. [11](#source-11)

[FACT] `launch_env` becomes a sorted `env K=V ...` command prefix. It carries
nonsecret launch configuration across the sandbox SSH environment boundary.
It is visible in process argv and is not the secret-delivery mechanism.
[4](#source-4)

[FACT] Native import reads an explicit `--file` or the profile's preferred
host filenames followed by its credential filename. It requires an existing
account and a running controller. The caller reads the file; the worker
compares the adapter digest, refuses active account consumers, and writes via
the native helper while holding the account lock. Claude also has
a declared API-key extraction fallback stored as an account secret. The
operation copies once; it does not synchronize later host edits or own the
agent's subsequent credential updates. [2](#source-2) [5](#source-5) [9](#source-9)

[FACT] Secret set captures stdin or `--from-env` in the caller, then sends a
private request payload. It never reads the controller root environment for a
host variable. Secret edit files are private and local; optimistic revision
checks protect their save. Both operations require the controller.
[9](#source-9) [10](#source-10)

[FACT] Active-consumer checks inspect running containers using authoritative
runtime inventory. They match either the desired account or actual account
label. Explicit generic-shell labels establish that a missing account label
is expected; an unknown running identity fails closed. Native import refuses
consumers; forced account destruction stops them. Destruction removes native
state before the account manifest, retaining the declaration when native
cleanup fails. [6](#source-6) [7](#source-7)

[FACT] Auth status recognizes local Claude/Codex credential material with bounded
VM-native reads. Empty/malformed or tokenless objects do not count as credentials;
unknown formats remain unverified. Codex recognizes a nonempty API key or access
token; other adapters retain file-presence status. The output explicitly leaves
provider validity and environment/external auth unverified. Storage/access errors
remain unavailable. Codex file-store guidance does not force a store/provider
or add a mount; use --file for a host CODEX_HOME override. Claude's managed in-leaf
relocation and selected-profile import are detailed separately. List/show never reveal
secret values. [5](#source-5) [7](#source-7) [15](#source-15) [16](#source-16)

[FACT] Reconcile compares the account reference, not account secret/native
contents. Changing a secret does not automatically restart or recreate a box;
container recreation is how changed create-time environment is delivered.
Changing the selected account reference is a recreate reason. [1](#source-1)
[8](#source-8)

[FACT] Antigravity (`agy`) declares a separate native leaf and nested token file,
plus optional `GEMINI_API_KEY` injection. API-key mode additionally requires
its provider setting; native import does not export a host keyring. [12](#source-12)

[FACT] Pi declares nested native `agent/auth.json` and four provider API-key
secrets. Import copies every credential in the file, not settings or history.
Stored Pi credentials precede environment keys. [13](#source-13)

[FACT] OpenCode imports the whole native auth.json, uses five declared provider
secrets and persists XDG state in one leaf. Its optional auth.import_home_dir
selects a home-relative host import directory; custom XDG roots use --file. [14](#source-14)

## Sources

1. <a id="source-1"></a> [internal/agentbox/secret_injection.go](../../internal/agentbox/secret_injection.go#L44-L73) — `userEnvironmentJSON`.
2. <a id="source-2"></a> [internal/agentprofile/builtin/claude.yaml](../../internal/agentprofile/builtin/claude.yaml#L46-L119) — `launch_env`, `auth`, `secret_injection`.
3. <a id="source-3"></a> [internal/agentprofile/builtin/codex.yaml](../../internal/agentprofile/builtin/codex.yaml#L13) — `command`, `requires_features`, `auth` (line 89).
4. <a id="source-4"></a> [cmd/yoloctl/launchenv.go](../../cmd/yoloctl/launchenv.go#L26-L42) — `applyLaunchEnv`.
5. <a id="source-5"></a> [cmd/yoloctl/account_auth.go](../../cmd/yoloctl/account_auth.go#L119-L373) — `defaultHostAuthPaths` through `accountAuthFiles`.
6. <a id="source-6"></a> [cmd/yoloctl/space_lifecycle.go](../../cmd/yoloctl/space_lifecycle.go#L80) — authoritative consumer matching.
7. <a id="source-7"></a> [cmd/yoloctl/account.go](../../cmd/yoloctl/account.go#L210) — show, edit, deletion, secret commands.
8. <a id="source-8"></a> [internal/agentbox/diff.go](../../internal/agentbox/diff.go#L214) — account-reference drift.
9. <a id="source-9"></a> [cmd/yoloctl/controller_documents.go](../../cmd/yoloctl/controller_documents.go#L231) — native import and profile digest validation.
10. <a id="source-10"></a> [cmd/yoloctl/controller_client.go](../../cmd/yoloctl/controller_client.go#L182) — caller secret capture and local editor.

11. <a id="source-11"></a> [Gemini adapter](gemini.md) — shipped profile and upstream auth selection.

12. <a id="source-12"></a> [Antigravity adapter](agy.md) — native installation, auth and runtime contracts.

13. <a id="source-13"></a> [Pi adapter](pi.md) — install, state, provider and runtime contracts.

14. <a id="source-14"></a> [OpenCode adapter](opencode.md) — native runtime, account and validation contracts.

15. <a id="source-15"></a> [Claude profile storage](claude-auth.md) — managed relocation, supported local formats, import and preservation order.
16. <a id="source-16"></a> [Codex profile](../../internal/agentprofile/builtin/codex.yaml#L1) — same primary state path, native status format and unchanged secret delivery.
