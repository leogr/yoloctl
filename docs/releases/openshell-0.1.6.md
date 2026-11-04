# OpenShell 0.1.6 upgrade — release notes

The upgrade is on `main`. Before merging, the combined build
passed its functional test on a real Linux VM (every box kind, operator boxes,
declared env, chat, ports, image paste, destroy after a space recreate, and the
old-VM-image refusal), the full repository check and a Lima operator-box proof;
the team's own space was then upgraded with it and runs on OpenShell 0.1.6. A
first macOS/Lima (arm64) trial ran boxes successfully and found the two issues
listed under "Known issues". It is not a qualified release: the full
Firecracker/Lima test matrix comes later.

## What changes

- In a native space, every **new ordinary box** uses the OpenShell 0.1.6
  runtime. yoloctl bundles the sandbox, the supervisor and its own helper for
  Linux amd64 and arm64; nothing is downloaded when a box is created.
- Boxes created with the old runtime **keep it across restart**. Creating a new
  ordinary box or explicitly running `box recreate` on an ordinary box selects
  the new runtime after its preflight. Installing this CLI does not convert
  running sessions.
- The old 0.0.72 runtime is retired from new VM images in the same build. A
  box created on the old runtime keeps starting only while its old binaries are
  still present in the VM; on a fresh or updated VM image it refuses to start
  with "recreate this box to move it to the new OpenShell". Run `box recreate`
  to move it; there is no automatic conversion and no way back. Privileged
  boxes are unaffected.
- A space whose VM was made by an older yoloctl cannot run this build's boxes,
  and yoloctl checks that before doing anything. In an interactive terminal it
  asks "This space's VM was made by an older yoloctl … Recreate VM NAME now?
  [y/N]" and, on yes, recreates the VM (on Firecracker with the new VM image,
  as `space recreate NAME --update-vm-image` would; on Lima as
  `space recreate NAME`) and then runs your command once. A script, or `no`,
  gets the exact command to run instead ("Run `yoloctl space recreate NAME
  --update-vm-image`" on Firecracker, "Run `yoloctl space recreate NAME`" on
  Lima) and nothing is changed. Either way accounts, box settings, declarations,
  the data disk and host workdirs are kept; running boxes, sessions and
  ephemeral workdirs stop; boxes need explicit starts afterwards. A new space
  prepares the right VM automatically.
- After installing a newer yoloctl, an existing new-runtime box keeps the
  helper of the build that created it and prints one notice ("runs the
  OpenShell helper of an earlier yoloctl build; recreate it to use this
  build's helper"); it keeps working, and `box recreate` moves it to the new
  helper. The same applies to the managed chat server and members.
- Create, start, shell/use, stop, restart, recreate and destroy are wired.
  Recreate replaces the container while keeping the declaration, accounts and
  configured workdir; sessions end, and ephemeral workdir data in the replaced
  container is lost. Destroy keeps the existing ephemeral-data confirmation and
  `--keep-manifest` behavior; it does not delete host projects or account state.

## Accepted limits (the only intended differences from the old runtime)

- **Custom image startup wrappers:** the native OpenShell path does not support
  a final image with an inherited `ENTRYPOINT`, including a base wrapper such as
  `custom-init` that performs setup and then calls `exec "$@"`. The default
  yoloctl base has no entrypoint and is unaffected. yoloctl refuses the selected
  final image before native setup or container creation, naming its configured
  entrypoint. Rebuild the base without `ENTRYPOINT`, or move setup into a feature
  or Dockerfile `RUN` step. yoloctl does not silently clear the entrypoint or
  skip the image's initialization.
- **Private proxy settings:** OpenShell 0.1.6 removes secret-provided
  HTTP_PROXY, HTTPS_PROXY, ALL_PROXY and NO_PROXY settings from the workload
  environment, including in unconfined boxes. Applications that relied on these
  settings may fail or fall back to other configuration. A command or tool can
  still configure its own proxy. Lowercase variants, `grpc_proxy` and
  `NODE_USE_ENV_PROXY` are also filtered.

## Minor differences (intended, no action needed)

- A box mount onto `/.openshell` or onto the helper directory is refused on
  the new path; those destinations belong to the runtime.
- Account environment secrets, chat settings and the box's declared `env`
  values reach the agent and SSH processes through a private bootstrap channel
  instead of the container environment; an account or chat value wins over a
  declared literal of the same name. Maintenance commands run with
  `nerdctl exec` do not see them; they never needed them. A box created before
  this fix takes it on `box recreate`.
- A port forward reaches the workload's own `127.0.0.1:<target>`, so a
  server bound to loopback only is now reachable through the forward.
  `box port list` also shows retained or restarting forward services.

## Also changed on the new path (tested on the VM)

- Boxes with operator authority (`yolo.space.operator`) work at the old path's
  safety level: the controller creates the operator socket and checks box name,
  container ID, ownership and incarnation on every request; `yoloctl box list`
  from inside such a box works.
- Destroying a new-runtime box whose container is gone (for example every
  native box after `space recreate`) follows the old cleanup: each step's own
  result decides, anything that could not be removed is named in a notice, and
  creating a box with the same name again works.

## Known issues (fixes in progress, not accepted losses)

- `box destroy --force` of a box that has a forwarded port could end as
  "uncertain" and hold the chat until host review. Fixed: the stopped forward
  unit is now collected and a leftover is only a warning; confirmed on Linux
  Lima with a host-side destroy, the original macOS operator-box case was not
  rerun separately.
- An extra read-only mount (`--mount SRC:DST:ro` inside a granted directory)
  appeared in the box but was not readable from inside the sandbox. Fixed: each
  declared extra mount is granted in the box policy with the access its bind
  promises; confirmed on a Firecracker VM (read-only file readable, write to it
  refused, read-write mount written through to the host, both listed by
  `box inspect`). It applies to boxes created or recreated with this build.
- Inside OpenShell-confined boxes (privileged boxes bypass this filter), GNU
  `make`, `nice` and `ionice` failed with "Operation not permitted" because
  OpenShell 0.1.6 denies set-ID and priority calls that v0.0.72 allowed. Fixed
  by the third yoloctl patch: confirmed in a Firecracker box on the rebuilt
  OpenShell (box start, `make`, `nice`, `ionice`, `ulimit`). Signalling a whole
  process group (`killpg`, `kill -TERM -<pgid>`, Python `os.killpg`) was still
  refused by OpenShell's signal mediation; the fourth yoloctl patch delivers a
  group signal unless the group is the sandbox's own; broadcast `kill(-1, sig)`
  stays refused. Confirmed in a Firecracker box on the rebuilt OpenShell (bash
  `kill -TERM -<pgid>`, Python `os.killpg`, `make`). Both fixes take effect
  after the OpenShell rebuild and a `box recreate` of each box.

## Known limits (not accepted losses)

- Supervisor-connection loss can terminate workloads after detection and the
  recovery timeout. This is not a general hang detector or an immediate-stop
  guarantee.
- Port-forwarding edge cases and interrupted-operation receipts on the new path
  were compared with the old path from the source; only the real-VM functional
  run above exercised them, not the full fault matrix; the forwarded-port
  destroy fix under "Known issues" is confirmed only on Linux Lima with a
  host-side destroy, and the extra-mount, set-ID and process-group fixes only on a Firecracker
  VM on an amd64 host; the arm64 build of the patched OpenShell compiles, but
  its upstream Rust test suite does not build for arm64, so arm64 runtime
  behaviour is unverified until a real arm64 host runs it.

## Building it yourself

`make build` compiles the two OpenShell programs from the pinned upstream
source plus yoloctl's four patches for Linux amd64 and arm64 on your machine,
using the pinned Rust toolchain through rustup, zig and cargo-zigbuild (see the
README development section); the first build takes several minutes and later
builds reuse the cached result; editing the Go helper's sources re-embeds the
helper automatically (a few seconds), the Rust build is not repeated. The bytes depend on your zig and cargo-zigbuild
versions and are recorded in the build receipt; they are not required to equal
the reviewed release archives.

## Later

The full Firecracker/Lima test matrix and native ARM workload coverage come
later; the first macOS/Lima (arm64) trial above is not that coverage. Bundled arm64 binaries and
cross-vet do not establish Mac runtime support.

Implementation evidence and current qualification limits are in the
[compatibility digest](../digests/openshell-compatibility.md).
