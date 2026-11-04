# npm development environment

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] The `npm` feature combines a Dockerfile setup step with
`network.npm`. The base image supplies Node.js 22 from NodeSource, including
npm/npx. The feature validates `/usr/bin/node`, `/usr/bin/npm` and
`/usr/bin/npx` and reuses them. Custom images must supply those paths and the
sandbox user/group; this recipe does not install or select a different Node
version. [1](#source-1) [2](#source-2) [3](#source-3)

[FACT] Setup writes the sandbox user's `.npmrc` with
`prefix=/home/sandbox/.npm-global` and `cache=/home/sandbox/.cache/npm`.
`npm config set` preserves unrelated settings. Setup creates the directories
and gives the config/directory ownership to sandbox; the entrypoint remaps
home ownership when the host UID differs. Global commands are on the image
PATH. The config applies to the sandbox user without environment forwarding;
root-run image installs retain their own config/cache. [2](#source-2)
[3](#source-3) [4](#source-4)

[FACT] Agent maintenance still passes an explicit global prefix and uses its
separate `/home/sandbox/.npm-global/.cache/npm` cache. Enabling npm development
does not change agent update commands. [5](#source-5)

[FACT] The feature adds an install recipe to the composed-image hash. Enabling
it changes the selected runtime image, including for a box already carrying
an agent feature. The regression checks both generic and Codex compositions.
[6](#source-6) [7](#source-7)

[FACT] `network.npm` permits HTTPS to `registry.npmjs.org` for the declared
Node/npm/npx/pnpm/yarn paths. This feature does not install pnpm or Yarn. The
grant includes Node programs; it is executable/endpoint attribution, not a
filter limiting what JavaScript can send to the registry. Private registries,
Git dependencies and package-specific binary/header download hosts need
separate applicable capabilities. [8](#source-8) [9](#source-9)

[FACT] `make smoke-npm` creates an isolated generic-shell box and exercises
the confined SSH path: runtime/config/ownership, registry installation, offline
`npm ci` preserving a lockfile, build/test scripts, local/downloaded npx use,
global command installation, attributed curl denial and reconcile consistency.
The fixture dependency is pinned; tool and agent versions are not pinned by
the smoke. Its existence is not evidence of a successful live run.
[10](#source-10)

## Sources

1. <a id="source-1"></a> [internal/features/features.go](../../internal/features/features.go#L168-L173) — builtin `npm` install and required capability.
2. <a id="source-2"></a> [internal/features/npm.go](../../internal/features/npm.go#L11) — `npmInstallDockerfile` runtime verification and user config.
3. <a id="source-3"></a> [assets/containers/agentbox/Dockerfile](../../assets/containers/agentbox/Dockerfile#L9) — Node version/install, sandbox identity and global-command PATH.
4. <a id="source-4"></a> [assets/containers/agentbox/yoloctl-entrypoint.sh](../../assets/containers/agentbox/yoloctl-entrypoint.sh#L28-L46) — identity alignment and home ownership.
5. <a id="source-5"></a> [cmd/yoloctl/autoupdate.go](../../cmd/yoloctl/autoupdate.go#L72) — `sandboxNpmMaintenanceCache`, `npmInstallGlobalAtLatestArgs`.
6. <a id="source-6"></a> [internal/agentboximage/composed.go](../../internal/agentboximage/composed.go#L41-L57) — `ComposedImageRef`, `composedImageRef`.
7. <a id="source-7"></a> [internal/agentboximage/composed_test.go](../../internal/agentboximage/composed_test.go#L123-L138) — `TestComposedImageRef_NpmEnvironmentChangesImage`.
8. <a id="source-8"></a> [internal/capabilities/capabilities.go](../../internal/capabilities/capabilities.go#L258-L268) — `network.npm`.
9. <a id="source-9"></a> [npm policy golden](../../internal/agentbox/testdata/policy_golden/npm.yaml#L28-L40) — exact composed grant.
10. <a id="source-10"></a> [scripts/smoke/npm.sh](../../scripts/smoke/npm.sh#L1) — complete isolated runtime gate.
