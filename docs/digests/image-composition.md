# Image composition and build caches

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; apt metadata caching/repository consolidation verified on `9101409dbbb5f2febbe3bdf2f581dff7775ff43b` and accompanying changes; APT-compatible smoke probe correction on `a3827ba3b050fc0e7641586704954c4fc76f0241` and accompanying changes.

[FACT] Composition prepares available sandbox identities before installation,
using the space owner's UID/GID without account/workdir mounts. npm owns packages
in its install layer. Final home repair changes only mismatches without following
symlinks. Matching startups skip remapping; the entrypoint handles other identities.
References hash builder version, base, recipe and identity; policy-only features
are hash-neutral. Cached images are reused; forced builds refresh feature layers
only. Custom bases retain normal runtime handling. [1](#source-1) [2](#source-2)
[3](#source-3) [4](#source-4)

[FACT] Managed-base refresh and builtin/npm composition use a fresh build ARG
to rerun installers while retaining downloads and stable image references.
Custom bases/raw Dockerfile features retain `--no-cache`; BuildKit v0.31.2
resets cache mounts under that flag. [1](#source-1) [2](#source-2) [5](#source-5)

[FACT] npm uses a locked download cache outside image layers, disposable under
GC. `--prefer-online` revalidates metadata; agent features retain `latest`.
[6](#source-6) [7](#source-7)

[FACT] Base and builtin apt installers retain both signed indexes and debs in
locked BuildKit caches outside image layers. Each install still revalidates
indexes with three retries and `--error-on=any`; unavailable indexes stop it.
The base registers NodeSource directly with its signing key and deadsnakes
without implicit updates, then installs Node/Python in one transaction. This
leaves two base refreshes, avoids the NodeSource bootstrap script's extra
updates, and keeps Node 22/Python 3.13. Raw custom/OCI feature order is unchanged.
[8](#source-8) [9](#source-9) [10](#source-10) [11](#source-11)

[FACT] Image/Pi/AIRC smokes bypass the entrypoint to check baked ownership.
The image smoke asserts a small final ownership layer, apt-list/deb/npm marker retention
across base refresh/composed rebuild but absence from image layers, and isolated maintenance
despite a poisoned default npm cache. The lists marker lives under APT's
`auxfiles/`, so normal index cleanup preserves it; a local-repository regression
executes the exact probe across a real update and rejects a reset cache. Numeric
ownership handles duplicate GID 20. [12](#source-12) [13](#source-13) [14](#source-14)

[FACT] agy's `native_release` shares a SHA512-verifying installer between image
and runtime updates. Outer version checks read metadata, never execute the CLI.
[15](#source-15)

[FACT] Pi installs its latest npm package plus image-provided ripgrep and fd-find;
its profile suppresses automatic runtime downloads. Its provider policy scopes
seven literal hosts to Node and adds no PTY grant. [16](#source-16)

[FACT] OpenCode installs its native executable through npm latest, alongside
ripgrep. Its own capability grants provider and npm registry egress only to that executable;
managed launch disables automatic LSP downloads and uses the bundled model catalog. [17](#source-17)

[FACT] Explicit OCI Features extend the installation plan using persisted digest
locks and typed options; they add no capability grants. Their pinned artifacts
and option values participate in composed-image identity. [18](#source-18)

[FACT] Codex's feature installs distro `bubblewrap` alongside npm `@latest`,
using the apt archive and npm download caches. Its managed profile disables
Codex's nested sandbox; OpenShell remains the confinement boundary. The image
smoke checks `bwrap` on the confined PATH, not nested sandbox operation.
[19](#source-19) [20](#source-20) [21](#source-21) [12](#source-12)

[FACT] The optional aws feature installs a verified official CLI archive with no
required capabilities. It uses the ordinary recipe hash and refresh/cache rules;
AWS credential and custom-egress setup remain explicit. [22](#source-22)

## Sources

1. <a id="source-1"></a> [internal/agentboximage/composed.go](../../internal/agentboximage/composed.go#L48) — prepared identity, repair, hashing and build selection.
2. <a id="source-2"></a> [internal/agentboximage/ensure.go](../../internal/agentboximage/ensure.go#L54-L105) — `EnsureImage`, `imageRefreshArgs`.
3. <a id="source-3"></a> [assets/entrypoint_test.go](../../assets/entrypoint_test.go#L11) — prepared-identity no-op, group collisions and rejection before mutation.
4. <a id="source-4"></a> [Image identity regressions](../../internal/agentboximage/composed_test.go#L48) — identity cache separation and native worker build/plan agreement.
5. <a id="source-5"></a> [BuildKit v0.31.2 bridge](https://github.com/moby/buildkit/blob/v0.31.2/solver/llbsolver/bridge.go#L137-L150) — cache mount reset before an uncached solve.
6. <a id="source-6"></a> [npm installation](../../internal/features/features.go#L444) — shared `npmPackageInstallDockerfile`.
7. <a id="source-7"></a> [Dockerfile reference](https://docs.docker.com/reference/dockerfile/#arg) — cache mounts/GC, inherited ARGs and cache invalidation.
8. <a id="source-8"></a> [Base Dockerfile](../../assets/containers/agentbox/Dockerfile#L28) — apt cache, retries and strict index refresh.
9. <a id="source-9"></a> [Base recipe regression](../../assets/base_image_test.go#L11) — repository/index/install failure propagation.
10. <a id="source-10"></a> [Apt installers](../../internal/features/apt.go#L3-L14) — shared locked metadata/archive mounts and strict refresh/install recipe; callers in codex.go, pi.go, opencode.go and features.go; conditional Python fallback in agent_observe.go.
11. <a id="source-11"></a> [Apt regressions](../../internal/features/apt_test.go#L11-L69) — builtin cache coverage and unavailable-index failure before installation.
12. <a id="source-12"></a> [scripts/smoke/image-ensure.sh](../../scripts/smoke/image-ensure.sh#L27) — ownership-layer measurement, forced rebuild/cache checks and maintenance.
13. <a id="source-13"></a> [Image smoke probe](../../scripts/smoke/common.sh#L46) — `smoke_check_image_identity`; no account/project mounts or agent workload.
14. <a id="source-14"></a> [Cache probe regression](../../scripts/test_image_cache_probe.py#L1-L97) — exact smoke shell probe, real isolated APT update with default cleanup, unchanged indexes and reset-cache rejection.
15. <a id="source-15"></a> [Antigravity adapter](agy.md) — native installation, auth and runtime contracts.
16. <a id="source-16"></a> [Pi adapter](pi.md) — install, state, provider and runtime contracts.
17. <a id="source-17"></a> [OpenCode adapter](opencode.md) — native runtime, account and validation contracts.
18. <a id="source-18"></a> [OCI plan](../../internal/ocifeature/plan.go#L228) — resolution and Dockerfile generation.
19. <a id="source-19"></a> [Codex installer](../../internal/features/codex.go#L3) — `codexInstallDockerfile`.
20. <a id="source-20"></a> [Codex profile](../../internal/agentprofile/builtin/codex.yaml#L14) — default args delegate confinement to OpenShell.
21. <a id="source-21"></a> [Codex install regression](../../internal/features/features_test.go#L56) — `TestCodexFeatureInstallsBubblewrap`.
22. <a id="source-22"></a> [AWS feature](../../internal/features/aws.go#L1) — embedded installer/key recipe; configuration is described in `bedrock.md`.
