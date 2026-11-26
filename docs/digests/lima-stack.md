# Lima container stack and upgrades

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior. Native bind startup re-read at `2c911fb23783c65f13488438858f36f873aaee09` and accompanying changes.

Container stack pins and provisioning verified on `fcd4a112bc26c8316d9ecf9c0e216a5fee9c0f22`
and accompanying changes.

[FACT] New/recreated Lima VMs use system containerd, disable user containerd,
and select nerdctl-full 2.4.2 by **guest** architecture. No Go build-architecture,
VM backend or mount-transport override is added. The decoded regression covers
fresh/preserved data-disk rendering and equality with Firecracker's amd64 pin.
[1](#source-1) [2](#source-2)

| Lima architecture | Full archive | SHA256 |
|---|---|---|
| x86_64 | nerdctl-full-2.4.2-linux-amd64.tar.gz | 1f12c1a5840744fdf7029483c55a69813d44aa2ec1ef879d52d81ff6a3049ecd |
| aarch64 | nerdctl-full-2.4.2-linux-arm64.tar.gz | fe0ef0e9840ee8932640e6058f84b7fa98050f9726e44af44883a5b804ae830e |

[FACT] The full release bundles containerd 2.4.2, runc 1.5.2, CNI 1.9.1 and
BuildKit 0.34.1. When `buildg` is present after Lima installs the selected
archive, provisioning finds it beside the guest-agent executable recorded in
Lima's systemd service and hashes the selected archive at
`/mnt/lima-cidata/nerdctl-full.tgz`. Lima 2.2.1 and 2.3.0 use that path for
defaults and overrides. For a qualified pin,
it removes the checksummed debugger and refuses an unexpected leaf. This leaves
`nerdctl builder debug` unavailable on the pinned stack. A selected archive
whose digest differs keeps its files and is logged as unqualified. If the
archive cannot be identified, provisioning leaves the debugger and fails
readiness. An absent debugger requires only the absence check. Readiness also
requires a working engine. Existing instances keep their saved provisioning
script until recreate.
[3](#source-3) [6](#source-6) [11](#source-11) [24](#source-24) [25](#source-25) [26](#source-26) [31](#source-31) [32](#source-32) [33](#source-33) [34](#source-34) [35](#source-35)

[FACT] On Lima and Firecracker, nerdctl 2.4.2 makes a read-only box bind
recursively read-only when the guest kernel and OCI runtime support it. This
also makes mounts below the source read-only inside the box. The native
operator projection accepts one `ro` or `rro` option with the bind flag and
continues to refuse `rw`. Its two inspections each get a two-minute
allowance, shortened by an earlier caller deadline or cancellation.
[27](#source-27) [28](#source-28) [29](#source-29) [30](#source-30)

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

[FACT] nerdctl 2.4.2 derives its privilege boolean from a label older creators
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

[FACT] Nested virtualization is an instance-file boot setting, independent of
the retained stack provisioning. The guarded resource update also sets its explicit
boolean on macOS before starting new or stopped instances; it does not rewrite
the creation template or install guest packages. The preference and eligibility
contract is recorded in the adapter digest. [22](#source-22)

[FACT] Fixed root service startup restores and verifies the native account bind
before serving, including after a guest reboot. Workers/recovery remain verify-only.
Disk/owner/target errors fail startup; systemd retries are unchanged. [23](#source-23)

## Sources

1. <a id="source-1"></a> [Template](../../assets/lima/yoloctl.yaml.tmpl#L31) — system modes, architecture archives.
2. <a id="source-2"></a> [Decoded renders](../../internal/lima/template_test.go#L14) — `TestTemplatePinsFullContainerStackForBothGuestArchitectures`.
3. <a id="source-3"></a> [nerdctl v2.4.2](https://github.com/containerd/nerdctl/releases/tag/v2.4.2) — bundle versions/checksums.
4. <a id="source-4"></a> [Lima v2.2.1 defaults](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/limayaml/defaults.go#L572) — precedence/fallback.
5. <a id="source-5"></a> [Lima v2.2.1 installer](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/cidata/cidata.TEMPLATE.d/boot.Linux/40-install-containerd.sh#L18) — mtime comparison.
6. <a id="source-6"></a> [Reuse](../../internal/lima/lima.go#L55) — `Ensure`, mount updates.
7. <a id="source-7"></a> [Resources](../../internal/lima/resources.go#L21) — resource-only update.
8. <a id="source-8"></a> [Disk attachment](../../internal/lima/disk_yaml.go#L16) — `setInstanceAccountDisk`.
9. <a id="source-9"></a> [Host lifecycle](../../cmd/yoloctl/controller_lifecycle.go#L215-L258) — bundle/disk verification, teardown/recreation.
10. <a id="source-10"></a> [Deployment](../../cmd/yoloctl/controller_deploy.go#L139) — `startControllerSpace`.
11. <a id="source-11"></a> [Adapter](../../internal/vm/lima.go#L21) — current template.
12. <a id="source-12"></a> [Preservation regression](../../cmd/yoloctl/space_recreate_test.go#L94) — `TestSpaceRecreatePreservesAccountDiskAndManifests`.
13. <a id="source-13"></a> [nerdctl v2.2.2 creation](https://github.com/containerd/nerdctl/blob/v2.2.2/pkg/cmd/container/create.go) — no privileged-label write.
14. <a id="source-14"></a> [nerdctl v2.4.2 inspection](https://github.com/containerd/nerdctl/blob/v2.4.2/pkg/inspecttypes/dockercompat/dockercompat.go#L690) — label-derived boolean.
15. <a id="source-15"></a> [Classifier](../../internal/agentbox/privileged.go#L87) — `ClassifyRuntime` agreement.
16. <a id="source-16"></a> [Agreement regression](../../internal/agentbox/privileged_test.go#L279) — `TestClassifyRuntimeRequiresAgreeingEvidence`.
17. <a id="source-17"></a> [Parsing regression](../../internal/container/runtime_state_test.go#L9) — `TestInspectDoesNotInferUnreportedPrivilege`.
18. <a id="source-18"></a> [Host runner](../../scripts/smoke/lima_stack.py#L1) — owned fixture, cache wrapper, deadlines, cleanup and observation; `group_alive`/`settle_command` L183-L222 and `cleanup` at L594 retain verified cancellation before destruction.
19. <a id="source-19"></a> [Archives](space-archives.md) — explicit export boundary.
20. <a id="source-20"></a> [Runner tests](../../scripts/test_lima_stack.py#L1) — hermetic routing/failure checks; `CommandGroupTests` at L581 covers denied null signals, live/zombie groups, invalid inventories and preserved timeout settlement.
21. <a id="source-21"></a> [Go toolchains](https://go.dev/doc/toolchain#download) — exact toolchain names, module selection, private module-cache downloads and checksum verification.

22. <a id="source-22"></a> [Boot resource application](../../cmd/yoloctl/space_nested_virtualization.go#L1) — `applySpaceBootResources`, verified with accompanying changes on fc48bca3.

23. <a id="source-23"></a> [Controller startup](../../cmd/yoloctl/controller_internal.go#L287) — `loadControllerActionRuntime`; internal/controller/runtime.go:`PrepareRuntimeForLifecycle`; internal/controller/service.go:`ServiceUnit`.
24. <a id="source-24"></a> [Lima boot order](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/cidata/cidata.TEMPLATE.d/boot.sh#L64-L180) — distribution boot files run before system provisioning.
25. <a id="source-25"></a> [Archive qualification](../../assets/lima/yoloctl.yaml.tmpl#L136) — selected archive digest, verified removal and per-boot readiness result.
26. <a id="source-26"></a> [Lima cidata](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/cidata/cidata.go#L126) — `templateArgs` gives the selected archive a generic name.
27. <a id="source-27"></a> [nerdctl mount options](https://github.com/containerd/nerdctl/blob/v2.4.2/pkg/mountutil/mountutil_linux.go#L120-L140) — `readOnlyMountOptions` selects recursive read-only or falls back to plain read-only.
28. <a id="source-28"></a> [nerdctl recursive read-only support](https://github.com/containerd/nerdctl/blob/v2.4.2/pkg/ociruntimeutil/rro_linux.go#L48-L70) — kernel and OCI runtime checks.
29. <a id="source-29"></a> [Native operator projection](../../internal/container/operator_projection.go#L137) — `parseNativeOperatorProjection` checks read-only and bind options on the recorded OCI mount.
30. <a id="source-30"></a> [Projection reads](../../internal/container/operator_projection.go#L55-L85) — `NativeOperatorProjection` bounds each read and requires matching observations.
31. <a id="source-31"></a> [Lima v2.3.0 cidata](https://github.com/lima-vm/lima/blob/v2.3.0/pkg/cidata/cidata.go#L133) — generic archive name.
32. <a id="source-32"></a> [Linux cidata mount](https://lima-vm.io/docs/dev/internals/#cidataiso) — guest path and archive name.
33. <a id="source-33"></a> [Lima guest-agent unit](https://github.com/lima-vm/lima/blob/v2.3.0/cmd/lima-guestagent/lima-guestagent.TEMPLATE.service#L5) — configured executable.
34. <a id="source-34"></a> [Lima v2.3.0 guest-agent installer](https://github.com/lima-vm/lima/blob/v2.3.0/pkg/cidata/cidata.TEMPLATE.d/boot.Linux/25-guestagent-base.sh#L20-L27) — same prefix as the container stack.
35. <a id="source-35"></a> [Lima v2.2.1 guest-agent installer](https://github.com/lima-vm/lima/blob/v2.2.1/pkg/cidata/cidata.TEMPLATE.d/boot.Linux/25-guestagent-base.sh#L20-L27) — same prefix as the container stack.
