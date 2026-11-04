# Antigravity CLI adapter

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; reporter `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes;
official Linux amd64 release `1.2.17`, inspected 2026-10-05. Re-read cited
sources when changing behavior.

[FACT] The `agy` profile requires `agy`, `github` and its status line
reporter `yolo.agent.observe-agy` ([agy reporter](agy-observation.md)), uses the Ubuntu base,
and launches `agy --dangerously-skip-permissions --sandbox=false` with
`AGY_CLI_DISABLE_AUTO_UPDATE=true`. OpenShell supplies confinement. The native
CLI's help accepts those flags; a confined startup logged that `true` disables
updates, while `1` still spawned an updater. The icon is `⋏` (U+22CF).
[1](#source-1) [2](#source-2) [8](#source-8)

[FACT] `native_release: antigravity` selects a reviewed distribution, mutually
exclusive with `npm_package`. Copied profiles retain it without agent-name
dispatch. Image builds and runtime updates use the same installer, querying
the official Linux amd64/arm64 manifest. It validates the artifact location
and SHA512, copies only the regular `antigravity` archive member, and atomically
replaces `/home/sandbox/.local/share/yoloctl/agy/agy`. Metadata records the
installed version; status never executes an agent through outer `nerdctl exec`.
Failed downloads/checks preserve the previous installation. Versions, status,
explicit updates and lifecycle autoupdate support both npm and native sources.
The native executable itself never runs during outer maintenance.
The installer completed in a disposable Linux amd64 box; its binary was
sandbox-owned and reported the recorded 1.2.17 version. That download used
explicit unconfined networking and does not validate the agent allow-list.
[3](#source-3) [4](#source-4) [9](#source-9) [14](#source-14) [15](#source-15)

[FACT] The account's `agy` leaf mounts at `/home/sandbox/.gemini`, separately
from its Gemini leaf. It includes `antigravity-cli/` and shared `config/` state.
Native import copies `antigravity-cli/antigravity-oauth-token` once, refuses
active consumers, and does not copy settings or export host keyrings. A
confined 1.2.17 startup selected file storage without D-Bus; another process
read a synthetic saved consumer token and attempted Code Assist eligibility.
A disposable PTY probe reached the authorization-code prompt without SSH
environment overrides. These probes do not establish a real Google login.
[1](#source-1) [5](#source-5) [8](#source-8)

[FACT] Upstream documents remote URL/code OAuth. Optional API-key mode needs
both the injected account secret `GEMINI_API_KEY` and `modelProvider: "gemini"`
in `~/.gemini/antigravity-cli/settings.json`. A key alone does not select it.
Google documents configuration migration from Gemini CLI to Antigravity.
[1](#source-1) [10](#source-10) [11](#source-11)

[FACT] The image's `agy` launcher atomically seeds a missing settings file with
`enableTelemetry: false` and `enableTerminalSandbox: false`, plus the
reporter's `statusLine` when its launcher is installed; existing settings
are preserved. With the operator helper mounted, it links read-only guidance
into the documented global skills directory. The parent remains sandbox-owned;
the launcher does not create root-owned directories inside account state.
A stale link after operator revocation supplies neither the helper nor authority.
[6](#source-6) [12](#source-12) [16](#source-16)

[FACT] In isolated-home synthetic-auth probes, native 1.2.17 rewrote these
defaults to `{}`. It removed `enableTelemetry` for both true and false inputs,
retained a true `enableTerminalSandbox`, and omitted a false one. Its startup
log reported consulting server telemetry settings and defaulting to disabled
after authentication failed. Seeding the JSON is not proof of a persistent or
effective telemetry opt-out. The smoke checks the installed launcher's defaults
before native execution, without requiring those keys to survive an agent save.
[8](#source-8) [13](#source-13)

[FACT] `network.agy` attributes port-443 grants only to the native executable:
daily/standard Code Assist, Gemini API, OAuth token exchange, Google userinfo
and `antigravity-unleash.goog` feature configuration. It grants no Node/curl,
package-download or Google-wide wildcard access. Confined 1.2.17 probes observed
the same executable running its language server and requesting daily Code Assist,
userinfo and feature configuration. Its language server also attempted
`play.googleapis.com/log` with the seeded false preference; that host remains
denied. PTY allocation remains opt-in. [7](#source-7) [8](#source-8)

[FACT] Regressions cover launch/state, nested import, secret isolation,
distribution validation, rejected archive preservation, settings preservation,
policy goldens and icon width. The isolated smoke adds image/update/policy,
operator guidance and persistence checks; `SMOKE_AGY_AUTH_FILE` enables real
OAuth and shell tools across two processes. The default synthetic smoke does
not establish completed personal login, token refresh, model/tool compatibility
or interactive terminal behavior; those require separate live validation. [5](#source-5)
[9](#source-9) [13](#source-13)

## Sources

1. <a id="source-1"></a> [agy profile](../../internal/agentprofile/builtin/agy.yaml#L1) — launch/auth/state declarations.
2. <a id="source-2"></a> [TUI defaults](../../internal/config/tui.go#L136) — `DefaultAgentIcons` and YAML template.
3. <a id="source-3"></a> [Native distribution](../../internal/nativerelease/release.go#L1) — reviewed source, verification and replacement.
4. <a id="source-4"></a> [Distribution dispatch](../../cmd/yoloctl/agent_distribution.go#L1) — latest, installed version and maintenance.
5. <a id="source-5"></a> [Adapter regressions](../../cmd/yoloctl/agy_test.go#L16) — launch, account leaf, nested import and completion.
6. <a id="source-6"></a> [Launcher](../../internal/features/agy-launch.py#L1) — defaults and operator link.
7. <a id="source-7"></a> [Policy golden](../../internal/agentbox/testdata/policy_golden/agy.yaml#L1) — complete compiled grants.
8. <a id="source-8"></a> [Official amd64 manifest](https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_amd64.json) — release 1.2.17 archive SHA512 beginning `d0ebe612f7cfc21c`; confined help/startup and synthetic-token probes against that artifact.
9. <a id="source-9"></a> [Installer regressions](../../internal/nativerelease/install_test.py#L1) — corrupt hash, symlink member and foreign URL preservation.
10. <a id="source-10"></a> [Official installation/auth guide](https://www.antigravity.google/docs/cli/install/) — remote login and API-key selection, accessed 2026-10-05.
11. <a id="source-11"></a> [Official Gemini migration guide](https://www.antigravity.google/docs/cli/gcli-migration/) — configuration migration, accessed 2026-10-05.
12. <a id="source-12"></a> [Official skills guide](https://www.antigravity.google/docs/skills/) — global `~/.gemini/antigravity-cli/skills`, accessed 2026-10-05.
13. <a id="source-13"></a> [Isolated smoke](../../scripts/smoke/agy.sh#L1) — default synthetic checks and optional real personal OAuth/tool gate.

14. <a id="source-14"></a> [Native installer](../../internal/nativerelease/install.py#L1) — verified download and atomic replacement.
15. <a id="source-15"></a> [Lifecycle autoupdate](../../cmd/yoloctl/autoupdate.go#L17) — distribution dispatch.
16. <a id="source-16"></a> [Launcher regression](../../internal/features/agy_test.go#L8) — seed values at the exec boundary and preservation of existing settings.
