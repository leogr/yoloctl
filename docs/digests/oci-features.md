# OCI Dev Container Features

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

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
credentials from host Docker configuration are not read. [4](#source-4)

[FACT] Recursive `dependsOn` expands the installation set. `installsAfter`
orders already-selected OCI Features. Equal digests/options install once; alias
references retain their locks. Cycles and excessive graphs fail. Each
installer runs as image-build root with typed uppercase option variables,
`_REMOTE_USER=sandbox`, its home and root container-user variables. Supported
`containerEnv` values enter the image; customizations such as editor extensions
are ignored. This installs tools into the existing Ubuntu foundation and does
not select a Microsoft base image. [2](#source-2) [3](#source-3)

[FACT] Features requesting privileged mode, init replacement, added Linux
capabilities, security overrides, mounts, entrypoints or lifecycle hooks fail
before persistence. OCI Features add no runtime grants; the box's explicit
capabilities and OpenShell policy still determine tool access. Consequently some
upstream Features, including artifacts intended for unconfined debuggers or
Docker-in-Docker, are incompatible. Local feature directories, direct tarball
URLs and complete `devcontainer.json` lifecycle execution are not implemented.
[2](#source-2) [5](#source-5) [13](#source-13)

[FACT] Cobra supplies shell and palette completion for selected Feature options,
boolean/enum/proposed values, built-ins, common references and cached references.
Metadata lookup is bounded and may return no suggestions when the registry is
unavailable; persisted locks provide offline option completion. Its one-hour
metadata cache is advisory and never becomes an installation lock. There is no
universal registry search or automatic import of a project's Features.
[6](#source-6) [7](#source-7) [14](#source-14)

[FACT] Focused tests exercise public-registry protocol fixtures, integrity errors,
unsafe archives, authority rejection, dependency cycles, option drift, persistence
and completion. These do not establish live installer compatibility for every
upstream Feature or either CPU architecture. [8](#source-8) [9](#source-9) [15](#source-15)

## Sources

1. <a id="source-1"></a> [CLI options](../../cmd/yoloctl/agentbox.go#L882) — create/copy flags.
2. <a id="source-2"></a> [Plan](../../internal/ocifeature/plan.go#L18) — `Definition`, `Compatible`, `Values`, `ApplyOptions`, `Resolve`, `Dockerfile`.
3. <a id="source-3"></a> [Persistence](../../internal/agentbox/oci_features.go#L10) — `ResolveFeatures`, `FeaturePlan`.
4. <a id="source-4"></a> [Registry](../../internal/ocifeature/registry.go#L28) — `Parse`, `get`, `Resolve`, `Files`, `unpack`.
5. <a id="source-5"></a> [Capability composition](../../internal/agentbox/compose.go#L63) — `EffectiveCapabilities`.
6. <a id="source-6"></a> [Completion](../../cmd/yoloctl/completion_features.go#L14) — selected declarations and metadata options.
7. <a id="source-7"></a> [Advisory cache](../../internal/ocifeature/completion.go#L30) — `remember`, `MetadataForCompletion`, `CachedReferences`.
8. <a id="source-8"></a> [Resolver tests](../../internal/ocifeature/oci_test.go#L66) — registry, archive, options and dependencies.
9. <a id="source-9"></a> [Declaration tests](../../internal/agentbox/oci_features_test.go#L9) — persisted pins and option drift.
10. <a id="source-10"></a> [Set options](../../cmd/yoloctl/agentbox_reconcile.go#L440) — replacement and option updates.
11. <a id="source-11"></a> [Manual builds](../../cmd/yoloctl/image.go#L344) — Feature-aware build context.
12. <a id="source-12"></a> [Image composition](../../internal/agentboximage/composed.go#L45) — recipe identity and archive context.
13. <a id="source-13"></a> [OCI lookup](../../internal/features/features.go#L250) — no implicit capability requirements.
14. <a id="source-14"></a> [Cobra registration](../../cmd/yoloctl/completion.go#L248) — feature and option completion.
15. <a id="source-15"></a> [Completion tests](../../cmd/yoloctl/completion_features_test.go#L13) — CLI routes and metadata values.
