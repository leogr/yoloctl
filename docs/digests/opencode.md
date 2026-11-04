# OpenCode adapter

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes;
upstream inspected at `v1.18.34` (`aec0b9a6d8898f68f923aaf08b7306d931fd9d76`).
Re-read cited sources when changing behavior.

[FACT] The embedded `opencode` profile requires `opencode` and `github`, launches
`opencode --auto`, and uses `opencode-ai` for npm versions/update commands.
`--auto` approves requests not explicitly denied by OpenCode configuration;
OpenShell supplies confinement. The existing tab icon is `OC`.
[1](#source-1) [2](#source-2) [3](#source-3) [4](#source-4)

[FACT] The feature installs ripgrep and `opencode-ai@latest` at the managed npm
prefix. Upstream postinstall selects the platform/CPU package and copies or
hardlinks its native binary to `opencode-ai/bin/opencode.exe`, including Linux.
Consequently `network.opencode` attributes egress to that executable, not Node.
The compiled Bun binary can also execute JavaScript with `BUN_BE_BUN=1`: this
is a process-path grant, not an assertion that only trusted agent code can use it.
[5](#source-5) [6](#source-6) [7](#source-7) [8](#source-8)

[FACT] The capability grants HTTPS to `registry.npmjs.org`, `opencode.ai`, `api.anthropic.com`,
`api.openai.com`, `auth.openai.com`, `chatgpt.com`,
`generativelanguage.googleapis.com` and `openrouter.ai`. This covers Zen and
common provider APIs, ChatGPT OAuth and npm plugins; it is not arbitrary
web egress. Custom registries/providers require explicit capabilities. Shell PTYs
remain opt-in. Policy goldens cover isolated, GitHub and read-only compositions.
[7](#source-7) [8](#source-8) [9](#source-9) [10](#source-10)

[FACT] The account's OpenCode leaf binds at
`/home/sandbox/.local/share/opencode`. XDG data retains that conventional path;
launch overrides place config, cache, state and npm's download cache beneath that leaf. Config
is `config/opencode/opencode.json` there. Credentials (`auth.json`), SQLite
sessions and agent settings therefore use the account mount. Temporary files
under `/tmp/opencode` are disposable. Import copies only the entire credential
file, including all stored providers; it does not copy host configuration.
[1](#source-1) [11](#source-11) [12](#source-12) [13](#source-13) [23](#source-23)

[FACT] Optional `auth.import_home_dir` chooses the host directory relative to
home; absent it, import retains `~/<basename(state_container_path)>`. Preferred
`import_files` still precede `credential_file`, with duplicates removed.
OpenCode selects `.local/share/opencode`; custom host XDG roots require `--file`.
Validation rejects absolute/escaping directories. Active consumers prevent
import; status establishes native-file presence, not provider acceptance.
[2](#source-2) [13](#source-13) [14](#source-14)

[FACT] Declared account secrets are `OPENCODE_API_KEY`, `ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`, `GOOGLE_GENERATIVE_AI_API_KEY` and `OPENROUTER_API_KEY`.
They use supervisor environment delivery, never launch argv. Changed secrets
need box recreation. ChatGPT offers a headless device method through
`opencode auth login --provider openai --method 'ChatGPT Pro/Plus (headless)'`;
`yoloctl box create opencode-login --agent opencode --account NAME --no-attach`
prepares a named box; `box exec opencode-login -- opencode auth login ...` runs
that explicit login and writes native state. Exec forwards the supplied argv;
managed-session profile environment is not implicitly added.
[1](#source-1) [9](#source-9) [15](#source-15) [16](#source-16)

[FACT] Managed launch disables self-updates, model-catalog fetching and automatic
LSP downloads, configures sharing disabled, and exempts loopback from proxies.
The bundled model catalog remains available and updates with the CLI. Embedded
auth plugins stay enabled. Operator guidance is discovered through the existing
`~/.agents/skills` mount. These settings do not grant more network destinations.
[1](#source-1) [17](#source-17) [18](#source-18) [19](#source-19) [21](#source-21) [22](#source-22)

[FACT] Upstream installs `@opencode-ai/plugin` in configuration directories and
resolves configured npm plugins through bundled Arborist in the native process.
The registry grant permits encoded slashes used by scoped package URLs. npm
lifecycle scripts remain disabled by upstream's `ignoreScripts: true`; loaded
plugin hooks run within the agent's existing authority. `NPM_CONFIG_CACHE` keeps
downloads in the account leaf. The fixture requires SDK installation and a local
plugin hook. [1](#source-1) [20](#source-20) [24](#source-24) [25](#source-25) [26](#source-26) [27](#source-27)

[FACT] `smoke-opencode` tests installation, attributed HTTPS, secret replacement,
import refusal and native state across box/VM replacement. Its local provider
fixture runs the real CLI, checks plugin loading, a shell tool without new PTYs, operator skill
discovery and saved-session resumption. Optional OpenAI credentials enable a real
provider request. These definitions do not establish live image/VM or OAuth
validation; first login, token refresh and interactive terminal behavior require
separate checks. [8](#source-8) [20](#source-20)

## Sources

1. <a id="source-1"></a> [Embedded profile](../../internal/agentprofile/builtin/opencode.yaml#L1) — launch, state and secrets.
2. <a id="source-2"></a> [Adapter tests](../../cmd/yoloctl/opencode_test.go#L16) — launch, install, import and completion.
3. <a id="source-3"></a> [Upstream TUI CLI](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/opencode/src/cli/cmd/tui.ts#L108) — auto approval.
4. <a id="source-4"></a> [Icon table](../../internal/config/tui.go#L144) — OpenCode defaults.
5. <a id="source-5"></a> [Feature recipe](../../internal/features/opencode.go#L3) — installation.
6. <a id="source-6"></a> [Upstream installer](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/opencode/script/postinstall.mjs#L23) — native selection and target.
7. <a id="source-7"></a> [Capabilities](../../internal/capabilities/capabilities.go#L197) — network.opencode.
8. <a id="source-8"></a> [Isolated smoke](../../scripts/smoke/opencode.sh#L1) — binary/runtime/persistence checks.
9. <a id="source-9"></a> [Upstream ChatGPT auth](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/opencode/src/plugin/openai/codex.ts#L471) — headless device flow and endpoints.
10. <a id="source-10"></a> [Policy tests](../../internal/agentbox/policy_golden_test.go#L50) — compositions and attribution.
11. <a id="source-11"></a> [Upstream XDG paths](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/core/src/global.ts#L10) — persistent and temporary directories.
12. <a id="source-12"></a> [Upstream auth storage](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/opencode/src/auth/index.ts#L10) — credential file.
13. <a id="source-13"></a> [Import implementation](../../cmd/yoloctl/account_auth.go#L162) — defaultHostAuthPaths and import.
14. <a id="source-14"></a> [Profile validation](../../internal/agentprofile/profile.go#L527) — validateAuth.
15. <a id="source-15"></a> [Secret projection tests](../../internal/agentbox/secret_injection_test.go#L12) — declared keys and argv/label isolation.
16. <a id="source-16"></a> [Confined exec](../../cmd/yoloctl/run.go#L95) — explicit argv through OpenShell SSH.
17. <a id="source-17"></a> [Upstream catalog](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/core/src/models-dev.ts#L217) — snapshot and fetch control.
18. <a id="source-18"></a> [Upstream skills](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/opencode/src/skill/index.ts#L21) — external skills.
19. <a id="source-19"></a> [Upstream config](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/opencode/src/config/config.ts#L482) — inline config merge.
20. <a id="source-20"></a> [Local provider fixture](../../scripts/smoke/opencode-runtime.mjs#L1) — real CLI/tool/session checks without real keys.
21. <a id="source-21"></a> [Upstream runtime flags](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/opencode/src/effect/runtime-flags.ts#L16) — default plugins and LSP control.
22. <a id="source-22"></a> [Upstream flag parsing](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/core/src/flag/flag.ts#L15) — update and model-fetch flags.
23. <a id="source-23"></a> [Upstream database path](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/core/src/database/database.ts#L53) — SQLite under data.
24. <a id="source-24"></a> [Upstream dependency bootstrap](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/opencode/src/config/config.ts#L452) — background plugin-SDK installation.
25. <a id="source-25"></a> [Upstream npm service](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/core/src/npm.ts#L80) — Arborist, script suppression, package resolution and dependency installation.
26. <a id="source-26"></a> [Endpoint compiler](../../internal/agentbox/compose.go#L352) — REST configuration and encoded slash permission.
27. <a id="source-27"></a> [Upstream npm configuration](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/core/src/npm-config.ts#L13) — environment and npm settings.
