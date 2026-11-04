# Installing yoloctl

The [README](../README.md) has the one-line install for each platform. This
guide explains what that line does, how to verify a download first, how
upgrades and space updates work, where builds keep their data, and how
releases are made. Implementation evidence: [build identity and
installation](digests/installation.md).

## The one-line install

`curl` downloads `yoloctl-<os>-<arch>.tar.gz` from the latest published
release (GitHub's `releases/latest/download/` link points at the newest
release that is neither a draft nor a pre-release), `tar` unpacks it into a
folder of the same name, and `yoloctl install` inside that folder installs the
bundle. Each archive holds the host `yoloctl`, the Linux companions used inside
the VM, `LICENSE`, `INSTALL.txt` and `MANIFEST.json`; the linux/amd64 archive
also carries the VM init. Keep the unpacked folder together until `install`
has finished.

`install` verifies the whole bundle against its manifest before writing
anything and refuses extra, missing or symlinked files. It also refuses to run
from a development build, inside a box or VM, or as a space operator. It then
copies the bundle into `~/.local/share/yoloctl/bundles/<version>-<os>-<arch>-<bundle>`,
an immutable directory that is never replaced or pruned, and atomically points
`~/.local/bin/yoloctl` (or `--bin-dir DIR`) at it. It never edits shell rc
files: when the bin directory is not on your PATH, it prints the one line
that adds it for your shell, for you to run.

`install --dry-run` verifies and prints what would happen without writing.
Without a terminal and without `--yes`, it only prints. An existing `yoloctl`
in the bin directory that is not one of ours is fingerprinted, never executed,
and replaced only after a confirmation at the terminal; without a terminal
it is refused (only `--dry-run` still prints).

## Verify a download (optional)

Every release also publishes `SHA256SUMS` and GitHub build attestations for
its archives. To check an archive before unpacking it, download the archive
and `SHA256SUMS` from the release page into one folder, then:

```sh
grep yoloctl-linux-amd64.tar.gz SHA256SUMS | sha256sum -c -   # macOS: shasum -a 256 -c -
gh attestation verify yoloctl-linux-amd64.tar.gz --repo leogr/yoloctl
tar xzf yoloctl-linux-amd64.tar.gz && ./yoloctl-linux-amd64/yoloctl install
```

## Upgrades and space updates

Run the same install line for a newer release. At a terminal, `install` asks
before replacing the current build; `--yes` accepts that managed upgrade
unattended and nothing else.

Spaces keep running their old controller until you update them. `install`
prints one command per space, normally `yoloctl space start NAME --update-controller`,
or `yoloctl space recreate NAME` for a Lima VM whose template is older than
this build, and asks separately before executing each one; `--yes` never
executes them. Updating a stopped space's controller boots its VM. A recreate
keeps the data disk, accounts and box declarations but loses running sessions
and VM-local workdirs. A Firecracker space whose VM image origin cannot be
proved still gets its controller-update command when the bundle differs,
but no VM image replacement is proposed automatically: that decision stays
manual (`space recreate NAME --update-vm-image`).

## Where builds keep their data

Development builds and installed releases share the default `yoloctl` home
beneath the XDG/OS config and cache bases; `yoloctl version --json` prints the
selected roots. An absolute, clean `YOLOCTL_HOME` selects that directory for
config and its `cache` child instead, for example to keep a development home
separate. Never move a live Lima home or update its controller automatically;
keep the old selected bundle until planned maintenance.

## macOS

Darwin binaries ship exactly as linked: the arm64 build carries the Go
linker's ad-hoc signature, which macOS CI verifies together with the bytes and
a native run. Browser quarantine, Developer ID signing and notarization are
outside this curl flow; Intel (amd64) execution remains a separate host/CI
acceptance gate.

## Making a release (maintainers)

`make dist VERSION=vX.Y.Z` builds the four archives and `SHA256SUMS`. Pushing
a `v*` tag runs the strict release workflow: it builds the same commit, runs
the host installer tests, verifies the artifacts, attests the archives and
checksums, and creates a draft release that a human publishes. It never
overwrites or retries an existing tag (rerunning fails), and an attestation
failure blocks the draft. A manual workflow run produces QA artifacts only.
Archive names carry no version (`yoloctl-<os>-<arch>.tar.gz`), so the
one-liners always fetch the latest release; the version is in the release
tag and in each bundle's `MANIFEST.json`. Before the first tag, configure a
`v*` tag ruleset and confirm that the repository plan is eligible for
attestations.
Publish releases as normal releases, not pre-releases, so that
`releases/latest` resolves to them.
