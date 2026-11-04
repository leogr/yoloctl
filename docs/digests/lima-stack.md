# Lima container stack and upgrades

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] New/recreated Lima VMs use system containerd, disable user containerd,
and select nerdctl-full 2.3.5 by **guest** architecture. No Go build-architecture,
VM backend or mount-transport override is added. The decoded regression covers
fresh/preserved data-disk rendering and equality with Firecracker's amd64 pin.
[1](#source-1) [2](#source-2)

| Lima architecture | Full archive | SHA256 |
|---|---|---|
| x86_64 | nerdctl-full-2.3.5-linux-amd64.tar.gz | b697295c623639734aaab737523c808fd3cc8d3046039fd94fff1744e4c317aa |
| aarch64 | nerdctl-full-2.3.5-linux-arm64.tar.gz | 6e4b687f1d138e750a3c8372abc0f81d3d7490b6359c48c0562fc7dfe98859b2 |

[FACT] The full release bundles containerd 2.3.3, runc 1.5.1, CNI 1.9.1 and
BuildKit 0.31.2. These pins use release URLs and SHA256 digests. [3](#source-3)

[FACT] Lima v2.2.1 combines archives in override → instance → default order,
substituting its built-in list only when empty. Its boot installer compares
binary mtime, so an unpinned instance can change stacks after a host Lima
change. Pins exclude built-in-default drift, not explicit overrides or guest
edits. [4](#source-4) [5](#source-5)
Reuse updates selected mounts/resources/disk attachment, retaining stored
provisioning. A binary/controller update, ordinary start or box recreate does
not install the instance pin. [6](#source-6) [7](#source-7) [8](#source-8)

[FACT] With a matching host/guest bundle, the supported host upgrade is:

```sh
yoloctl space start SPACE --update-controller
yoloctl space recreate SPACE
yoloctl box start BOX --space SPACE --no-attach
```

Recreation checks the controller bundle and renders the current template with
the retained disk. Repeat explicit starts for desired boxes; privileged first
launches require the host. [9](#source-9) [10](#source-10) [11](#source-11)
Native disk identity/accounts/declarations and host workdirs survive. Sessions,
VM-local workdirs, container files outside retained mounts and system-disk
image/build caches are lost; cache survival depends on location.
[9](#source-9) [12](#source-12)
Explicit native export requires stopped consumers or `--stop` and omits
disposable container state. Upgrading the space hosting a team stops its
workloads too. [9](#source-9) [19](#source-19)

[FACT] nerdctl 2.3.5 derives its privilege boolean from a label older creators
did not record, allowing false for old privileged containers. The boolean alone
is insufficient: yoloctl requires agreeing launch, label and bind evidence;
missing/conflicting evidence stays UNKNOWN. The mixed-stack regression is
`runtime not privileged`; missing/null records remain unknown during parsing.
[13](#source-13) [14](#source-14) [15](#source-15) [16](#source-16) [17](#source-17)

[FACT] The host gate builds one bundle and uses a new isolated synthetic fixture
for stack/operator/policy/image checks. It replaces inherited `YOLOCTL_HOME`
with the fixture's `home`, shared by nested smokes, and checks config/cache
roots through read-only `version --json`. Recovery carries that explicit home.
An owned wrapper changes only Lima's
child home, isolating macOS downloads; caller/yoloctl/build homes remain intact.
It bounds commands, verifies cache bytes and cancels failed transient groups
even after their leader exits. Termination/drain are bounded; unverified
cancellation blocks fixture destruction and retains the original result and
recovery paths. A denied null-signal probe still requires the bounded PID/group/state
inventory: zombies are inactive, live members block settlement, and unavailable
or malformed inventory stays uncertain. After VM/disk teardown is verified,
removal grants owner-write
only on the owned read-only Go cache directories using a no-follow descriptor
walk; the same remover handles pre-VM failures. Failed teardown retains the
cache unchanged. Separate `--check` observes an already running matching space
without mutation/boot or the QA wrapper. Its explicit `--config-root` selects
`YOLOCTL_HOME`; the queried cache child may be absent and is never created by
the query. Restart expectations use independent
runtime dimensions, retaining display and session diagnostics and rejecting
unknown runtime evidence in either snapshot. Inventory/UUID comparison does not
establish native-byte preservation. Fake-tool tests cover routing/failure
boundaries, not live Lima behavior. [18](#source-18) [20](#source-20)

[FACT] Before building, the Go preflight records the installed launcher and
verifies the exact stable `go.mod` toolchain (or the `go` version, normalizing
language `1.27` to release `go1.27.0`). `GOENV=off` and owned Go module/build
caches remain explicit. Standard Go toolchain switching can download the release
with checksum verification; proxy/checksum policy is not relaxed. Unavailability
or version mismatch fails before build/VM work with actionable version details.
Read-only observation does not select/download Go. [18](#source-18)
[20](#source-20) [21](#source-21)

## Sources

1. <a id="source-1"></a> [Template](../../assets/lima/yoloctl.yaml.tmpl#L31) — system modes, architecture archives.
2. <a id="source-2"></a> [Decoded renders](../../internal/lima/template_test.go#L14) — `TestTemplatePinsFullContainerStackForBothGuestArchitectures`.
3. <a id="source-3"></a> [nerdctl v2.3.5](https://github.com/containerd/nerdctl/releases/tag/v2.3.5) — bundle versions/checksums.
4. <a id="source-4"></a> [Lima v2.2.1 defaults](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/limayaml/defaults.go#L572) — precedence/fallback.
5. <a id="source-5"></a> [Lima v2.2.1 installer](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/cidata/cidata.TEMPLATE.d/boot.Linux/40-install-containerd.sh#L18) — mtime comparison.
6. <a id="source-6"></a> [Reuse](../../internal/lima/lima.go#L55) — `Ensure`, mount updates.
7. <a id="source-7"></a> [Resources](../../internal/lima/resources.go#L21) — resource-only update.
8. <a id="source-8"></a> [Disk attachment](../../internal/lima/disk_yaml.go#L16) — `setInstanceAccountDisk`.
9. <a id="source-9"></a> [Host lifecycle](../../cmd/yoloctl/controller_lifecycle.go#L137) — bundle/disk verification, teardown/recreation.
10. <a id="source-10"></a> [Deployment](../../cmd/yoloctl/controller_deploy.go#L139) — `startControllerSpace`.
11. <a id="source-11"></a> [Adapter](../../internal/vm/lima.go#L21) — current template.
12. <a id="source-12"></a> [Preservation regression](../../cmd/yoloctl/space_recreate_test.go#L94) — `TestSpaceRecreatePreservesAccountDiskAndManifests`.
13. <a id="source-13"></a> [nerdctl v2.2.2 creation](https://github.com/containerd/nerdctl/blob/v2.2.2/pkg/cmd/container/create.go) — no privileged-label write.
14. <a id="source-14"></a> [nerdctl v2.3.5 inspection](https://github.com/containerd/nerdctl/blob/v2.3.5/pkg/inspecttypes/dockercompat/dockercompat.go#L665) — label-derived boolean.
15. <a id="source-15"></a> [Classifier](../../internal/agentbox/privileged.go#L79) — `ClassifyRuntime` agreement.
16. <a id="source-16"></a> [Agreement regression](../../internal/agentbox/privileged_test.go#L279) — `TestClassifyRuntimeRequiresAgreeingEvidence`.
17. <a id="source-17"></a> [Parsing regression](../../internal/container/runtime_state_test.go#L9) — `TestInspectDoesNotInferUnreportedPrivilege`.
18. <a id="source-18"></a> [Host runner](../../scripts/smoke/lima_stack.py#L1) — owned fixture, cache wrapper, deadlines, cleanup and observation; `group_alive`/`settle_command` L183-L222 and `cleanup` L581-L602 retain verified cancellation before destruction.
19. <a id="source-19"></a> [Archives](space-archives.md) — explicit export boundary.
20. <a id="source-20"></a> [Runner tests](../../scripts/test_lima_stack.py#L1) — hermetic routing/failure checks; `CommandGroupTests` at L518 covers denied null signals, live/zombie groups, invalid inventories and preserved timeout settlement.
21. <a id="source-21"></a> [Go toolchains](https://go.dev/doc/toolchain#download) — exact toolchain names, module selection, private module-cache downloads and checksum verification.
