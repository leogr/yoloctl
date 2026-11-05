# Installation, releases and host homes

Verification: `4bc09137374e91d374ba6454cd8cef3d3da44240` and accompanying changes (installer output and space advice); other sections at `1d265df3`.

[FACT] Coordinated builds stamp hosts and Linux companions with one version and
random bundle. Only strict `vMAJOR.MINOR.PATCH[-PRERELEASE]` selects release;
other stamps remain dev. VM init stays separately hash-verified. [1](#source-1)

[FACT] Native `spaceenv` roots win. Otherwise an absolute, clean `YOLOCTL_HOME`
selects config and `<home>/cache`; without it every build uses `yoloctl` under
the XDG/OS bases, so existing development spaces stay visible and a separate
dev home is opt-in; never move a live Lima home. Selection is lexical: aliases and explicit shared homes are
not ownership boundaries. Long Lima socket paths can exceed macOS's 104 bytes.
A test binary drops an inherited `YOLOCTL_HOME` before any test (`layout`
init), so XDG-isolated fixtures and subprocesses never write a live home.
[2](#source-2)

[FACT] `version` reports channel/config/cache roots without creating directories;
its Lima probe is bounded. `install` also skips directory setup. Other commands retain normal setup. Infrastructure/native
dispatch and operator checks remain first; the palette adds no marker/version.
[3](#source-3)

[FACT] Smoke entries replace inherited `YOLOCTL_HOME` with their owned fixture;
common setup and the standalone Lima stack gate read actual roots from
`version --json`. Children inherit them.
Hostile-home script checks use a sentinel, without a VM. [4](#source-4)

## Bundles and publication

[FACT] `make dist VERSION=vX.Y.Z` produces four archives in one invocation,
named `yoloctl-<os>-<arch>.tar.gz` without the version (so
`releases/latest/download/<name>` resolves); each extracts to one
`yoloctl-<os>-<arch>/` folder that contains both Linux companions, `LICENSE`,
`INSTALL.txt`, `MANIFEST.json` and host `yoloctl`; linux/amd64 adds VM init. Main’s `native-embed` prepares
OpenShell helper/runtime archives inside every CLI. The helper source closure
includes `buildinfo`, imported by layout. Manifest payload hashes exclude
itself; `SHA256SUMS` covers final archives. Verification rejects extra entries,
symlinks, missing files and linked identity/hash mismatches. [5](#source-5)

[FACT] Manual release runs are read-only QA. Strict tags gate the same commit
and host installer tests. Only publication has write/OIDC and Actions-read
permissions: it verifies artifact ID/run/digest and final manifests, attests
archives/checksums and creates a draft, never overwriting or retrying; the
human publishes. Attestation failure blocks drafting. CI build and install-smoke
jobs cache only `bin/openshell/<key>` (exact key plus OS, arch and toolchain-script
hash; qualify still runs); `dist` always builds OpenShell from source. [6](#source-6)

[FACT] Darwin bytes ship as linked; macOS CI requires arm64 `codesign -v`,
native execution and manifest equality. Intel execution, Gatekeeper and
notarization are outside this flow. [6](#source-6)

## Installer and space advice

[FACT] Hidden `install` refuses dev builds, native/controller context, operator
clients, `OPENSHELL_SANDBOX`, Linux container markers and invalid bundles before
writing. Absence of signals cannot prove host execution. Unknown executables are
fingerprinted, never executed. Non-TTY without `--yes` prints only; unknown
conflicts require terminal confirmation; `--yes` accepts managed upgrades only.
Output: one `✓ Installed yoloctl V → LINK (was OLD)` line, `!` warnings
(PATH shadowing, unknown build, downgrade), `✗` failures (a prerequisite only
when it fails); TUI dark-theme colors only on a terminal without `NO_COLOR` or
`TERM=dumb`. [7](#source-7)

[FACT] Shell setup (PATH and completion blocks) is in
[install shell setup](install-shell-setup.md). [10](#source-10)

[FACT] Publication rejects bin-directory overlap with source/store trees, including
aliases and missing descendants, before mutation. A symlinked prefix (a home or
`~/.local` behind a symlink) is resolved first (`CanonicalPath`); a store or
bin directory that is itself a symlink is still refused, never written through. It locks
`~/.local/share/yoloctl/install.lock`, stages a complete
bundle through no-follow descriptor writes/fsync, publishes its immutable
`bundles/<version>-<os>-<arch>-<bundle>` key without replacement (without the
no-replace rename flag, e.g. FUSE replying EINVAL: an absence check and plain
rename under the lock, which could replace an empty directory created in
between; `publishDirectory`), then atomically
swaps `<bin-dir>/yoloctl` (default `~/.local/bin`). A bin directory whose
filesystem refuses symbolic links (EPERM/ENOTSUP) fails with an error naming it
and asking for a local-disk `--bin-dir`; the published bundle stays. Corrupt/different existing keys
fail; identical bytes are a no-op. Old bundles, failed stages and explicitly
replaced unknown regular executables are retained. Nothing is pruned or called
safe to delete. [8](#source-8)

[FACT] Install never runs a space command. A read-only inventory (no boot,
lock or write) prints a block per space needing something; commands use the
install link and an explicit `YOLOCTL_HOME`; print-only and `--dry-run` say the
link still selects the old build. Bundle mismatch advises
`space start S --update-controller` (stopped: it starts the VM). A Lima VM
predating this build's OpenShell provisioning (`lima.CheckOpenShellVM`) gets
`space recreate S` instead. Proved Lima pin differences or an unpinned stack add
an optional recreate; unprovable configuration is `✗`. A Firecracker image
equal to this build's default needs nothing; an absent or known different
default adds an optional `space recreate S --update-vm-image`, never calling
the image current or obsolete; other read errors are `✗`. Pending checkpoints
show only recovery guidance. [9](#source-9)

## Sources

1. <a id="source-1"></a> [Identity](../../internal/buildinfo/buildinfo.go#L1-L47), `IsRelease`, `Channel`;
   [Make flags](../../Makefile#L12-L58).
2. <a id="source-2"></a> [Roots](../../internal/layout/layout.go#L96-L151), `ConfigRoot`, `CacheRoot`, `selectedHome`;
   [matrix](../../internal/layout/homes_test.go#L1-L91); test-binary isolation in [testhome.go](../../internal/layout/testhome.go#L1), sentinel regression in [home_isolation_test.go](../../internal/layout/home_isolation_test.go#L1).
3. <a id="source-3"></a> [Startup](../../cmd/yoloctl/main.go#L69-L110), `main`;
   [routing](../../cmd/yoloctl/main.go#L225-L230), `readOnlyStartup`;
   [bounded probe](../../cmd/yoloctl/version.go#L88-L107).
4. <a id="source-4"></a> [Smoke roots](../../scripts/smoke/common.sh#L111-L136), `smoke_init`;
   [sentinel regression](../../scripts/test_vm_smoke.py#L61-L90);
   [Lima gate](../../scripts/smoke/lima_stack.py#L380), `identity`, `setup`, `check`;
   [gate regressions](../../scripts/test_lima_stack.py#L1).
5. <a id="source-5"></a> [Manifest](../../internal/releasebundle/manifest.go#L1-L156), `Validate`, `Verify`, `VerifyLinked`;
   [packager](../../scripts/dist.go#L1-L146), `run`;
   [native preparation](../../Makefile#L48-L112), `native-embed`;
   [helper inputs](../../helperinputs.go#L27-L33), production closure.
6. <a id="source-6"></a> [Toolchain setup](../../scripts/ci-openshell-toolchain.sh#L1);
   [Workflow](../../.github/workflows/release.yml#L1-L212), OpenShell cache in `install-smoke`;
   [CI](../../.github/workflows/ci.yml#L38-L52), OpenShell cache in `build`;
   [publication](../../scripts/publish-release.py#L1-L199), `identity`, `verify_artifact`, `main`;
   [archive checks](../../scripts/check-dist.go#L1-L172), `run`, `extract`.
7. <a id="source-7"></a> [CLI](../../cmd/yoloctl/install.go#L1-L279), `newInstallCmd`, `installDecision`, `installPathHint`, `plainPathRune`; `installContainerSignal` in install_host_linux.go;
   [output](../../cmd/yoloctl/install_output.go#L1-L133), `installOut`, `installColor`, `installCommand`.
8. <a id="source-8"></a> [Publication engine](../../internal/installer/engine.go#L1-L476), `Inspect`, `Publish`, `publishDirectory`
   (platform `renameNoReplace`/`noReplaceUnsupported` in rename_linux.go and rename_darwin.go);
   [no-replace fallback regressions](../../internal/installer/publish_fallback_test.go#L1);
   [destination boundary](../../internal/installer/paths.go#L1-L87), `ValidatePaths`;
   [fault/concurrency regressions](../../internal/installer/engine_test.go#L1-L278);
   [overlap regressions](../../internal/installer/paths_test.go#L1-L193).
9. <a id="source-9"></a> [Space advice](../../cmd/yoloctl/install_migration.go#L1-L292), `installSpaceAdvice`, `printSpaceAdvice`, `installLimaAdvice`;
   [inventory](../../cmd/yoloctl/install_inventory.go#L1-L48), `consentSnapshot`, `readInstallConsent`;
   [default metadata](../../internal/firecracker/recipe_linux.go#L60-L88), `ReadDefaultImage`.
10. <a id="source-10"></a> [Install shell setup](install-shell-setup.md).
