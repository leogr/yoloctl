# Policy boundaries and image composition

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; static composition coverage `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] Effective features combine box selections and agent requirements;
effective capabilities combine box grants and feature requirements. Features
install software and/or grant policy; agents do not declare capabilities.
Composition validates catalogs and policy. [1](#source-1) [2](#source-2)

[FACT] Policy selects sandbox user/group, best-effort Landlock, capability-based
workdir access and scoped network grants. It rejects overlapping writable paths,
known `/dev` symlinks and invalid host patterns. PTY access grants `/dev/pts` and
`/dev/tty`, never `/dev/ptmx`. Each declared extra mount gets Landlock access
matching its bind (`read_only` for `:ro`, otherwise `read_write`) unless an
equal or broader entry already covers it, such as the account-state slot under
`/home/sandbox`. Tests include byte-exact policy goldens.
[1](#source-1) [3](#source-3) [11](#source-11) [13](#source-13) [39](#source-39)

[FACT] Static tests compare explicit agentless GitHub/npm grants with existing
goldens and filesystem policy across network modes, and check that a read-only
extra mount receives only a read-only entry. These checks establish computed
policy, not live enforcement. [40](#source-40)

[FACT] A network egress grant can opt into `allow_encoded_slash` for scoped npm
URLs. The compiler emits a REST endpoint with enforced full HTTP access and the
same host/port/binary scope. Copy preserves the opt-in per endpoint; show exposes
it in text/JSON. OpenShell v0.0.72 requires a protocol to read this flag and expands
the access preset before evaluation. Other endpoints retain strict parsing.
[21](#source-21) [22](#source-22) [23](#source-23) [24](#source-24) [25](#source-25)

[FACT] The outer container starts with elevated setup capabilities and
unconfined OCI seccomp/AppArmor settings; OpenShell establishes the inner
sandbox. `unconfined_network` selects process mode, removing inner netns/proxy
routing while retaining the process sandbox launch. Runtime commands must use
the sandbox endpoint, not substitute an outer `nerdctl exec` process.
[4](#source-4) [12](#source-12)

[FACT] When identities differ, the entrypoint aligns the named sandbox UID/GID
with the host developer and adjusts home ownership. It never chowns the workdir.
Nonunique GIDs retain existing groups (including macOS GID 20); policy still
names `sandbox`. GID zero is rejected before mutation. Tests cover rejection,
collisions, remapping and repeated startup. This changes numeric identity, not
user namespaces; home remains separate from workdir. [5](#source-5) [14](#source-14)

[FACT] Composition prepares available sandbox identities before installation,
using the space owner's UID/GID without account/workdir mounts. npm owns packages
in its install layer. Final home repair changes only mismatches without following
symlinks. Matching startups skip remapping; the entrypoint handles other identities.
References hash builder version, base, recipe and identity; policy-only features
are hash-neutral. Cached images are reused; forced builds refresh feature layers
only. Custom bases retain normal runtime handling. [6](#source-6) [7](#source-7)
[14](#source-14) [26](#source-26)

[FACT] Managed-base refresh and builtin/npm composition use a fresh build ARG
to rerun installers while retaining downloads and stable image references.
Custom bases/raw Dockerfile features retain `--no-cache`; BuildKit v0.31.2
resets cache mounts under that flag. [6](#source-6) [7](#source-7) [32](#source-32)

[FACT] npm uses a locked download cache outside image layers, disposable under
GC. `--prefer-online` revalidates metadata; agent features retain `latest`.
[28](#source-28) [29](#source-29)

[FACT] Explicit base apt installs use a locked archive cache outside image
layers and Ubuntu's cleanup path. Indexes are refreshed normally; downloads
allow three retries. Ubuntu/PPA updates use `--error-on=any`, and PPA registration
skips its implicit update. Unavailable indexes stop installation.
[30](#source-30) [31](#source-31)

[FACT] Runtime updates target latest with explicit `/home/sandbox/.npm-global`
and a separate maintenance cache. Failed version probes skip with notice;
failed installs return errors. Outer `nerdctl exec --user sandbox` is a
package-maintenance exception, never an agent launch path.
[2](#source-2) [8](#source-8) [17](#source-17)

[FACT] Image/Pi/AIRC smokes bypass the entrypoint to check baked ownership.
The image smoke asserts a small final ownership layer, apt/npm marker retention
across base refresh/composed rebuild but absence from image layers, and isolated maintenance
despite a poisoned default npm cache. Numeric ownership handles duplicate GID 20.
[16](#source-16) [27](#source-27)

[FACT] Infrastructure pins are separate from agent `latest` installs. Changes
require matching policy/install/runtime validation. [3](#source-3) [9](#source-9) [10](#source-10)

[FACT] The optional operator feature installs the same CLI frontend and requires
its scoped-controller capability. Endpoint/helper/skill mounts are infrastructure
added by provisioning and excluded from ordinary extra-mount drift. Changing the
effective operator grant requires recreation of that infrastructure.
[15](#source-15)

[FACT] agy's `native_release` shares a SHA512-verifying installer between image
and runtime updates. Outer version checks read metadata, never execute the CLI.
[18](#source-18)

[FACT] Pi installs its latest npm package plus image-provided ripgrep and fd-find;
its profile suppresses automatic runtime downloads. Its provider policy scopes
seven literal hosts to Node and adds no PTY grant. [19](#source-19)

[FACT] OpenCode installs its native executable through npm latest, alongside
ripgrep. Its own capability grants provider and npm registry egress only to that executable;
managed launch disables automatic LSP downloads and uses the bundled model catalog. [20](#source-20)

[FACT] Explicit OCI Features extend the installation plan using persisted digest
locks and typed options; they add no capability grants. Their pinned artifacts
and option values participate in composed-image identity. [33](#source-33)

[FACT] Codex's feature installs distro `bubblewrap` alongside npm `@latest`,
using the apt archive and npm download caches. Its managed profile disables
Codex's nested sandbox; OpenShell remains the confinement boundary. The image
smoke checks `bwrap` on the confined PATH, not nested sandbox operation.
[34](#source-34) [35](#source-35) [36](#source-36) [16](#source-16)

[FACT] Codex's OpenAI HTTPS grant names Node, npm-vendored Linux x64/arm64
Codex and its detached daemon. Daemon paths allow one version/target directory
under `app-server-daemon/releases`, ending in `bin/codex`; updates need no
version pin. OpenAI/ChatGPT hosts remain scoped to port 443. Unrelated binaries with no allowed ancestor, spoofed argv and unrelated
hosts gain no grant. Existing ancestor matching remains unchanged. [37](#source-37) [38](#source-38)

## Sources

1. <a id="source-1"></a> [internal/agentbox/compose.go](../../internal/agentbox/compose.go#L40-L226) — `ComposePolicy` through `compilePolicy`.
2. <a id="source-2"></a> [internal/features/features.go](../../internal/features/features.go#L1-L135) — `Feature`, builtin install and capability declarations.
3. <a id="source-3"></a> [internal/agentbox/compose.go](../../internal/agentbox/compose.go#L228-L331) — `lintReadWriteDevSymlink` through `lintHostPattern`.
4. <a id="source-4"></a> [internal/agentbox/live.go](../../internal/agentbox/live.go#L330) — runtime options, bind mounts and environment.
5. <a id="source-5"></a> [assets/containers/agentbox/yoloctl-entrypoint.sh](../../assets/containers/agentbox/yoloctl-entrypoint.sh#L26) — GID-zero guard and identity alignment.
6. <a id="source-6"></a> [internal/agentboximage/composed.go](../../internal/agentboximage/composed.go#L48) — prepared identity, repair, hashing and build selection.
7. <a id="source-7"></a> [internal/agentboximage/ensure.go](../../internal/agentboximage/ensure.go#L54-L105) — `EnsureImage`, `imageRefreshArgs`.
8. <a id="source-8"></a> [cmd/yoloctl/autoupdate.go](../../cmd/yoloctl/autoupdate.go#L50-L145) — `autoupdateAgentboxIfNeeded` through `npmInstallGlobalAtLatestArgs`.
9. <a id="source-9"></a> [assets/embed.go](../../assets/embed.go#L1) — embedded assets; the v0.0.72 `SandboxVersion` pins and legacy Rego embed are removed by the retirement (tree `3cc5e841`), the native runtime, its Rego and the generated qualification selection are embedded through `internal/nativeartifacts` (`embedded/selection/`, git-ignored, written by `make build`); `assets/native_openshell.go` keeps an empty default; `assets/sandbox/sandbox-policy.rego` stays only as a test fixture.
10. <a id="source-10"></a> [internal/features/features_test.go](../../internal/features/features_test.go#L187) — `TestGenerateDockerfile_AgentNpmFeaturesUseLatest`.
11. <a id="source-11"></a> [internal/capabilities/capabilities.go](../../internal/capabilities/capabilities.go#L78-L113) — `PTYAllocateCapability`, `ptyGrants`, `PTYGrants`.
12. <a id="source-12"></a> [internal/sandbox/sandbox.go](../../internal/sandbox/sandbox.go#L113-L139) — `EntrypointArgs` and process-mode selection.
13. <a id="source-13"></a> [internal/agentbox/policy_golden_test.go](../../internal/agentbox/policy_golden_test.go#L51-L143) — `policyGoldenCases`, `TestPolicyGolden`.
14. <a id="source-14"></a> [assets/entrypoint_test.go](../../assets/entrypoint_test.go#L11) — prepared-identity no-op, group collisions and rejection before mutation.
15. <a id="source-15"></a> [Operator implementation](operator.md).
16. <a id="source-16"></a> [scripts/smoke/image-ensure.sh](../../scripts/smoke/image-ensure.sh#L27) — ownership-layer measurement, forced rebuild/cache checks and maintenance.

17. <a id="source-17"></a> [Gemini adapter](gemini.md) — latest npm install with image-owned update/shell settings.

18. <a id="source-18"></a> [Antigravity adapter](agy.md) — native installation, auth and runtime contracts.

19. <a id="source-19"></a> [Pi adapter](pi.md) — install, state, provider and runtime contracts.

20. <a id="source-20"></a> [OpenCode adapter](opencode.md) — native runtime, account and validation contracts.

21. <a id="source-21"></a> [Capability authoring](../../internal/capabilities/user.go#L54) — `EgressGrant`, `translate`, `toGrants`.
22. <a id="source-22"></a> [Endpoint compiler](../../internal/agentbox/compose.go#L352) — `toNetworkPolicyFromGrant`.
23. <a id="source-23"></a> [Capability display](../../cmd/yoloctl/capability.go#L98) — text and JSON grants.
24. <a id="source-24"></a> [OpenShell L7 config](https://github.com/NVIDIA/OpenShell/blob/v0.0.72/crates/openshell-supervisor-network/src/l7/mod.rs#L164) — `parse_l7_config`, `expand_access_presets` at L1460.
25. <a id="source-25"></a> [OpenShell policy preprocessing](https://github.com/NVIDIA/OpenShell/blob/v0.0.72/crates/openshell-supervisor-network/src/opa.rs#L722) — validation and access expansion.
26. <a id="source-26"></a> [Image identity regressions](../../internal/agentboximage/composed_test.go#L48) — identity cache separation and native worker build/plan agreement.
27. <a id="source-27"></a> [Image smoke probe](../../scripts/smoke/common.sh#L46) — `smoke_check_image_identity`; no account/project mounts or agent workload.
28. <a id="source-28"></a> [npm installation](../../internal/features/features.go#L444) — shared `npmPackageInstallDockerfile`.
29. <a id="source-29"></a> [Dockerfile reference](https://docs.docker.com/reference/dockerfile/#arg) — cache mounts/GC, inherited ARGs and cache invalidation.
30. <a id="source-30"></a> [Base Dockerfile](../../assets/containers/agentbox/Dockerfile#L28) — apt cache, retries and strict index refresh.
31. <a id="source-31"></a> [Base recipe regression](../../assets/base_image_test.go#L11) — repository/index/install failure propagation.
32. <a id="source-32"></a> [BuildKit v0.31.2 bridge](https://github.com/moby/buildkit/blob/v0.31.2/solver/llbsolver/bridge.go#L137-L150) — cache mount reset before an uncached solve.
33. <a id="source-33"></a> [OCI plan](../../internal/ocifeature/plan.go#L228) — resolution and Dockerfile generation.
34. <a id="source-34"></a> [Codex installer](../../internal/features/codex.go#L3) — `codexInstallDockerfile`.
35. <a id="source-35"></a> [Codex profile](../../internal/agentprofile/builtin/codex.yaml#L14) — default args delegate confinement to OpenShell.
36. <a id="source-36"></a> [Codex install regression](../../internal/features/features_test.go#L56) — `TestCodexFeatureInstallsBubblewrap`.

37. <a id="source-37"></a> [OpenAI capability](../../internal/capabilities/capabilities.go#L162) — `network.openai` executable paths and endpoints.
38. <a id="source-38"></a> [Codex egress regressions](../../internal/agentbox/codex_egress_test.go#L19) — `TestCodexEgressGlobScope` checks composed binary paths with OPA glob.match; skips without the optional OPA tool.
39. <a id="source-39"></a> [Extra mount regressions](../../internal/agentbox/policy_golden_test.go#L376) — `TestPolicyGrantsDeclaredExtraMounts`; golden `extra_mounts`.
40. <a id="source-40"></a> [Static composition regressions](../../internal/agentbox/compose_surface_test.go#L1) — `TestComposeExplicitNetworkCapabilitiesMatchGoldens`, `TestComposeUnconfinedNetworkKeepsFilesystemPolicy`, `TestComposeExtraReadOnlyMountDesiredPolicy`; `ComposePolicy` and `compilePolicy` in compose.go L40-L226.
