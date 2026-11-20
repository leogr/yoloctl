# Policy boundaries

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; baseline writable paths at `e3b21585fc6a31b400f01455a70838d8ebdbabb7` and accompanying changes; agent npm grant `8a5e840af94251837f9ad6e9bded0b026b458070` and accompanying changes; re-read cited sources when changing behavior.

Image composition, build caches and built-in installers are in
[image composition](image-composition.md); agent updates in a running box are
in [agent maintenance](agent-maintenance.md).

[FACT] Effective features combine box selections and agent requirements;
effective capabilities combine box grants and feature requirements, plus one
agent-origin grant: read-only access to the managed npm prefix when the agent
profile names an npm package ([agent maintenance](agent-maintenance.md)). Features
install software and/or grant policy; agents do not declare capabilities.
Composition validates catalogs and policy. [1](#source-1) [2](#source-2)

[FACT] Policy selects sandbox user/group, best-effort Landlock, capability-based
workdir access and scoped network grants. Every box's baseline makes `/tmp`,
`/var/tmp`, `/home/sandbox`, `/run`, `/dev/null` and `/dev/zero` writable.
`/var/tmp` is there because SQLite, without `SQLITE_TMPDIR` or `TMPDIR`, sorts
through a temporary file in `/var/tmp`, whose mode passes its `access()` probe;
denied, Codex's index-building state upgrade failed and every Codex session
exited at launch (`baselineFilesystem`, `TestCompose_VarTmpReadWrite`). [22](#source-22) It rejects overlapping writable paths,
known `/dev` symlinks and invalid host patterns. PTY access grants `/dev/pts` and
`/dev/tty`, never `/dev/ptmx`. Each declared extra mount gets Landlock access
matching its bind (`read_only` for `:ro`, otherwise `read_write`) unless an
equal or broader entry already covers it, such as the account-state slot under
`/home/sandbox`. Tests include byte-exact policy goldens.
[1](#source-1) [3](#source-3) [8](#source-8) [10](#source-10) [20](#source-20)

[FACT] Static tests compare explicit agentless GitHub/npm grants with existing
goldens and filesystem policy across network modes, and check that a read-only
extra mount receives only a read-only entry. These checks establish computed
policy, not live enforcement. [21](#source-21)

[FACT] A network egress grant can opt into `allow_encoded_slash` for scoped npm
URLs. The compiler emits a REST endpoint with enforced full HTTP access and the
same host/port/binary scope. Copy preserves the opt-in per endpoint; show exposes
it in text/JSON. OpenShell v0.0.72 requires a protocol to read this flag and expands
the access preset before evaluation. Other endpoints retain strict parsing.
[13](#source-13) [14](#source-14) [15](#source-15) [16](#source-16) [17](#source-17)

[FACT] The outer container starts with elevated setup capabilities and
unconfined OCI seccomp/AppArmor settings; OpenShell establishes the inner
sandbox. `unconfined_network` selects process mode, removing inner netns/proxy
routing while retaining the process sandbox launch. Runtime commands must use
the sandbox endpoint, not substitute an outer `nerdctl exec` process.
[4](#source-4) [9](#source-9)

[FACT] When identities differ, the entrypoint aligns the named sandbox UID/GID
with the host developer and adjusts home ownership. It never chowns the workdir.
Nonunique GIDs retain existing groups (including macOS GID 20); policy still
names `sandbox`. GID zero is rejected before mutation. Tests cover rejection,
collisions, remapping and repeated startup. This changes numeric identity, not
user namespaces; home remains separate from workdir. [5](#source-5) [11](#source-11)

[FACT] Infrastructure pins are separate from agent `latest` installs. Changes
require matching policy/install/runtime validation. [3](#source-3) [6](#source-6) [7](#source-7)

[FACT] The optional operator feature installs the same CLI frontend and requires
its scoped-controller capability. Endpoint/helper/skill mounts are infrastructure
added by provisioning and excluded from ordinary extra-mount drift. Changing the
effective operator grant requires recreation of that infrastructure.
[12](#source-12)

[FACT] Codex's OpenAI HTTPS grant names Node, npm-vendored Linux x64/arm64
Codex under the managed and user prefixes, and its detached daemon. Daemon paths allow one version/target directory
under `app-server-daemon/releases`, ending in `bin/codex`; updates need no
version pin. OpenAI/ChatGPT hosts remain scoped to port 443. Unrelated binaries with no allowed ancestor, spoofed argv and unrelated
hosts gain no grant. Existing ancestor matching remains unchanged. [18](#source-18) [19](#source-19)

## Sources

1. <a id="source-1"></a> [internal/agentbox/compose.go](../../internal/agentbox/compose.go#L40-L278) — `ComposePolicy` through `compilePolicy`, including `AgentNpmPackage`.
2. <a id="source-2"></a> [internal/features/features.go](../../internal/features/features.go#L1-L141) — `Feature`, builtin install and capability declarations.
3. <a id="source-3"></a> [internal/agentbox/compose.go](../../internal/agentbox/compose.go#L302-L383) — `lintReadWriteDevSymlink` through `lintHostPattern`.
4. <a id="source-4"></a> [internal/agentbox/live.go](../../internal/agentbox/live.go#L330) — runtime options, bind mounts and environment.
5. <a id="source-5"></a> [assets/containers/agentbox/yoloctl-entrypoint.sh](../../assets/containers/agentbox/yoloctl-entrypoint.sh#L26) — GID-zero guard and identity alignment.

6. <a id="source-6"></a> [assets/embed.go](../../assets/embed.go#L1) — embedded assets; the v0.0.72 `SandboxVersion` pins and legacy Rego embed are removed by the retirement (tree `3cc5e841`), the native runtime, its Rego and the generated qualification selection are embedded through `internal/nativeartifacts` (`embedded/selection/`, git-ignored, written by `make build`); `assets/native_openshell.go` keeps an empty default; `assets/sandbox/sandbox-policy.rego` stays only as a test fixture.
7. <a id="source-7"></a> [internal/features/features_test.go](../../internal/features/features_test.go#L187) — `TestGenerateDockerfile_AgentNpmFeaturesUseLatest`.
8. <a id="source-8"></a> [internal/capabilities/capabilities.go](../../internal/capabilities/capabilities.go#L87-L129) — `PTYAllocateCapability`, `ptyGrants`, `PTYGrants`.
9. <a id="source-9"></a> [internal/sandbox/sandbox.go](../../internal/sandbox/sandbox.go#L113-L139) — `EntrypointArgs` and process-mode selection.
10. <a id="source-10"></a> [internal/agentbox/policy_golden_test.go](../../internal/agentbox/policy_golden_test.go#L51-L143) — `policyGoldenCases`, `TestPolicyGolden`.
11. <a id="source-11"></a> [assets/entrypoint_test.go](../../assets/entrypoint_test.go#L11) — prepared-identity no-op, group collisions and rejection before mutation.
12. <a id="source-12"></a> [Operator implementation](operator.md).

13. <a id="source-13"></a> [Capability authoring](../../internal/capabilities/user.go#L54) — `EgressGrant`, `translate`, `toGrants`.
14. <a id="source-14"></a> [Endpoint compiler](../../internal/agentbox/compose.go#L452) — `toNetworkPolicyFromGrant`.
15. <a id="source-15"></a> [Capability display](../../cmd/yoloctl/capability.go#L98) — text and JSON grants.
16. <a id="source-16"></a> [OpenShell L7 config](https://github.com/NVIDIA/OpenShell/blob/v0.0.72/crates/openshell-supervisor-network/src/l7/mod.rs#L164) — `parse_l7_config`, `expand_access_presets` at L1460.
17. <a id="source-17"></a> [OpenShell policy preprocessing](https://github.com/NVIDIA/OpenShell/blob/v0.0.72/crates/openshell-supervisor-network/src/opa.rs#L722) — validation and access expansion.

18. <a id="source-18"></a> [OpenAI capability](../../internal/capabilities/capabilities.go#L172) — `network.openai` executable paths and endpoints.
19. <a id="source-19"></a> [Codex egress regressions](../../internal/agentbox/codex_egress_test.go#L19) — `TestCodexEgressGlobScope` checks composed binary paths with OPA glob.match; skips without the optional OPA tool.
20. <a id="source-20"></a> [Extra mount regressions](../../internal/agentbox/policy_golden_test.go#L383) — `TestPolicyGrantsDeclaredExtraMounts`; golden `extra_mounts`.
21. <a id="source-21"></a> [Static composition regressions](../../internal/agentbox/compose_surface_test.go#L1) — `TestComposeExplicitNetworkCapabilitiesMatchGoldens`, `TestComposeUnconfinedNetworkKeepsFilesystemPolicy`, `TestComposeExtraReadOnlyMountDesiredPolicy`; `ComposePolicy` and `compilePolicy` in compose.go L40-L226.
22. <a id="source-22"></a> [Baseline filesystem](../../internal/agentbox/compose.go#L432) — `baselineFilesystem`; `TestCompose_VarTmpReadWrite` in compose_var_tmp_test.go; the goldens in testdata/policy_golden.
