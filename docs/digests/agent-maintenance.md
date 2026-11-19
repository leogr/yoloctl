# Agent maintenance and the managed npm prefix

Verification: `8bb7b34e20fe0d052c2b38cfc02f97dabeea5daa` and accompanying changes.

[FACT] npm is split by role. The package a box's agent profile names
(`npm_package`, built-in or custom) is installed in `/opt/yoloctl/npm`, owned by
the system user `yoloctl-maint`, whose private home `/var/lib/yoloctl-maint`
(mode 0700) holds the runtime update's start folder, temporary files and cache.
Every other npm install — npm features, the `npm` development feature, an agent
feature on a box whose agent is something else, manual installs — keeps
`/home/sandbox/.npm-global`. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] `RuntimeFeatures` appends the internal `yoloctl:agent-npm:<escaped
package>` installer for that package, so build and drift share one recipe and
the same features with and without that agent yield different images. The
renderer installs the package once, at the first feature that installs it or
where that installer stands when none does, and later copies of it render
nothing. That step creates or verifies the user (free system numbers, exact
home/shell/group shape, never root or sandbox), creates the folders, runs
`npm install -g PKG@latest` as `yoloctl-maint` with the shared per-UID build
cache, and puts `/opt/yoloctl/npm/bin` on PATH for later build steps; PATH puts
it first once more after every feature and OCI plan. Without an npm agent the
recipes render exactly as before; a pinned custom npm feature that is the agent's
package installs `@latest`. OpenCode's executable check follows the prefix in
use. [1](#source-1) [4](#source-4) [5](#source-5) [6](#source-6) [7](#source-7)

[FACT] Runtime npm installs (lifecycle autoupdate and `agent update`) run
`nerdctl exec --user yoloctl-maint --workdir /var/lib/yoloctl-maint` with the
fixed system PATH, that HOME and TMPDIR, the managed prefix and cache, and the
user and global npmrc files bypassed. Version reads use the managed package's
`package.json`; a copy in the user prefix never counts, and an unreadable file
keeps the existing skip notice. Explicit updates reinstall even at the same
version, which also repairs a cut-off install. The native installer, metadata
reads and the process probe still run as sandbox with the fixed PATH. Install
errors propagate with
`--no-autoupdate` guidance for npm. [8](#source-8) [9](#source-9) [10](#source-10)
[11](#source-11) [12](#source-12)

[FACT] An agent profile with an npm package adds `fs.agent-npm.read`
(read-only `/opt/yoloctl/npm`) with origin `agent`, the one capability not
reached through features; `box inspect` prints that origin. `network.anthropic`,
`network.openai` and `network.opencode` name their programs under both prefixes.
For such a box, the native container plan refuses mount targets equal to,
inside or above the managed prefix or the maintenance home. [13](#source-13)
[14](#source-14) [15](#source-15) [16](#source-16)

[FACT] The image smoke checks the managed owner, no group/other write,
`command -v` for the agent and a feature tool, a refused write from the box,
the forced update and the maintenance cache owner. [17](#source-17)
[18](#source-18)

## Sources

1. <a id="source-1"></a> [internal/agentnpm/agentnpm.go](../../internal/agentnpm/agentnpm.go#L1-L31) — `Prefix`, `User`, `Home`, `Cache`, `PackageJSON`.
2. <a id="source-2"></a> [internal/features/features.go](../../internal/features/features.go#L73-L77) — `NPMGlobalPrefix`, the box user's prefix.
3. <a id="source-3"></a> [internal/agentbox/compose.go](../../internal/agentbox/compose.go#L110-L125) — `AgentNpmPackage`.
4. <a id="source-4"></a> [internal/agentbox/privileged.go](../../internal/agentbox/privileged.go#L30-L52) — `RuntimeFeatures`.
5. <a id="source-5"></a> [internal/features/agentnpm.go](../../internal/features/agentnpm.go#L1-L114) — runtime feature name, `npmInstaller`, `recipe`, `agentNpmInstallDockerfile`, `agentNpmFinalPath`.
6. <a id="source-6"></a> [internal/features/features.go](../../internal/features/features.go#L380-L426) — `renderDockerfile`.
7. <a id="source-7"></a> [Rendering regressions](../../internal/features/agentnpm_test.go#L1-L119) — tool vs agent, first supplier, appended/pinned/duplicate, OpenCode check, names.
8. <a id="source-8"></a> [cmd/yoloctl/autoupdate.go](../../cmd/yoloctl/autoupdate.go#L98-L160) — `agentMaintenanceExecArgs`, `npmInstallGlobalAtLatestArgs`, `fetchInstalledNpmVersion`.
9. <a id="source-9"></a> [Native install argv](../../cmd/yoloctl/agent_distribution.go#L102-L118) — `nativeInstallArgs` uses the shared exec builder.
10. <a id="source-10"></a> [Maintenance argv regression](../../cmd/yoloctl/autoupdate_test.go#L11-L52) — `TestNpmAgentMaintenanceUsesExplicitConfigPaths`.
11. <a id="source-11"></a> [Target and failure regressions](../../cmd/yoloctl/container_target_test.go#L64-L172) — `TestMaintenanceExecsUseSystemPathAndExactlySelectedContainer`, `TestNpmMaintenanceFailureExplainsSystemPathAndAutoupdateOptOut`.
12. <a id="source-12"></a> [Explicit update](../../cmd/yoloctl/agent.go#L144-L166) — selected ID, probe and same-version install.
13. <a id="source-13"></a> [internal/capabilities/capabilities.go](../../internal/capabilities/capabilities.go#L97-L101) — `AgentNpmReadCapability`; `OriginAgent` in the `Origin` constants.
14. <a id="source-14"></a> [Origin regression](../../internal/agentbox/compose_capabilities_test.go#L200-L227) — `TestEffectiveCapabilitiesAgentNpmRead`.
15. <a id="source-15"></a> [Mount guard](../../internal/agentbox/openshell_container_plan_linux.go#L103-L131) — `agentNpmProtected`, `mountOverlaps`; regression `TestNativeContainerPlanProtectsTheAgentNpmFolders`.
16. <a id="source-16"></a> [Image regression](../../internal/agentbox/resolved_image_test.go#L109-L155) — `TestResolvedImage_AgentNpmPackageSelectsTheManagedPrefix`.
17. <a id="source-17"></a> [scripts/smoke/image-ensure.sh](../../scripts/smoke/image-ensure.sh#L23-L27) — managed and user package paths, write refusal and forced update.
18. <a id="source-18"></a> [scripts/smoke/common.sh](../../scripts/smoke/common.sh#L46-L68) — `smoke_check_image_identity` managed-prefix owner and mode.
