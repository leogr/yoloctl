# Go development environment

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; re-read cited sources when changing behavior.

[FACT] The `go` feature installs the latest stable official tarball at image
build under `/usr/local/go`, links `/usr/local/bin/go` and `gofmt` for `PATH`,
and requires `network.go-modules`. It sets `GOTOOLCHAIN=local` in
`/usr/local/go/go.env`, Go's installation default (Go 1.21+), so every go
process reads it however it was started; the build fails if that line is
absent. An explicit `GOTOOLCHAIN` in a process environment still overrides it.
No image `ENV` carries it. [1](#source-1) [4](#source-4)

[FACT] `network.go-modules` grants `proxy.golang.org:443` and
`sum.golang.org:443` to `/usr/local/go/bin/go` only. OpenShell attributes egress
to the resolved executable, so a grant naming the `/usr/local/bin/go` symlink
never matched and every module download was denied. Policy binaries therefore
name real files; a cross-check test fails when any capability grants a path
that a feature creates as a symlink. The policy golden pins the binary.
[2](#source-2) [3](#source-3) [4](#source-4)

[FACT] With `GOTOOLCHAIN=local` the box keeps the image's Go: a `go.mod` that
requires a newer Go fails with Go's own message naming `GOTOOLCHAIN=local`
until the box is recreated from a newer image. A downloaded toolchain would run
from a version-specific module-cache path that no policy can grant, so its
egress would be denied after the download. [1](#source-1) [5](#source-5)

[FACT] Reconcile compares the bind-mounted policy byte for byte with the desired
policy, so the changed grant is drift that restarts an existing box.
[6](#source-6)

[FACT] `make smoke-go` uses an isolated agentless box through confined SSH. It
checks the resolved binary, `GOTOOLCHAIN` and `GOSUMDB`, downloads,
checksum-verifies and builds a module dependency from an empty module cache,
refuses a newer `go.mod` locally, requires an attributed curl denial and no
denial of the go binary, and an in-sync box. The smoke's existence does not
establish a live pass. [5](#source-5)

## Sources

1. <a id="source-1"></a> [Go feature](../../internal/features/features.go#L107) — install fragment, description and `GOTOOLCHAIN=local`.
2. <a id="source-2"></a> [Go modules capability](../../internal/capabilities/capabilities.go#L259) — `network.go-modules` endpoints and binary.
3. <a id="source-3"></a> [Policy golden](../../internal/agentbox/testdata/policy_golden/go-modules.yaml#L28) — rendered `go-modules` grant.
4. <a id="source-4"></a> [Symlink cross-check](../../internal/features/symlink_test.go#L1) — `TestNoCapabilityGrantsAFeatureSymlink`, `TestGoFeatureKeepsItsToolchainAndGrantsTheRealBinary`.
5. <a id="source-5"></a> [Go smoke](../../scripts/smoke/go-modules.sh#L1) — module download, local toolchain and attributed denial.
6. <a id="source-6"></a> [Policy drift](../../internal/agentbox/diff.go#L97) — byte-equal policy comparison restarts on change.
