# Gemini CLI adapter

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; upstream behavior inspected at Gemini CLI `v0.62.0`. Re-read cited sources when changing behavior.

[FACT] The embedded `gemini` profile selects the Ubuntu base, requires the
`gemini` and `github` features, and launches `gemini --approval-mode yolo`.
Its nonsecret launch environment sets `NO_BROWSER=true` and
`GEMINI_SANDBOX=false`. OpenShell remains the workload confinement boundary.
The npm package metadata also selects Gemini for the existing versions/update
commands. [1](#source-1) [2](#source-2)

[FACT] The Gemini feature installs `@google/gemini-cli@latest` with the explicit
`/home/sandbox/.npm-global` prefix. Its Dockerfile recipe writes image-owned
`/etc/gemini-cli/settings.json`, disabling both self-updates and update
notifications, plus `system-defaults.json`, disabling usage statistics and
PTY-backed shell execution by default. It does not rewrite account-owned settings.
[3](#source-3) [11](#source-11)

[FACT] Upstream usage statistics default on. Clearcut posts to the ungranted
`play.googleapis.com`, with event-driven flushes after a 60-second threshold,
additional explicit flushes, and at most 100 failed events requeued per batch.
Disabling `privacy.usageStatisticsEnabled` prevents logger creation. This is
an overridable default; enabling it does not grant egress.
[14](#source-14) [15](#source-15) [4](#source-4)

[FACT] Upstream v0.62.0 loads system defaults below user/workspace settings and
system settings above them. Consequently users can set
`tools.shell.enableInteractiveShell=true` after explicitly granting
`fs.pty.allocate`. The Gemini feature itself grants no terminal devices.
The managed session still inherits its supervisor-allocated PTY.
[3](#source-3) [4](#source-4) [5](#source-5)

[FACT] `network.gemini` grants only Node (`/usr/bin/node`) port 443 access to
`generativelanguage.googleapis.com`, `cloudcode-pa.googleapis.com`,
`oauth2.googleapis.com` and `www.googleapis.com`. It grants neither a Google
wildcard nor npm registry access. API, OAuth/Code Assist and user-info traffic
motivate these endpoints; live provider acceptance remains a runtime check.
Vertex AI/ADC and arbitrary web-fetch destinations are outside this built-in
grant. [5](#source-5) [6](#source-6) [7](#source-7) [12](#source-12)

[FACT] The account's Gemini leaf mounts at `/home/sandbox/.gemini`; the adapter
declares native `oauth_creds.json` and environment injection of the account's
`GEMINI_API_KEY` secret. Import copies only the credential file once, defaults
to `~/.gemini/oauth_creds.json`, and refuses active account consumers. Native
status means file presence, not provider validity or API-key configuration.
Secret replacement requires explicit box recreation. [1](#source-1)
[2](#source-2) [8](#source-8) [13](#source-13)

[FACT] Upstream v0.62.0's browser-suppressed interactive Google login presents
a URL and requests an authorization code. Its default OAuth path uses the native
credential file; optional encrypted/keychain storage is a separate path that
yoloctl's single-file import does not export. Import also does not copy the
host's auth-method selection or other settings. Select Google login once in
Gemini after import; a fresh headless invocation with no saved selection needs
`GOOGLE_GENAI_USE_GCA=true`. The env helper checks that exact value before
API-key inference; saved auth selection takes precedence. Interactive users
choose their auth method on first launch. [6](#source-6) [9](#source-9)
[16](#source-16)

[FACT] After importing, prepare a named Gemini box with `box create NAME
--agent gemini --no-attach`, then run `box exec NAME -- env
GOOGLE_GENAI_USE_GCA=true GEMINI_SANDBOX=false NO_BROWSER=true gemini
--approval-mode yolo --prompt '...'`. The explicit prefix carries auth selection
and nonsecret settings through SSH. Box `--env` alone sets container variables,
which OpenShell's SSH child environment clears; the supervisor injection map
contains only declared account secrets.
[17](#source-17) [18](#source-18) [19](#source-19) [20](#source-20)

[FACT] Regression tests cover managed launch, native mount resolution, image
composition/update selection, credential-only import, secret isolation,
completion and policy goldens. The isolated Gemini smoke exercises installation,
endpoint policy, secret replacement and synthetic native-state persistence
through box/VM replacement. `SMOKE_GEMINI_API_KEY` opts into an actual
headless API/shell-tool request. These test definitions do not establish a live
pass; Google login and interactive terminal behavior require separate checks.
[2](#source-2) [5](#source-5) [10](#source-10) [13](#source-13)

[FACT] Google documents configuration migration from Gemini CLI to Antigravity;
that guide does not establish account eligibility. The Gemini adapter remains
available with its own launch and authentication conventions.
[1](#source-1)
[21](#source-21)

## Sources

1. <a id="source-1"></a> [internal/agentprofile/builtin/gemini.yaml](../../internal/agentprofile/builtin/gemini.yaml#L1) — embedded adapter contract.
2. <a id="source-2"></a> [cmd/yoloctl/gemini_test.go](../../cmd/yoloctl/gemini_test.go#L16) — launch, update, import and completion regressions.
3. <a id="source-3"></a> [internal/features/gemini.go](../../internal/features/gemini.go#L3) — npm installation and system settings.
4. <a id="source-4"></a> [Gemini v0.62.0 settings](https://github.com/google-gemini/gemini-cli/blob/v0.62.0/packages/cli/src/config/settings.ts#L255) — merge precedence.
5. <a id="source-5"></a> [internal/agentbox/policy_golden_test.go](../../internal/agentbox/policy_golden_test.go#L50) — Gemini policy cases and binary attribution.
6. <a id="source-6"></a> [Gemini v0.62.0 OAuth](https://github.com/google-gemini/gemini-cli/blob/v0.62.0/packages/core/src/code_assist/oauth2.ts#L260) — manual login, credential storage and user-info endpoint.
7. <a id="source-7"></a> [Gemini v0.62.0 Code Assist](https://github.com/google-gemini/gemini-cli/blob/v0.62.0/packages/core/src/code_assist/server.ts#L73) — service endpoint.
8. <a id="source-8"></a> [cmd/yoloctl/account_auth.go](../../cmd/yoloctl/account_auth.go#L132) — host paths, import and presence status.
9. <a id="source-9"></a> [Gemini v0.62.0 headless auth](https://github.com/google-gemini/gemini-cli/blob/v0.62.0/packages/cli/src/validateNonInterActiveAuth.ts#L20) — configured/environment auth selection.
10. <a id="source-10"></a> [scripts/smoke/gemini.sh](../../scripts/smoke/gemini.sh#L1) — isolated runtime and opt-in provider checks.

11. <a id="source-11"></a> [internal/features/gemini_test.go](../../internal/features/gemini_test.go#L11) — image settings regression.
12. <a id="source-12"></a> [Gemini policy golden](../../internal/agentbox/testdata/policy_golden/gemini.yaml#L1) — complete compiled policy.
13. <a id="source-13"></a> [internal/agentbox/secret_injection_test.go](../../internal/agentbox/secret_injection_test.go#L12) — secret isolation and create-time projection.
14. <a id="source-14"></a> [Gemini v0.62.0 settings schema](https://github.com/google-gemini/gemini-cli/blob/v0.62.0/packages/cli/src/config/settingsSchema.ts#L970) — usage-statistics default.
15. <a id="source-15"></a> [Gemini v0.62.0 Clearcut logger](https://github.com/google-gemini/gemini-cli/blob/v0.62.0/packages/core/src/telemetry/clearcut-logger/clearcut-logger.ts#L247) — creation gate, endpoint, flushes and retries.
16. <a id="source-16"></a> [Gemini v0.62.0 env auth](https://github.com/google-gemini/gemini-cli/blob/v0.62.0/packages/core/src/core/contentGenerator.ts#L80) — `getAuthTypeFromEnv`.
17. <a id="source-17"></a> [cmd/yoloctl/run.go](../../cmd/yoloctl/run.go#L95) — explicit confined exec command.
18. <a id="source-18"></a> [internal/agentbox/live.go](../../internal/agentbox/live.go#L424) — container environment rendering.
19. <a id="source-19"></a> [OpenShell v0.0.72 SSH](https://github.com/NVIDIA/OpenShell/blob/v0.0.72/crates/openshell-supervisor-process/src/ssh.rs#L690) — `apply_child_env` clears inherited variables.
20. <a id="source-20"></a> [internal/agentbox/secret_injection.go](../../internal/agentbox/secret_injection.go#L44) — declared account secrets only.

21. <a id="source-21"></a> [Official Gemini migration guide](https://www.antigravity.google/docs/cli/gcli-migration/) — configuration migration, accessed 2026-10-05.
