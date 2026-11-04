# Opt-in space operator authority

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Feature `yolo.space.operator` installs a `yoloctl` PATH symlink and requires the
same-name positive capability. That grant provides helper read access and the
Unix endpoint access needed to connect. It is absent by default. The executable
is the same CLI as the host, with the same box commands, aliases and flags;
there is no parallel reduced command parser. [1](#source-1) [2](#source-2)
[3](#source-3)

[FACT] Only `box create` accepts `--operator`: after agent defaults it merges
and deduplicates `yolo.space.operator` and `yolo.chat.skill` with explicit
features. Networking defaults to unconfined unless explicitly set, including
`--unconfined-network=false`. The ordinary space-default/explicit workdir and
agent/account validation paths remain. Manifests store expanded settings, so
later preset additions do not change existing boxes. The preset neither
starts/enrolls chat nor installs its server or human client. Cobra exposes the
flag to shell and palette completion. [19](#source-19) [20](#source-20)
[21](#source-21)

[FACT] This is broad authority over every box in one running space, including
existing accounts and credentials, granted host projects, stronger capabilities,
delegated operators, destructive/self-targeted operations and managed chat
administration. The managed server's human identity and arbitrary execution/
terminal access remain host-only. [22](#source-22) Create/edit/set
and inspect report that authority. It does not expose space/account/catalog
administration or another space. Revocation does not undo previously created
boxes. The server registry explicitly authorizes operations; future commands
are not granted by a prefix wildcard. [3](#source-3) [4](#source-4)
[5](#source-5) Operators also manage privileged boxes (`privileged: true`) and
use a running one, gaining its space-VM root, but never launch one: only the host
creates or starts a privileged container, and an operator may only restart or
recreate one the same request proved running. [23](#source-23)

[FACT] Provisioning creates a random container-incarnation directory beneath
`/run/yoloctl/operators/`, mounted read-only at `/run/yoloctl/operator` in that
box. The socket listener supplies identity; client metadata only supplies
routing. Authorization checks the current declaration's effective grant, a
running container, identity/incarnation labels and its exact read-only endpoint
bind. Existing connections are rechecked periodically and canceled on failure;
box and attach commands and box edit apply recheck the grant once the native
mutation lock is held, before effects. A copied binary or same-account peer
inherits no endpoint. [4](#source-4) [5](#source-5) [6](#source-6)
[24](#source-24)

[FACT] Host and operator mutations use the same dependency holds and recheck
them at the effect gate. Availability never grants authority. Operator
refusals omit other receipts and principals; only host administration can
acknowledge unknown outcomes. [25](#source-25) [5](#source-5)

[FACT] Replacement/removal retires old registrations. On VM reboot the controller
reconstructs volatile endpoints from container labels, native boxes from their
incarnation and qualification labels; a box it cannot restore gets a notice and
never stops controller startup. Helpers are directory
binds, allowing an atomic executable replacement to appear in an existing box;
old executable copies still fail the exact-bundle handshake. No admin/session
socket, whole data disk or account-wide credential token is mounted into the
operator. [4](#source-4) [6](#source-6) [7](#source-7)

[FACT] Relative workdir/mount sources translate through the caller container's
actual binds to granted host paths. Ephemeral/private-account paths cannot be
promoted to host shares. The shared service still canonicalizes and verifies
protected roots and live grants. Operators cannot copy across spaces; the host
can transfer a source declaration snapshot between running controllers.
[4](#source-4) [8](#source-8)

[FACT] The embedded guidance is mounted read-only in Codex's administrator skill
location, Claude Code's managed skill location, and Gemini's user agent-skill
alias at `/home/sandbox/.agents/skills/yolo-box-operator`. The operator feature
creates sandbox-owned `.agents` and `.agents/skills` parents at build time,
so user skills remain writable even without entrypoint UID remapping.
[1](#source-1) These locations follow the cited upstream documentation;
the live smoke checks directory writes and read-only guidance, not that
every future agent release discovers or follows the skill. Guidance is advice;
server authorization is independent of it. [6](#source-6) [9](#source-9)
[10](#source-10) [11](#source-11) [14](#source-14)

[FACT] The guidance includes structured `--box-use` interaction with shared
agent sessions and the existing PTY/passthrough procedure. Peer activity is
advisory; installed guidance itself grants no session authority.
[9](#source-9) [18](#source-18)

[FACT] Tests cover the shared command inventory, flag fidelity, caller-path
mapping, stopped/replaced endpoint rejection and connection revocation. The
isolated live script exercises helper calls, denied scopes, concurrent writers,
controller restart, grant removal, offline errors and VM replacement.
[3](#source-3) [12](#source-12) [13](#source-13)

[FACT] The agy launcher links the mounted helper guidance into its global
`~/.gemini/antigravity-cli/skills/yolo-box-operator` location as the sandbox user.
An absent helper after revocation leaves an inert link; it never grants authority.
[15](#source-15)

[FACT] Pi discovers the existing user `.agents/skills` mount. No Pi-specific
mount or account-directory mutation is needed. Its local-provider runtime
fixture checks that the operator skill is advertised in the actual CLI model context.
[16](#source-16)

[FACT] OpenCode discovers the operator skill through the existing .agents/skills
mount. Its local-provider smoke checks discovery by the real CLI; authorization
continues to come from the controller grant. [17](#source-17)

## Sources

1. <a id="source-1"></a> [internal/features/features.go](../../internal/features/features.go#L103) — built-in operator installation recipe.
2. <a id="source-2"></a> [internal/capabilities/capabilities.go](../../internal/capabilities/capabilities.go#L121) — operator filesystem grant.
3. <a id="source-3"></a> [cmd/yoloctl/controller_test.go](../../cmd/yoloctl/controller_test.go#L184) — CLI fidelity, authority and path tests.
4. <a id="source-4"></a> [cmd/yoloctl/controller_operator.go](../../cmd/yoloctl/controller_operator.go#L28) — authorization, provisioning support, warnings and path translation.
5. <a id="source-5"></a> [internal/controller/server.go](../../internal/controller/server.go#L126) — trusted principal and periodic revocation.
6. <a id="source-6"></a> [internal/agentbox/live.go](../../internal/agentbox/live.go#L168) — operator mounts and incarnation labels.
7. <a id="source-7"></a> [internal/operator/endpoint.go](../../internal/operator/endpoint.go#L58) — registration lifecycle and endpoint provisioning.
8. <a id="source-8"></a> [cmd/yoloctl/controller_client.go](../../cmd/yoloctl/controller_client.go#L96) — shared frontend routing and cross-space copy.
9. <a id="source-9"></a> [assets/embed.go](../../assets/embed.go#L13) — embedded BoxOperatorSkill.
10. <a id="source-10"></a> [OpenAI skills documentation](https://learn.chatgpt.com/docs/build-skills) — administrator scope `/etc/codex/skills`, consulted 2026-09-30.
11. <a id="source-11"></a> [Claude Code skills documentation](https://code.claude.com/docs/en/skills) — managed Linux skills path, consulted 2026-09-30.
12. <a id="source-12"></a> [internal/controller/protocol_test.go](../../internal/controller/protocol_test.go#L330) — active-connection revocation regression.
13. <a id="source-13"></a> [scripts/smoke/controller-operator.sh](../../scripts/smoke/controller-operator.sh#L3) — isolated runtime gate.

14. <a id="source-14"></a> [Gemini v0.62.0 skill discovery](https://github.com/google-gemini/gemini-cli/blob/v0.62.0/packages/core/src/skills/skillManager.ts#L71) — user skills and `.agents/skills` alias.

15. <a id="source-15"></a> [Antigravity adapter](agy.md) — native installation, auth and runtime contracts.

16. <a id="source-16"></a> [Pi adapter](pi.md) — install, state, provider and runtime contracts.

17. <a id="source-17"></a> [OpenCode adapter](opencode.md) — native runtime, account and validation contracts.
18. <a id="source-18"></a> [Box-use](box-use.md) — shared-agent interaction contract.
19. <a id="source-19"></a> [Creation preset](../../cmd/yoloctl/agentbox_operator.go#L5-L31) — maintained list and `applyOperatorCreatePreset`.
20. <a id="source-20"></a> [Create command](../../cmd/yoloctl/agentbox.go#L764-L884) — shared handler, validation, workdir and flag registration.
21. <a id="source-21"></a> [Preset regressions](../../cmd/yoloctl/agentbox_operator_test.go#L17) — decoded controller requests, saved settings, validation and real Cobra completion.
22. <a id="source-22"></a> [Operator chat boundary](operator-chat.md) — allowed administration, participation and protected human server.
23. <a id="source-23"></a> [Privileged development boxes](privileged-boxes.md) — host-started launch boundary.
24. <a id="source-24"></a> [cmd/yoloctl/controller_operator.go](../../cmd/yoloctl/controller_operator.go#L253-L265) — `revalidateOperator`, called under the lock by `registerSpaceOperations` and box edit apply.

25. <a id="source-25"></a> [internal/controller/holds.go](../../internal/controller/holds.go#L1) — `heldError`, `loadHolds` and dependency intersections.
