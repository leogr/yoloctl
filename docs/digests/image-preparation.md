# Controller-owned image preparation

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes. Private exporter compression and its space setting verified on `f5928f39223f8851c230b898d6eb2fcfab16cc2c` and accompanying changes.

[FACT] The VM controller owns jobs capped at
`max(runtime.NumCPU()-1, 1)` tracked attempts. Copied Dockerfile/context bytes, modes, image digests, platforms and arguments
form input identity;
force work has a controller-allocated nonce. Ordinary jobs share work; force callers share only before build. Desired/base reads (including cached/new destinations) honor publication holds. Consumers keep separate apply receipts/interests. [1](#source-1) [2](#source-2) [12](#source-12) [13](#source-13)

[FACT] Requesters receive bounded independent stderr build progress, replay for
joiners and quiet-phase feedback; see [image build progress](image-progress.md).

[FACT] Preparation misses unwind space/entity guards; the driver warms then
revalidates, with two re-preparation loops. Lifecycle, cold attach, ports,
documents and chat use this boundary; holder attachment does not build.
Managed-base preparation precedes composed jobs without waiting for another slot.
Unmanaged no-install runtime pulls stay locked. [3](#source-3)
[4](#source-4) [5](#source-5) [14](#source-14) [15](#source-15) [16](#source-16) [17](#source-17)

[FACT] On a native box, the pre-effect checks of `box edit`, `box set` and
mutate-and-reconcile run in the native creation mode (`defaultCreationContext`),
like the reconcile's later creation preflight, so a cached image is retained
before apply begins and the first apply after an edit succeeds. [33](#source-33)

[FACT] A managed request keeps its inherited view channel open across image
preparation and npm-probe revalidation. The request owns one descriptor and
closes it on return; individual passes preserve queued input and the eventual
session attachment. [3](#source-3)

[FACT] Every solve receives a never-reused private output name. The closed exporter permits one private image, no push/canonical aliases, and
only inline cache. By default newly created layers are written uncompressed, because they are unpacked in the same VM and never pushed; a space whose bootstrap says `image_layer_compression: gzip` (host `space set --image-layer-compression gzip`, carried in the controller context at the next start) omits the attribute and keeps BuildKit's gzip. Existing layer blobs are untouched because `force-compression` is not set (`privateExporterAttrs`, `privateLayerCompression`; `spaceenv.Context.Validate`; [BuildKit v0.31.2 image exporter options](https://github.com/moby/buildkit/blob/v0.31.2/README.md#L292-L296)). It checks the returned
name and OCI descriptor against containerd, leases the retained content graph,
and records completion before consumers proceed. Local inputs have an independent
private alias/lease. BuildKit-parsed syntax frontend and FROM/COPY/RUN-mount inputs enter
identity; Dockerfile bytes remain unchanged. Source policy converts known images
to immutable locators and denies unknown images. Converted images also get
frontend named contexts bound to those locators, except when the normalized
name matches any Dockerfile stage alias. These collisions retain source-policy
conversion: a named context would replace the stage and skip its instructions.
Noncolliding contexts resolve immutable metadata directly. [30](#source-30)
[31](#source-31) [32](#source-32)
Builds, base refresh and pulls stay private until publication. [6](#source-6) [23](#source-23) [24](#source-24)

[FACT] BuildKit connects after service startup and bounded readiness checks;
pre-Solve connection failure reports not started. Its RPCs strip containerd's
namespace header; direct containerd calls use default. [29](#source-29)

[FACT] Only the controller publishes public tags after apply permission, scope
expansion and the native guard. A newly prepared managed base is published under its canonical name with its composed output. Different recipes retain separate results. Create passes the retained OCI target digest to nerdctl; `yoloctl.runtime_image` records the desired recipe reference for drift.
Later tag movement cannot substitute another job's bytes. The last consumer
releases the controller's private reference and lease; completed jobs record `released_at`, while `abandoned_at` records actual abandonment. [1](#source-1) [2](#source-2)
[3](#source-3) [7](#source-7) [18](#source-18)

[FACT] A cancelled requester leaves without cancelling other interested callers.
Last-requester abandonment durably revokes the private attempt, immediately
releases its slot and same-input admission, and requests cancellation without
waiting for BuildKit. Its result can remain unknown. Late output cannot be consumed/published and
may reappear as private garbage.
The cap counts tracked attempts; residual resource use can accumulate. Returned
solve failures keep their real error and failed completion; cancellation/lost
transport preserves its cause and unknown outcome. [1](#source-1) [6](#source-6) [8](#source-8)

[FACT] Startup preserves already-durable image completion and application
receipts, abandons unfinished qualified private attempts, and frees admission
without history polling, grace, or acknowledgment. Durable orphan facts survive receipt eviction and appear in operation status
and logs. Startup sweeps private names/leases while retaining legacy effects and
residual-work accounting. [6](#source-6) [8](#source-8) [19](#source-19)

[FACT] Proof version 2 names image resources. The unnamed images writer overlaps
all image accesses; a named writer overlaps an unnamed writer or the same named
resource, but not another image's compatibility domain read. Version-1 holds stay conservative; HMACs use recorded versions. Older controllers
reject v2 receipts; rollback requires supported storage procedures. [9](#source-9) [20](#source-20) [21](#source-21)

[FACT] Preflight has a durable private permit. Deleting low-space cleanup owns
an independent images-domain receipt, checks holds/drain/storage, and waits
outside native guards for tracked build effects. Its short-lived worker takes a
brief native guard, reports acquisition through private inherited pipes, then
receives durable permission and verifies disk/generation/permit before deletion. Failed or
cancelled cleanup preserves uncertainty; private preflight does not reserve an
output. Ended metadata owners settle their failure ledger for storage repair,
without claiming a remote solve stopped. Service and bounded repair contexts are
separate. [1](#source-1) [3](#source-3) [10](#source-10) [25](#source-25) [26](#source-26)

[FACT] Npm latest probes run outside native/entity guards and bind memory-only
results/failures to profile and catalog snapshot. Cancellation stores no result;
native probes, installed-version reads and installs remain in apply. No-agent and
no-autoupdate skip probing. Blocker notices use the controller's acquired lock
token/context or actual tracked jobs, bounded method/box/known-digest summaries,
and generic fallback after owner death. Advisory data never grants permission.
[27](#source-27) [28](#source-28) [4](#source-4)

[FACT] An interactive retry prompt requires typed controller evidence that the
image job and this request never received effect permission. `y` sends a new
request ID through normal admission; N, EOF, non-TTY and caller cancellation do
not retry. Lost application replies retain the normal fence contract.
[2](#source-2) [11](#source-11) [22](#source-22)

## Sources

1. <a id="source-1"></a> [image_jobs.go](../../internal/controller/image_jobs.go#L1) — `ImageJobs`, `Acquire`, `leave`, `run`.
2. <a id="source-2"></a> [server.go](../../internal/controller/server.go#L1) — private image callback and finalization.
3. <a id="source-3"></a> [controller_images.go](../../cmd/yoloctl/controller_images.go#L1) — prepare driver and consumption.
4. <a id="source-4"></a> [space_operation.go](../../cmd/yoloctl/space_operation.go#L1) — deferred apply gate.
5. <a id="source-5"></a> [controller_documents.go](../../cmd/yoloctl/controller_documents.go#L1) — document apply preparation.
6. <a id="source-6"></a> [image_backend.go](../../internal/controller/image_backend.go#L1) — exporter, pinning, publication, pull and sweep.
7. <a id="source-7"></a> [live.go](../../internal/agentbox/live.go#L1) — immutable consumption and desired-image drift.
8. <a id="source-8"></a> [receipts.go](../../internal/controller/receipts.go#L1) — recovery and eviction.
9. <a id="source-9"></a> [holds.go](../../internal/controller/holds.go#L1) — image overlap and abandonment.
10. <a id="source-10"></a> [controller_storage.go](../../cmd/yoloctl/controller_storage.go#L1) — preflight worker.
11. <a id="source-11"></a> [controller_client.go](../../cmd/yoloctl/controller_client.go#L1) — interactive retry.
12. <a id="source-12"></a> [recipe.go](../../internal/imagejob/recipe.go#L1) — canonical copied inputs.
13. <a id="source-13"></a> [image_channel.go](../../internal/controller/image_channel.go#L1) — private transport.
14. <a id="source-14"></a> [agentbox.go](../../cmd/yoloctl/agentbox.go#L1) — lifecycle preflight.
15. <a id="source-15"></a> [agentbox_reconcile.go](../../cmd/yoloctl/agentbox_reconcile.go#L1) — set/reconcile preparation.
16. <a id="source-16"></a> [chat_runtime.go](../../cmd/yoloctl/chat_runtime.go#L1) — server/member preflight.
17. <a id="source-17"></a> [session_client.go](../../cmd/yoloctl/session_client.go#L1) — cold attach.
18. <a id="source-18"></a> [prepared.go](../../internal/agentboximage/prepared.go#L1) — trusted consumption hook.
19. <a id="source-19"></a> [image_orphans.go](../../internal/controller/image_orphans.go#L1) — independent accounting.
20. <a id="source-20"></a> [recovery.go](../../internal/controller/recovery.go#L1) — recorded proof versions.
21. <a id="source-21"></a> [images_scope_test.go](../../internal/controller/images_scope_test.go#L1) — mixed-version/overlap tests.
22. <a id="source-22"></a> [client.go](../../internal/controller/client.go#L1) — typed retry response.
23. <a id="source-23"></a> [dependencies.go](../../internal/imagejob/dependencies.go#L1) — external image graph.
24. <a id="source-24"></a> [image_sources.go](../../internal/controller/image_sources.go#L1) — immutable policy.
25. <a id="source-25"></a> [image_cleanup.go](../../internal/controller/image_cleanup.go#L1) — cleanup permission.
26. <a id="source-26"></a> [recovery_scheduler.go](../../internal/controller/recovery_scheduler.go#L1) — ledger repair.
27. <a id="source-27"></a> [autoupdate_prepare.go](../../cmd/yoloctl/autoupdate_prepare.go#L1) — npm preparation.
28. <a id="source-28"></a> [advisory.go](../../internal/controller/advisory.go#L1) — blocker ownership.
29. <a id="source-29"></a> [image_connection.go](../../internal/controller/image_connection.go#L1) — namespace isolation and readiness.
30. <a id="source-30"></a> [image_backend.go](../../internal/controller/image_backend.go#L103) — `privateSolveOptions`, immutable context targets and stage exclusions.
31. <a id="source-31"></a> [image_sources.go](../../internal/controller/image_sources.go#L19) — `dockerfileStageContexts`, frontend parser and normalized alias exclusions.
32. <a id="source-32"></a> [image_frontend_test.go](../../internal/controller/image_frontend_test.go#L62) — `TestImageNamedContextsPreserveDockerfileStageInstructions`, actual frontend graph with synthetic metadata and production policy.
33. <a id="source-33"></a> [box_edit_native_warm_test.go](../../cmd/yoloctl/box_edit_native_warm_test.go#L1) — `TestNativeBoxEditPreEffectCheckRetainsWhatReconcileRequires`, `TestBoxMutationPreEffectChecksUseTheCreationMode`; call sites in `controller_documents.go` (edit apply) and `agentbox_reconcile.go` (`runSet`, `mutateAndReconcile`) on main `1d265df3` and accompanying changes.
