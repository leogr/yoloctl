# Installation, releases and host homes

Verification: `1d265df3cfae49026ec4202fe88e9cce83ad1ca9` and accompanying changes; review fixes on `1d265df3` (option B home, test isolation, symlinked prefixes, interruption, too-old Lima template) and accompanying changes.

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
its Lima probe is bounded. `install` and guarded installer lifecycle starts also
skip directory setup. Other commands retain normal setup. Infrastructure/native
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

## Installer and migrations

[FACT] Hidden `install` refuses dev builds, native/controller context, operator
clients, `OPENSHELL_SANDBOX`, Linux container markers and invalid bundles before
writing. Absence of signals cannot prove host execution. Prerequisites and PATH
shadowing are reported read-only; after installing, a bin directory missing
from PATH gets one line to add it for `$SHELL` (bash/zsh rc `export`, fish
`fish_add_path`, else generic; `installPathHint`). Unknown executables are
fingerprinted, never executed. Non-TTY without `--yes` prints only; unknown conflicts require terminal
confirmation. `--yes` accepts managed upgrades only, never migrations.
[7](#source-7)

[FACT] Publication rejects bin-directory overlap with source/store trees, including
aliases and missing descendants, before mutation. A symlinked prefix (a home or
`~/.local` behind a symlink) is resolved first (`CanonicalPath`); a store or
bin directory that is itself a symlink is still refused, never written through. It locks
`~/.local/share/yoloctl/install.lock`, stages a complete
bundle through no-follow descriptor writes/fsync, publishes its immutable
`bundles/<version>-<os>-<arch>-<bundle>` key without replacement (a filesystem
without the no-replace rename flag, e.g. a FUSE mount replying EINVAL, gets an
absence check plus a plain rename under the lock; unlike the flag, that would
replace an empty directory a foreign process creates in between;
`publishDirectory`), then atomically
swaps `<bin-dir>/yoloctl` (default `~/.local/bin`). A bin directory whose
filesystem refuses symbolic links (EPERM/ENOTSUP) fails with an error naming it
and asking for a local-disk `--bin-dir`; the published bundle stays. Corrupt/different existing keys
fail; identical bytes are a no-op. Old bundles, failed stages and explicitly
replaced unknown regular executables are retained. Nothing is pruned or called
safe to delete; rc files are not edited. [8](#source-8)

[FACT] Read-only proposals print absolute link/home commands; in print-only or
`--dry-run` mode they say nothing is installed yet, since the link still selects
the old build. A Lima VM whose template predates this build's OpenShell
provisioning (read-only `lima.CheckOpenShellVM`) gets one `space recreate S`
proposal instead of a controller update known to fail. Bundle mismatch
requires `space start S --update-controller`; stopped starts explicitly boot.
Proved Lima pin differences (or an unpinned stack) can offer recreation after
the update; inherited, global or unreadable configuration and other missing
evidence stay UNKNOWN. Firecracker image origin is unrecorded: only equality
with this build's default needs no action, anything else is UNKNOWN with a
manual `space recreate S --update-vm-image` hint. Pending checkpoints show only
recovery guidance. [9](#source-9)

[FACT] Terminal execution asks per command, then compares bundle, state, disk
identity, generation and bootstrap digest under the existing lifecycle lock;
missing evidence exits 76 without mutation. Running consent forbids the boot
fallback. Only the private pipe receipt advances consent; stdout cannot. An
approved recreate runs with `--force`. An interruption is never a skip: the
child gets SIGINT (bounded wait), guarded checks return the cancellation and
prompting stops. Skips continue; failures stop without replay. [10](#source-10)

## Sources

1. <a id="source-1"></a> [Identity](../../internal/buildinfo/buildinfo.go#L1-L47), `IsRelease`, `Channel`;
   [Make flags](../../Makefile#L12-L58).
2. <a id="source-2"></a> [Roots](../../internal/layout/layout.go#L96-L151), `ConfigRoot`, `CacheRoot`, `selectedHome`;
   [matrix](../../internal/layout/homes_test.go#L1-L91); test-binary isolation in [testhome.go](../../internal/layout/testhome.go#L1), sentinel regression in [home_isolation_test.go](../../internal/layout/home_isolation_test.go#L1).
3. <a id="source-3"></a> [Startup](../../cmd/yoloctl/main.go#L69-L110), `main`;
   [routing](../../cmd/yoloctl/main.go#L226-L244), `readOnlyStartup`;
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
7. <a id="source-7"></a> [CLI](../../cmd/yoloctl/install.go#L1-L256), `newInstallCmd`, `installDecision`, `installPathHint`, `plainPathRune`; `installContainerSignal` in install_host_linux.go.
8. <a id="source-8"></a> [Publication engine](../../internal/installer/engine.go#L1-L476), `Inspect`, `Publish`, `publishDirectory`
   (platform `renameNoReplace`/`noReplaceUnsupported` in rename_linux.go and rename_darwin.go);
   [no-replace fallback regressions](../../internal/installer/publish_fallback_test.go#L1);
   [destination boundary](../../internal/installer/paths.go#L1-L87), `ValidatePaths`;
   [fault/concurrency regressions](../../internal/installer/engine_test.go#L1-L278);
   [overlap regressions](../../internal/installer/paths_test.go#L1-L193).
9. <a id="source-9"></a> [Proposals](../../cmd/yoloctl/install_migration.go#L1-L318), `installProposals`, `installImageNote`, `installLimaAdvice`;
   [default metadata](../../internal/firecracker/recipe_linux.go#L60-L88), `ReadDefaultImage`.
10. <a id="source-10"></a> [Guard](../../cmd/yoloctl/install_precondition.go#L1-L140), `checkInstallPrecondition`, `writeInstallReceipt`;
   [deployment](../../cmd/yoloctl/controller_deploy.go#L170-L280), `startControllerSpaceImpl`;
   [execution](../../internal/installmigration/migration.go#L1-L132), `Execute`;
   [compatibility offer](../../cmd/yoloctl/space_vm_compatibility.go#L87), `offerSpaceVMRecreate`.
