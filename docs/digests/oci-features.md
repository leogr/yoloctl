# OCI Dev Container Features

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; ignored runtime requests verified on `1faf621b2de05046711d5177863d326146cf122b` and accompanying changes; Feature folders and environment verified on `0a1e47d47001eab39274faf83f3af36419b04f2b` and accompanying changes; re-read cited sources when changing behavior.

Environment validation verified on `4624036d2ea68a359c1e308a4f6b2651921ffba5` and accompanying changes.
Catalog binary lookup verified on `5a4b9acf13bce7f18501300ad49117613ee08a91` and accompanying changes.

[FACT] `box create`, `box copy`, `box set` and `image build` accept
repeatable `--feature` names or explicit registry references alongside
`--features`. `--feature-option FEATURE.OPTION=VALUE` uses an unambiguous final
reference component or the full reference. String/boolean types, defaults and
enums come from the artifact; proposals suggest values without restricting them.
Options are non-secret installation inputs. [1](#source-1) [2](#source-2) [10](#source-10) [11](#source-11)

[FACT] Box manifests persist `feature_options` keyed by the complete reference
and `feature_locks` containing immutable manifest/layer identities and metadata.
Declaration writes resolve missing locks; existing locks are retained. Inspection,
drift and composed-image identity use those locks offline. Option changes alter
the generated recipe and runtime image identity. `box copy` preserves pins/options.
Remove `feature_locks` with `box edit` to explicitly resolve moving tags again;
`--rebuild` reruns the locked installers, without changing their artifact versions.
[1](#source-1) [2](#source-2) [3](#source-3) [12](#source-12)

[FACT] The resolver supports public HTTPS OCI registries with anonymous Bearer
authentication and SHA256 digests. It validates artifact type, manifest/layer
binding, response sizes and layer checksums. Bounded tar or gzip-tar extraction
occurs in memory; links, special entries, duplicate paths and traversal fail.
Files enter a private image-build context and never execute on the host. Registry
credentials from host Docker configuration are not read. Connection, TLS,
headers and stalls during body reads have a two-minute fail-safe. The caller
can stop the request earlier. Redirects discard intermediate bodies before
applying the HTTPS and hop-count checks. Redirects that change hosts omit authentication. [4](#source-4)

[FACT] Recursive `dependsOn` expands the installation set. `installsAfter`
orders already-selected OCI Features. Equal digests/options install once; alias
references retain their locks. Cycles and excessive graphs fail. Each
installer runs as image-build root with typed uppercase option variables,
`_REMOTE_USER=sandbox`, its home and root container-user variables. Supported
`containerEnv` values enter the image; customizations such as editor extensions
are ignored. This installs tools into the existing Ubuntu foundation and does
not select a Microsoft base image. [2](#source-2) [3](#source-3)

[FACT] The first eligible soft-order hint scans the selected references directly.
Later hints reuse full tagless names and short aliases for references reached by
the scans. Each hint stops at the first match for each other installation.
The resolver sorts each node's edges before traversing them. [2](#source-2)
Verified on `ffd9e9df71c33aca45ad783b3c6f21cc106fdc51` and accompanying changes.

[FACT] Requests for privileged mode, init replacement, added Linux
capabilities, security overrides, mounts, entrypoints or lifecycle hooks are
always ignored, also for privileged boxes: the Feature installs and nothing
requested is applied. Any other unknown property fails before persistence. Each
declaration write and image build logs a notice per Feature listing every
non-default ignored request, one per line, continuing in further records past 1 KiB (values cut at 200 bytes and
marked), escaped and filtered like other CLI notices; a worker's warm pass may
repeat it. Ignored requests grant nothing, so debuggers needing `SYS_PTRACE` or
unconfined seccomp may not work. Local feature directories, direct tarball URLs and complete
`devcontainer.json` lifecycle execution are not implemented.
[2](#source-2) [5](#source-5) [13](#source-13) [16](#source-16)

[FACT] A locked Feature's `containerEnv` adds policy folders: literal `PATH`
entries read-only; a folder variable holding a `PATH` entry read-write; other
paths read-only. A folder holding, inside or containing a catalog network
executable (pattern) stays read-only: with go:1/rust:1 `/go` and
`/usr/local/cargo` are writable, `/usr/local/go` and `/usr/local/rustup` not.
Protected places (system and yoloctl folders, yoloctl mount destinations) and
declared mount targets are never granted. A final build step resolves each
granted folder (`readlink -m`) and fails if it lands in a protected place, or a
writable one in a built-in network-tool folder; read-only links pass. Limit: a
custom capability's executable reached only through a link is checked by its
declared name. Other boxes keep folders and image; every box with
`network.cargo` gains `/usr/local/rustup/toolchains/*/bin/cargo`. [17](#source-17) [18](#source-18)

[FACT] Folder checks visit the catalog's binaries in capability-name order,
preserving grant and binary order, then visit the box's effective grants. A
malformed private capability catalog contributes zero binaries to the first
pass, including built-in binaries. [17](#source-17) [19](#source-19)

[FACT] The expanded `containerEnv` (Dockerfile order, `${NAME}`/`$NAME`, Feature
values only; 4 KiB per value, 64 KiB and 1024 variables per plan; unresolvable,
oversized or other `$` forms unset the key; `PATH`, reserved and agent-profile
names skipped) reaches native and privileged children below box `--env`, the
account map and `launch_env`; a privileged child gets the box value of a
Feature key. Notices list skipped folders and variables; `box show`/`inspect`
list declared values. Existing boxes need `box recreate`.
[17](#source-17) [18](#source-18)

[FACT] Definition validation checks each environment name and value before
checking supported braced substitutions. The process compiles the substitution
regexp when an entry first reaches that check and reuses it thereafter. [2](#source-2)

[FACT] Cobra supplies shell and palette completion for selected Feature options,
boolean/enum/proposed values, built-ins, common references and cached references.
Metadata lookup is bounded and may return no suggestions when the registry is
unavailable; persisted locks provide offline option completion. Its one-hour
metadata cache is advisory and never becomes an installation lock. There is no
universal registry search or automatic import of a project's Features.
[6](#source-6) [7](#source-7) [14](#source-14)

[FACT] Feature option completion keeps an explicitly typed full qualifier.
The first short-name count scans the selected names directly. Later counts
reuse OCI aliases, while duplicate references and bare names still count
toward ambiguity. [6](#source-6)
Verified on `35772701f5880bd0b7ba417afd80eb43be6c48ae` and accompanying changes.

[FACT] Focused tests exercise public-registry protocol fixtures, integrity errors,
unsafe archives, authority rejection, dependency cycles, option drift, persistence
and completion. These do not establish live installer compatibility for every
upstream Feature or either CPU architecture. [8](#source-8) [9](#source-9) [15](#source-15)

## Sources

1. <a id="source-1"></a> [CLI options](../../cmd/yoloctl/agentbox.go#L910) — create/copy flags.
2. <a id="source-2"></a> [Plan](../../internal/ocifeature/plan.go#L75) — `Definition`, `Compatible`, `RuntimeRequests`, `Values`, `ApplyOptions`, `Resolve`, `Dockerfile`.
3. <a id="source-3"></a> [Persistence](../../internal/agentbox/oci_features.go#L10) — `ResolveFeatures`, `FeaturePlan`.
4. <a id="source-4"></a> [Registry](../../internal/ocifeature/registry.go#L30) — `Parse`, `get`, `Resolve`, `Files`, `unpack`.
5. <a id="source-5"></a> [Capability composition](../../internal/agentbox/compose.go#L55) — `EffectiveCapabilities`.
6. <a id="source-6"></a> [Completion](../../cmd/yoloctl/completion_features.go#L14) — selected declarations and metadata options.
7. <a id="source-7"></a> [Advisory cache](../../internal/ocifeature/completion.go#L30) — `remember`, `MetadataForCompletion`, `CachedReferences`.
8. <a id="source-8"></a> [Resolver tests](../../internal/ocifeature/oci_test.go#L66) — registry, archive, options and dependencies.
9. <a id="source-9"></a> [Declaration tests](../../internal/agentbox/oci_features_test.go#L9) — persisted pins and option drift.
10. <a id="source-10"></a> [Set options](../../cmd/yoloctl/agentbox_reconcile.go#L441) — replacement and option updates.
11. <a id="source-11"></a> [Manual builds](../../cmd/yoloctl/image.go#L344) — Feature-aware build context.
12. <a id="source-12"></a> [Image composition](../../internal/agentboximage/composed.go#L45) — recipe identity and archive context.
13. <a id="source-13"></a> [OCI lookup](../../internal/features/features.go#L279) — no implicit capability requirements.
14. <a id="source-14"></a> [Cobra registration](../../cmd/yoloctl/completion.go#L248) — feature and option completion.
15. <a id="source-15"></a> [Completion tests](../../cmd/yoloctl/completion_features_test.go#L13) — CLI routes and metadata values.
16. <a id="source-16"></a> [Runtime notice](../../cmd/yoloctl/feature_runtime_notice.go#L14) — `noticeIgnoredRuntime`; tests `internal/ocifeature/runtime_test.go`, `cmd/yoloctl/feature_runtime_notice_test.go`.
17. <a id="source-17"></a> [Feature runtime](../../internal/agentbox/feature_runtime.go#L59) — `FeatureRuntimeOf`, `featureGrants`, `FeatureEnvironment`, `workloadLiterals`; `compilePolicy`; `ChildEnvironmentWithFeatures`; tests `feature_runtime_test.go`, golden `oci_go_rust.yaml`.
18. <a id="source-18"></a> [Expansion and folders](../../internal/ocifeature/runtime_env.go#L79) — `Environment`, `PathCandidates`, `ProtectedPath`, `linkGuard`; tests `runtime_env_test.go`.
19. <a id="source-19"></a> [Catalog binaries and parity](../../internal/capabilities/capabilities.go#L451-L461) — `CatalogNetworkBinaries`; differential cases in `internal/agentbox/catalog_snapshot_test.go`.
