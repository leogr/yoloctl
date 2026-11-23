# Image composition and build caches

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; agent npm installer `8a5e840af94251837f9ad6e9bded0b026b458070` and accompanying changes; apt metadata caching/repository consolidation verified on `9101409dbbb5f2febbe3bdf2f581dff7775ff43b` and accompanying changes; APT-compatible smoke probe correction on `a3827ba3b050fc0e7641586704954c4fc76f0241` and accompanying changes. GitHub CLI install on `804b5adcd478a04b234daa3d344587fd58525df1` and accompanying changes. State bind parents on `bfa83927136293f6dfdbfe32ddf7eca3b235a2c8` and accompanying changes.

Install-plan catalog reuse verified on `5a4b9acf13bce7f18501300ad49117613ee08a91` and accompanying changes.

[FACT] Composition prepares available sandbox identities before installation,
using the space owner's UID/GID without account/workdir mounts. npm owns packages
in its install layer; the agent's own package is installed by and owned by
`yoloctl-maint` in `/opt/yoloctl/npm`, an image input that keeps such recipes
distinct ([agent maintenance](agent-maintenance.md)). Final home repair changes only mismatches without following
symlinks. Matching startups skip remapping; the entrypoint handles other identities.
References hash builder version, base, recipe and identity; policy-only features
are hash-neutral. Cached images are reused; forced builds refresh feature layers
only. Custom bases retain normal runtime handling. [1](#source-1) [2](#source-2)
[3](#source-3) [4](#source-4)

[FACT] Install planning loads the private custom Feature catalog at the first
ordinary lookup and reuses that load for the worker's snapshot. OCI references
and internal setup features bypass the ordinary catalog. [27](#source-27) [28](#source-28)

[FACT] A nested managed bind below `/home/sandbox` adds its parent directories
to the image recipe. When this is the sole install, only new parents receive
the launch UID/GID, and identity remapping stays at startup. Other install
recipes use the existing home repair. Setup stops at symlinks and the runtime
mounts the leaf. [1](#source-1) [25](#source-25) [26](#source-26)

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

[FACT] The `github` feature installs the latest `gh`, pinned to GitHub's apt
repository, at `/usr/bin/gh`, the path `network.github` names. The embedded
keyring must hold exactly the two published primary keys; apt receives only
those. Rotation is a source update. Every box with `github` gets a new
composed image. [23](#source-23) [24](#source-24)

## Sources

1. <a id="source-1"></a> [internal/agentboximage/composed.go](../../internal/agentboximage/composed.go#L45-L81) — prepared identity, repair and hashing. Build selection starts at `EnsureComposedImage` on line 98.
2. <a id="source-2"></a> [internal/agentboximage/ensure.go](../../internal/agentboximage/ensure.go#L54-L105) — `EnsureImage`, `imageRefreshArgs`.
3. <a id="source-3"></a> [assets/entrypoint_test.go](../../assets/entrypoint_test.go#L11) — prepared-identity no-op, group collisions and rejection before mutation.
4. <a id="source-4"></a> [Image identity regressions](../../internal/agentboximage/composed_test.go#L50-L78) — identity cache separation. `TestComposedImageRef_UsesSpaceOwner` on line 127 checks native worker build and plan agreement.
5. <a id="source-5"></a> [BuildKit v0.31.2 bridge](https://github.com/moby/buildkit/blob/v0.31.2/solver/llbsolver/bridge.go#L137-L150) — cache mount reset before an uncached solve.
6. <a id="source-6"></a> [npm installation](../../internal/features/features.go#L542) — shared `npmPackageInstallDockerfile`.
7. <a id="source-7"></a> [Dockerfile reference](https://docs.docker.com/reference/dockerfile/#arg) — cache mounts/GC, inherited ARGs and cache invalidation.
8. <a id="source-8"></a> [Base Dockerfile](../../assets/containers/agentbox/Dockerfile#L28) — apt cache, retries and strict index refresh.
9. <a id="source-9"></a> [Base recipe regression](../../assets/base_image_test.go#L11) — repository/index/install failure propagation.
10. <a id="source-10"></a> [Apt installers](../../internal/features/apt.go#L3-L14) — shared locked metadata/archive mounts and strict refresh/install recipe; callers in codex.go, pi.go, opencode.go and features.go; conditional Python fallback in agent_observe.go.
11. <a id="source-11"></a> [Apt regressions](../../internal/features/apt_test.go#L11-L69) — builtin cache coverage and unavailable-index failure before installation.
12. <a id="source-12"></a> [scripts/smoke/image-ensure.sh](../../scripts/smoke/image-ensure.sh#L30) — ownership-layer measurement, forced rebuild/cache checks and maintenance.
13. <a id="source-13"></a> [Image smoke probe](../../scripts/smoke/common.sh#L78-L101) — `smoke_check_image_identity`; no account/project mounts or agent workload.
14. <a id="source-14"></a> [Cache probe regression](../../scripts/test_image_cache_probe.py#L1-L97) — exact smoke shell probe, real isolated APT update with default cleanup, unchanged indexes and reset-cache rejection.
15. <a id="source-15"></a> [Antigravity adapter](agy.md) — native installation, auth and runtime contracts.
16. <a id="source-16"></a> [Pi adapter](pi.md) — install, state, provider and runtime contracts.
17. <a id="source-17"></a> [OpenCode adapter](opencode.md) — native runtime, account and validation contracts.
18. <a id="source-18"></a> [OCI plan](../../internal/ocifeature/plan.go#L312) — `Resolve`; `Plan.Dockerfile` generation.
19. <a id="source-19"></a> [Codex installer](../../internal/features/codex.go#L6-L8) — `codexInstall`.
20. <a id="source-20"></a> [Codex profile](../../internal/agentprofile/builtin/codex.yaml#L14) — default args delegate confinement to OpenShell.
21. <a id="source-21"></a> [Codex install regression](../../internal/features/features_test.go#L56) — `TestCodexFeatureInstallsBubblewrap`.
22. <a id="source-22"></a> [AWS feature](../../internal/features/aws.go#L9-L26) — embedded installer/key recipe; configuration is described in `bedrock.md`.
23. <a id="source-23"></a> [GitHub CLI feature](../../internal/features/github-install.sh#L1-L36) — key check, keyring, origin pin, install; recipe `github.go`.
24. <a id="source-24"></a> [GitHub CLI Linux install](https://github.com/cli/cli/blob/trunk/docs/install_linux.md) — published fingerprints and keyring checksums, checked 2026-11-20.
25. <a id="source-25"></a> [Runtime recipe](../../internal/agentbox/privileged.go#L30-L55) — selects setup for managed state.
26. <a id="source-26"></a> [State parents installer](../../internal/features/agent_state_parents.go#L20-L91) — directory selection and creation.
27. <a id="source-27"></a> [Install planning](../../internal/features/features.go#L463-L498) — `InstallPlan`; `lookup` at line 286 resolves OCI and privileged setup before custom Features.
28. <a id="source-28"></a> [Runtime lookup](../../internal/features/agentnpm.go#L32-L40) — `lookupRuntime` handles agent npm and state-parent setup before `lookup`.
