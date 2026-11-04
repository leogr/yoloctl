# Pi CLI adapter

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; upstream Pi `v1.0.3` (`d78dc83d633229d12f8b79631384c4c2717c399f`). Re-read cited sources when changing behavior.

[FACT] The embedded `pi` profile uses the Ubuntu base, requires `pi` and
`github`, and launches the Node CLI `pi`. The existing TUI icon is `π`.
Pi provides no per-tool approval sandbox; OpenShell confines its process and
children. Project trust remains Pi's own resource-loading decision.
[1](#source-1) [2](#source-2) [9](#source-9) [17](#source-17)

[FACT] The feature installs `@earendil-works/pi-coding-agent@latest` into
`/home/sandbox/.npm-global`, with Ubuntu's `ripgrep` and `fd-find`. Pi accepts
`fdfind` as its system fd executable. An image check rejects Node older than
22.19 with a base-rebuild hint. Installation writes no native account settings;
the composed builder restores npm-prefix ownership to sandbox. Existing npm
metadata/version/update paths handle Pi without an agent-name branch.
[3](#source-3) [4](#source-4) [10](#source-10) [15](#source-15)

[FACT] Managed launches set `PI_CODING_AGENT_DIR={{state_dir}}/agent`,
`PI_OFFLINE=1` and `PI_TELEMETRY=0`. These nonsecret values cross the SSH
environment reset through the launch-command prefix. Upstream offline mode
suppresses automatic catalog refresh, missing-tool downloads, package fetching
and version checks; it does not disable explicit model requests. Telemetry is
disabled independently. Bundled/cached models and installed resources remain
available. Direct `box exec ... -- pi` needs the corresponding explicit `env`
prefix to get these managed startup settings. [1](#source-1) [4](#source-4)
[11](#source-11) [18](#source-18) [19](#source-19)

[FACT] `network.pi` permits `/usr/bin/node` on port 443 to seven literal hosts:
`api.anthropic.com`, `platform.claude.com`, `api.openai.com`, `auth.openai.com`,
`chatgpt.com`, `generativelanguage.googleapis.com` and `openrouter.ai`.
They cover Anthropic API/OAuth exchange, OpenAI API/ChatGPT login and the legacy
`openai-codex` backend, Gemini API, and OpenRouter. Browser navigation occurs
outside the box. Other providers, arbitrary endpoints, catalog/telemetry hosts
and npm downloads receive no Pi grant. Attribution is to Node, including other
Node programs, not to one JavaScript package. [5](#source-5) [12](#source-12) [20](#source-20)

[FACT] The account's `pi` leaf binds at `/home/sandbox/.pi`, persisting
`agent/auth.json`, settings, trust decisions, installed resources and sessions.
Import defaults to `~/.pi/agent/auth.json` and copies the entire credential file
once, including every stored provider, but no settings/history. It refuses active
consumers. Native status establishes presence, not provider acceptance.
[1](#source-1) [4](#source-4) [13](#source-13)

[FACT] The profile projects declared account secrets `ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`, `GEMINI_API_KEY` and `OPENROUTER_API_KEY` at container creation.
Pi prefers stored credentials over environment keys. Switching a provider to
environment auth can therefore require `/logout`; changed account secrets need
box recreation. Interactive `/login` stores credentials; remote OAuth flows
offer a pasted redirect URL/code. Source support does not establish subscription
eligibility or a successful live login. [1](#source-1) [6](#source-6)
[12](#source-12) [13](#source-13) [20](#source-20) [21](#source-21)

[FACT] Pi discovers the existing operator mount under `~/.agents/skills` as a
user resource. No additional mount or launcher is needed. Its bash tool spawns
children with pipe stdio; the adapter adds no PTY capability.
[9](#source-9) [14](#source-14)

[FACT] Tests cover launch/state/import, declared secret isolation, latest installs,
completion and exact policy goldens. The isolated smoke checks installation,
endpoint policy, secret replacement, active-import refusal and persistence
through box/VM replacement. Its local model fixture drives the actual CLI's
bash tool, checks the operator skill listing in model context and saves/resumes Pi sessions.
That fixture proves no real-provider acceptance. Optional OpenAI credentials
enable a real API/tool request; OAuth, token refresh and terminal interactions
require separate live validation. [4](#source-4) [6](#source-6) [7](#source-7)
[8](#source-8) [16](#source-16)

[FACT] Provider-root probes report HTTP status and require an attributed Node
allowance for every host. Unauthenticated HTTP 2xx–4xx (except proxy-auth 407)
establishes reachability, not API/auth success; 5xx and transport errors fail.
Unauthorized curl still requires an attributed denial. [7](#source-7)
[22](#source-22) [23](#source-23)

## Sources

1. <a id="source-1"></a> [Pi profile](../../internal/agentprofile/builtin/pi.yaml#L1) — adapter declarations.
2. <a id="source-2"></a> [TUI icon](../../internal/config/tui.go#L134) — `DefaultAgentIcons`.
3. <a id="source-3"></a> [Installer](../../internal/features/pi.go#L3) — latest npm and search-tool recipe.
4. <a id="source-4"></a> [CLI regressions](../../cmd/yoloctl/pi_test.go#L16) — launch, maintenance, import, completion.
5. <a id="source-5"></a> [Capability](../../internal/capabilities/capabilities.go#L214).
6. <a id="source-6"></a> [Secret projection tests](../../internal/agentbox/secret_injection_test.go#L12).
7. <a id="source-7"></a> [Pi smoke](../../scripts/smoke/pi.sh#L1).
8. <a id="source-8"></a> [Local runtime fixture](../../scripts/smoke/pi-runtime.mjs#L1).
9. <a id="source-9"></a> [Pi v1.0.3 bash](https://github.com/earendil-works/pi/blob/v1.0.3/packages/coding-agent/src/core/tools/bash.ts#L105).
10. <a id="source-10"></a> [Pi v1.0.3 tool discovery](https://github.com/earendil-works/pi/blob/v1.0.3/packages/coding-agent/src/utils/tools-manager.ts#L30).
11. <a id="source-11"></a> [Pi v1.0.3 environment](https://github.com/earendil-works/pi/blob/v1.0.3/packages/coding-agent/docs/environment-variables.md#L79).
12. <a id="source-12"></a> [Pi v1.0.3 providers](https://github.com/earendil-works/pi/tree/v1.0.3/packages/ai/src/providers) — Anthropic, OpenAI, legacy Codex, Google and OpenRouter modules.
13. <a id="source-13"></a> [Pi v1.0.3 state paths](https://github.com/earendil-works/pi/blob/v1.0.3/packages/coding-agent/src/config.ts#L605).
14. <a id="source-14"></a> [Pi v1.0.3 skill discovery](https://github.com/earendil-works/pi/blob/v1.0.3/packages/coding-agent/src/core/package-manager.ts#L2549).
15. <a id="source-15"></a> [Composed builder](../../internal/agentboximage/composed.go#L133) — identity and npm ownership.
16. <a id="source-16"></a> [Pi golden](../../internal/agentbox/testdata/policy_golden/pi.yaml#L1).
17. <a id="source-17"></a> [Pi v1.0.3 CLI options](https://github.com/earendil-works/pi/blob/v1.0.3/packages/coding-agent/src/cli/args.ts#L290).
18. <a id="source-18"></a> [Pi v1.0.3 startup](https://github.com/earendil-works/pi/blob/v1.0.3/packages/coding-agent/src/main.ts#L573).
19. <a id="source-19"></a> [Pi v1.0.3 telemetry](https://github.com/earendil-works/pi/blob/v1.0.3/packages/coding-agent/src/core/telemetry.ts#L8).
20. <a id="source-20"></a> [Pi v1.0.3 OAuth](https://github.com/earendil-works/pi/tree/v1.0.3/packages/ai/src/auth/oauth) — provider exchange/refresh endpoints.
21. <a id="source-21"></a> [Pi v1.0.3 credential precedence](https://github.com/earendil-works/pi/blob/v1.0.3/packages/coding-agent/docs/models.md#L25).
22. <a id="source-22"></a> [HTTPS checks](../../scripts/smoke/common.sh#L18) — `smoke_check_https_status`, `smoke_require_allowed_egress`.
23. <a id="source-23"></a> [Probe regressions](../../scripts/test_vm_smoke.py#L82) — status handling and attributed allowances.
