# AgentMon Live feature

Verification: `55760051dce0c80f3523c005929f07aa0470bc2f` and accompanying changes; re-read cited sources when changing behavior.

[FACT] The `agentmon` feature installs the AgentMon Live plugin for Claude
Code at `/usr/local/share/agentmon-live`. The source is the GitHub archive of
the commit behind release `v0.39.0`, named by its full revision, fetched over HTTPS and checked against a
pinned SHA-256 before extraction. Nothing is compiled. A marker file holding
the revision makes a rebuilt layer skip a matching installation. The feature
requires no capability. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] The image-level drop-in
`/etc/claude-code/managed-settings.d/60-agentmon.json` sets one environment
value and nothing else. `CLAUDE_CODE_PLUGIN_DIRS` lists AIRC's plugin directory and then AgentMon's:
the variable is one colon-separated value, this drop-in sorts after AIRC's
`50-airc-delivery.json`, and the later file decides a key both set. Nothing is
written to the shared account leaf. [1](#source-1) [4](#source-4)
[5](#source-5) [8](#source-8)

[FACT] When the directory `/var/lib/agentmon-live/spool` exists, the pinned
plugin writes each session's snapshot to `<that directory>/<session id>.json`
and starts no server. `AGENTMON_SPOOL` names another directory and is not set
by the feature. AgentMon's server on the host reads the other side of the
mount and marks such a session `sandbox`. The feature grants no
mount: the directory exists in a box only through a declared box mount, which
must fit the space's host grants like any other. [6](#source-6)
[7](#source-7) [9](#source-9)

[FACT] No smoke covers this feature. A container built from the generated
recipe plus Claude Code 2.1.322 loaded the plugin through the drop-in, beside
a stand-in for AIRC's drop-in whose plugin directory was absent, and its
session reached a host server through a bind-mounted spool. That run used
plain Docker. The same recipe, as a user feature on a released yoloctl, was
also run in a Firecracker space: the image built, the plugin loaded under
OpenShell, the write to the mounted spool was allowed, and the session showed
on the host. Both runs used an earlier AgentMon commit that took the directory
from `AGENTMON_SPOOL`; the directory-exists rule of the pinned commit has not
been run in a box, and neither run used the built-in registry entry.
[1](#source-1)

## Sources

1. <a id="source-1"></a> [internal/features/agentmon.go](../../internal/features/agentmon.go#L8-L59) — pinned revision and hash, `agentmonSettingsJSON`, `agentmonInstallDockerfile`.
2. <a id="source-2"></a> [internal/features/features.go](../../internal/features/features.go#L88-L92) — `agentmon` definition, no required capabilities.
3. <a id="source-3"></a> [internal/features/agentmon_test.go](../../internal/features/agentmon_test.go#L10) — `TestAgentmonInstallsOneVerifiedCommit`: verification precedes extraction.
4. <a id="source-4"></a> [internal/features/agentmon_test.go](../../internal/features/agentmon_test.go#L46) — `TestAgentmonSettingsKeepAIRCPlugin`.
5. <a id="source-5"></a> [Claude Code managed settings](https://code.claude.com/docs/en/managed-settings) — drop-ins apply in alphabetical order with objects deep-merged.
6. <a id="source-6"></a> [AgentMon Live at the pinned commit](https://github.com/codenotary/agentmon-live/blob/5794505e5c230ef71d617bb19aef518f4b339ef1/hooks/web.ts) — `SPOOL_DIR`, `startWeb`, `publish`, `stopWeb`.
7. <a id="source-7"></a> [internal/agentbox/compose.go](../../internal/agentbox/compose.go#L148) — `compilePolicy`: declared mounts enter the filesystem policy.
8. <a id="source-8"></a> [Claude Code environment variables](https://code.claude.com/docs/en/env-vars) — `CLAUDE_CODE_PLUGIN_DIRS`, colon-separated, since 2.1.280.
9. <a id="source-9"></a> [AgentMon Live server/spool.mjs at the pinned commit](https://github.com/codenotary/agentmon-live/blob/5794505e5c230ef71d617bb19aef518f4b339ef1/server/spool.mjs) — `readSpool`.
