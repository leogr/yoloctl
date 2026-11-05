# AIRC packaging and agentless sessions

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; dialog note checked at yoloctl `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; selected source verified at AIRC `b76ba355863cc3eb51900a313d7647d9190eedfa`; release v0.1.1 (source `6af217cfb6fe2fb05106ba592b75b9bf8225aa73`; after `b76ba355` it changes the Claude plugin launcher and Claude recipe, verified there) and its asset hashes verified against `SHA256SUMS` at yoloctl `3736b1c` and accompanying changes.

[FACT] `yolo.chat.skill` installs the named/JSONL client, server, Codex delivery
adapter, Claude delivery plugin and upstream skill. It requires no capabilities
and creates no credentials. Skills use Codex/Claude system paths and shared
`.agents/skills`. [1](#source-1) [2](#source-2)

[FACT] Repartee, `airc-box`, `yolo.chat.server` and `yolo.chat.repartee` are
removed. After custom-feature lookup, either feature name fails with managed-room/
`yolo.chat.skill` guidance, not an alias; custom features may reuse those names.
[2](#source-2) [13](#source-13)

[FACT] AIRC remains standalone: recipes fetch the published v0.1.1 release
archive for the build architecture (`uname -m`), verified by its pinned SHA-256
before extraction, not embedded source. A shebang heredoc bounds installer
Dockerfile lines and shell arguments.
[1](#source-1) [13](#source-13) [17](#source-17) [18](#source-18)
The archive's static binaries, skill, Claude plugin and license are installed;
nothing is compiled and the temporary release tree is removed. Pin/installer enter the composed-image recipe hash. Selected release
source includes scoped delivery receipts, the mentions-only/long-task away rule,
shared `m:` references (named client/native view; local `@N` arguments refused)
and same-account nick regain.
[1](#source-1) [3](#source-3) [4](#source-4) [23](#source-23)

[AIRC upstream contracts](airc-upstream.md) cover saved state, channel controls,
recovery, artifacts and monitoring. The manifest selects packaged code;
upstream support does not prove deployment. [3](#source-3)

[FACT] Recursive skill installation includes upstream support files. Codex's
`subscribe`/`airc-codex` queues batches without per-wake reads, rearming or
Python. Optional `--wait-for-idle` with `subscribe --sink-timeout 0` holds each
batch until thread idle; nothing selects it automatically. The separate opt-in
app-server route starts idle input or steers the identified active turn, with
durable protection against uncertain replay; approval routing is untested. See
[26](#source-26) Claude loads copied
`integrations/claude-plugin` through
`/etc/claude-code/managed-settings.d/50-airc-delivery.json`; its only `env`
entries are `CLAUDE_CODE_PLUGIN_DIRS` and `AIRC_DELIVERY_SESSION=space`.
Account leaf/base `managed-settings.json` stay untouched. The legacy helper
records waiter handles, launches nothing. Registration does not prove model wake. [1](#source-1) [13](#source-13) [20](#source-20) [21](#source-21) [23](#source-23)

[FACT] AIRC accepts Claude-monitor delivery on write, without a Claude receipt.
devOpUs’s live check (Claude Code 2.1.312, 2026-10-28) found `/usage` and `/resume`
held turns: batches were accepted ~2 s after a mention, but no turn began until
Esc closed the dialog; the first poll ~1.3 s later showed the monitor event,
with neither tested batch lost. Keep local dialogs closed while awaiting work.
These two single-batch checks held dialogs ~10 s; they establish no longer-hold
or queue guarantee. The checked hooks reference exposes no local-dialog
open/close event. [24](#source-24) [25](#source-25)

[FACT] Finite artifact upload/download uses configured HTTP credentials/origin,
without IRC registration/worker. Reserved UUID/name/size/digest metadata survives
history/named reads. Neither metadata nor `.png` proves image type, clipboard
behavior or native UI validation. [19](#source-19)

[FACT] Agentless `session_command` keeps ordered argv, no shell expansion;
real agents, empty executables and NUL bytes are refused. Inspect/copy/set/reuse
preserve/validate it. Set affects future launches; default is a login shell,
positional run argv overrides once.
[5](#source-5) [6](#source-6) [7](#source-7) [8](#source-8)

[FACT] The server advertises `CHANMODES=b,,,mnt`; joined members get 367/368
for `MODE #channel b`, only operators change masks. Persistent bans upgrade
transactionally from schemas 1–3 to 4 without data loss. Masks match
`nick!account@airc`, blocking joins/unprivileged speaking/uploads, not configured
reads: 100 masks/channel, 200 ASCII bytes/expanded mask. Schema 3 marks multiline
text; schema 4 adds staged attachments. Older servers refuse newer schemas. [16](#source-16) [22](#source-22)

[FACT] Tests cover declarations, pins, recipes and removed names. The in-box
`aircd` driver uses agent/human clients for steering, readiness/read replay,
confirmed sends, reconnect history and shutdown. Isolated `smoke-airc` adds
image/home identity, skills, absent removed tools, managed `aircd`, detach/rejoin
and restart. Cross-box fixtures use an unconfined server/human client and
separately authenticated proxy-confined participants for binary-scoped denial,
steering, artifacts and persistence. Reruns update only the disposable
controller. Test existence proves no live pass; bridge plaintext proves no remote TLS.
[15](#source-15) [9](#source-9) [10](#source-10) [11](#source-11)
[12](#source-12) [13](#source-13) [14](#source-14)

## Sources

1. <a id="source-1"></a> [internal/features/airc.go](../../internal/features/airc.go#L36) — `aircInstallDockerfile` and source download.
2. <a id="source-2"></a> [internal/features/features.go](../../internal/features/features.go#L82) — feature registry; `Lookup` and `removedFeatures` at L252–L280.
3. <a id="source-3"></a> [Source pin](../../internal/features/airc-source/manifest.json#L1) — public repository, release tag, source commit and per-architecture archive URL/SHA-256.
4. <a id="source-4"></a> [internal/agentboximage/composed.go](../../internal/agentboximage/composed.go#L45) — recipe identity.
5. <a id="source-5"></a> [internal/agentbox/agentbox.go](../../internal/agentbox/agentbox.go#L105) — `SessionCommand`; `Config.Validate` at L504.
6. <a id="source-6"></a> [cmd/yoloctl/session_client.go](../../cmd/yoloctl/session_client.go#L326) — `ensureAgentSession`; `resolveSessionArgv` at L537.
7. <a id="source-7"></a> [cmd/yoloctl/session_client.go](../../cmd/yoloctl/session_client.go#L537) — `resolveSessionArgv` for agentless commands and login-shell fallback.
8. <a id="source-8"></a> [cmd/yoloctl/agentbox_reconcile.go](../../cmd/yoloctl/agentbox_reconcile.go#L596) — `runSet` command replacement.
9. <a id="source-9"></a> [cmd/yoloctl/airc_test.go](../../cmd/yoloctl/airc_test.go#L13) — persistence, copy, set/clear and inspect regressions.
10. <a id="source-10"></a> [scripts/smoke/airc-runtime.py](../../scripts/smoke/airc-runtime.py#L74) — direct `aircd` and real-client driver; `shared_msgid`/`confirmed` at L51–L65 check named-client references.
11. <a id="source-11"></a> [scripts/smoke/airc.sh](../../scripts/smoke/airc.sh#L1) — isolated deployment.
12. <a id="source-12"></a> [internal/agentbox/session_command_test.go](../../internal/agentbox/session_command_test.go#L5) — argv validation boundaries.
13. <a id="source-13"></a> [internal/features/airc_test.go](../../internal/features/airc_test.go#L14) — source pin, installation contract, removed names and multiline recipe regressions.
14. <a id="source-14"></a> [scripts/smoke/airc-attach.py](../../scripts/smoke/airc-attach.py#L14) — managed `aircd` session driver.

15. <a id="source-15"></a> [scripts/smoke/airc-cross-box.py](../../scripts/smoke/airc-cross-box.py#L37) — bounded event waits, membership readiness and isolated cross-box collaboration driver.
16. <a id="source-16"></a> [AIRC a9d1d9b](https://github.com/leogr/airc/blob/a9d1d9bbf24858c64e1ce2c398ee7a2ec1e0c5a9/src/server/core.rs#L806-L812) — `mode` L1589–1739; `src/server/ban.rs:1–60` masks/limits; `src/store.rs:37–125` schema/upgrades/bans.
17. <a id="source-17"></a> [BuildKit v0.31.2 parser](https://github.com/moby/buildkit/blob/v0.31.2/frontend/dockerfile/parser/parser.go#L271) — physical-line scanner limit.
18. <a id="source-18"></a> [BuildKit v0.31.2 RUN conversion](https://github.com/moby/buildkit/blob/v0.31.2/frontend/dockerfile/dockerfile2llb/convert.go#L1358) — shebang heredoc execution from a mounted script.
19. <a id="source-19"></a> [AIRC finite CLI](https://github.com/leogr/airc/blob/a9d1d9bbf24858c64e1ce2c398ee7a2ec1e0c5a9/src/bin/airc.rs#L101-L195) — artifact dispatch; `src/client.rs:1157–1327`, `src/server/core.rs:2116–2187`, `src/agent_session/state.rs:785–816` file/metadata contracts.
20. <a id="source-20"></a> [AIRC monitor helper](https://github.com/leogr/airc/blob/a9d1d9bbf24858c64e1ce2c398ee7a2ec1e0c5a9/skills/airc/scripts/airc-monitor.py#L28-L157) — bounded retained-handle state; `src/agent_session/mod.rs:192–218,411–468,625–637` waiter observation/renewal.
21. <a id="source-21"></a> [AIRC Codex adapter](https://github.com/leogr/airc/blob/b1e88a82d83554b94ff53a04cb2fa8563bf6e37b/src/bin/airc-codex.rs#L1-L132) — stdin bounds, parent/header checks, literal queue argv and receipt classification.
22. <a id="source-22"></a> [AIRC 7da4150 store](https://github.com/leogr/airc/blob/7da41509514601ec60d4b7a97ec3dedf5ad090db/src/store.rs#L75-L98) — schema 4 migration and newer-schema refusal.

23. <a id="source-23"></a> [Selected AIRC skill](https://github.com/leogr/airc/blob/8a03beadfa18dfcc958711652547e1acdc3569c8/skills/airc/SKILL.md#L57-L73) — away/back rule; receipt notice L126–147 and shared reference contract L188–196; Codex wait-for-idle section in `references/codex.md` L120–165.

24. <a id="source-24"></a> [Claude delivery recipe](https://github.com/leogr/airc/blob/8a03beadfa18dfcc958711652547e1acdc3569c8/skills/airc/references/claude.md#L40-L71) — monitor notification/acceptance contract; timings above are live observations, not source-derived bounds.
25. <a id="source-25"></a> [Claude hooks reference](https://code.claude.com/docs/en/hooks#hook-lifecycle) — event catalog checked 2026-10-28; no local-dialog lifecycle hook.

26. <a id="source-26"></a> [Upstream delivery contracts](airc-upstream.md) — optional app-server start/steer, durable replay protection and approval-routing limit.
