# Audit rule paths and container recognition

Verification: `e078b4ee0cf92e680a4a5ce2f1e52c434e487227` and accompanying changes.

[FACT] Filesystem rules distinguish the path changed by a syscall from the
payload of a new symbolic link. In pinned libs 0.26.1, `fs.path.source` for
symlink/symlinkat is the new link location; `fs.path.target` is its destination
text. An image layer's link under containerd/BuildKit storage pointing at
`/usr/lib/systemd/...` does not modify that VM path. Both persistence and
infrastructure rules therefore exclude symbolic-link payloads from target
matching, while checking the created link's source path. Rename/hard-link
targets remain covered. [1](#source-1) [2](#source-2)

[FACT] The exception is based on syscall semantics, with no blanket exemption
for containerd, buildkitd, nerdctl or containerd-shim. Real protected name/source
paths remain eligible regardless of which runtime process touches them; this
includes VM units, cron, SSH keys, loader configuration and infrastructure.
The existing host package/controller exceptions remain. Known OpenShell setup
infrastructure writes use the same fixed-executable, schema-1 confined-container
and protocol-2 context as the namespace rule. Missing context does not grant
that exception. These remain detection heuristics, not authenticity proofs.
[1](#source-1) [3](#source-3)

[FACT] Container plugin 0.7.6 registers nerdctl systemd-scope recognition in its
CRI matcher; its native-containerd matcher handles namespace/ID layouts.
Configuration enables both engines against only `/run/containerd/containerd.sock`,
with the existing two-second per-engine timeout. Docker/Podman/other engines
stay disabled. The native API still enumerates containerd namespaces, including
default; the guest does not need its CRI service enabled. CRI RPCs may fail on
that socket; the worker fetcher tries enabled metadata providers until one
succeeds. No containerd configuration, runtime cgroup layout or yolo plugin ABI
is changed. [4](#source-4) [5](#source-5) [6](#source-6) [7](#source-7)

[FACT] The native-containerd matcher has no nerdctl systemd-scope pattern;
unmatched thread cgroups are reported as host before snapshot enrichment.
The C++ plugin joins a fresh, unambiguous snapshot only when the thread has a
recognized container ID. These source contracts require native event
qualification; a no-driver engine or structural rule test does not prove live
cgroup identity or noise rates. [5](#source-5) [6](#source-6) [8](#source-8)

## Sources

1. <a id="source-1"></a> [Pinned rules](../../internal/audit/rules.yaml#L1) — `yolo_fs_target_mutation`, persistence/infrastructure and setup context.
2. <a id="source-2"></a> [libs 0.26.1 filesystem fields](https://github.com/falcosecurity/libs/blob/0.26.1/userspace/libsinsp/sinsp_filtercheck_fspath.cpp) — `create_fspath_checks`, symlink linkpath/target and rename/hard-link mappings.
3. <a id="source-3"></a> [Rule regressions](../../internal/audit/rules_test.go#L1) — privacy boundaries and `TestProtectedPathsIgnoreOnlySymlinkPayloads` structural checks.
4. <a id="source-4"></a> [Configuration](../../internal/audit/config.go#L1) — `Configuration`, enabled engines and explicit socket paths.
5. <a id="source-5"></a> [Container 0.7.6 CRI matcher](https://github.com/falcosecurity/plugins/blob/plugins/container/v0.7.6/plugins/container/src/matchers/cri.cpp) — nerdctl scope pattern; native-containerd matching is separate.
6. <a id="source-6"></a> [Container 0.7.6 matcher selection](https://github.com/falcosecurity/plugins/blob/plugins/container/v0.7.6/plugins/container/src/matchers/matcher.cpp) — engines select matchers; unmatched cgroups become host.
7. <a id="source-7"></a> [Container 0.7.6 metadata fetcher](https://github.com/falcosecurity/plugins/blob/plugins/container/v0.7.6/plugins/container/go-worker/pkg/container/fetcher.go) — enabled-provider lookup and retry bounds.
8. <a id="source-8"></a> [Metadata extraction](../../third_party/audit-plugin/plugin.cpp#L1) — thread container ID lookup and bounded snapshot extraction.
