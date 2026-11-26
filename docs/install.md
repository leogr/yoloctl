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
`~/.local/bin/yoloctl` (or `--bin-dir DIR`) at it.

For bash, zsh or fish (from `$SHELL`), `install` can add one marked block to
your shell's startup file: the bin directory on PATH when it is missing, and
yoloctl shell completion when it is not set up yet. At a terminal it asks
(default No); `--add-to-path` and `--completion` answer yes, also for scripts
(with `--yes` when there is no terminal). The files are `~/.zshrc` (or
`$ZDOTDIR/.zshrc`), `~/.bashrc`, and fish's `config.fish` and
`completions/yoloctl.fish` under `$XDG_CONFIG_HOME` or `~/.config`. zsh
completion loads only where `compinit` has already run. A symlinked,
hard-linked, foreign-owned or oversized file is never edited: the block is
printed for you to add. An existing block for another install is left alone.
The file is replaced atomically with its mode kept; ACLs and extended
attributes are not copied. To undo, delete the lines from `# >>> yoloctl` to
`# <<< yoloctl`. Without a supported shell, install prints the PATH line to run.

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

The installer leaves spaces untouched and prints follow-up commands for spaces
that need them. If a space uses another controller build, run
`yoloctl space upgrade NAME` on the host. It checks the VM and offers to
recreate it if the Firecracker image or the Lima container stack differs from
this build. It can start a stopped VM and restart eligible stale sessions. It
asks separately for each container replacement or policy-only restart of a
running box. On a VM you keep, stopped boxes stay stopped and get follow-up
commands for known changes.

Back up files stored only inside containers before accepting replacements.
A VM recreate keeps the data disk, accounts, box settings and host workdirs.
It discards containers, sessions, per-box caches and VM-local workdirs. Start
the boxes you need afterwards. See the Upgrade notes section of the
[v0.4.0 release](https://github.com/leogr/yoloctl/releases/tag/v0.4.0) for what
to expect when coming from v0.3.0.

For a controller-only update with a separate session restart decision, use
`yoloctl space start NAME --update-controller`. The installer advises
`yoloctl space recreate NAME` directly if a Lima VM lacks the required
OpenShell provisioning. It can also print optional VM recreation commands,
including `yoloctl space recreate NAME --update-vm-image` when the selected
Firecracker image differs from this build's default.

The printed commands use the installed binary and your explicit
`YOLOCTL_HOME`, if any. Set `NO_COLOR` to turn off terminal log colors.

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

### Fix slow sudo in an existing Lima VM

If a Lima command waits while `sudo` resolves the guest's own hostname,
check whether that name is missing from `/etc/hosts`. Older yoloctl builds
could report this as a `check Lima OpenShell nft support` timeout. VMs
created with the updated template add the entry during boot.

Repair the entry in the running guest for either an initialized space or an
unfinished `space create`. The command adds the hostname only if it is
missing. Replace `NAME` with your space name in this host command.

```sh
yoloctl space ssh NAME --root -- sh -s <<'SH'
set -eu
name=$(hostname)
mapped=$(LC_ALL=C awk -v name="$name" '
  BEGIN { name = tolower(name) }
  {
    sub(/#.*/, "")
    for (i = 2; i <= NF; i++) {
      if (tolower($i) == name) { found = 1; exit }
    }
  }
  END { print found ? "yes" : "no" }
' /etc/hosts)
if [ "$mapped" = no ]; then
  printf '\n127.0.1.1 %s\n' "$name" >> /etc/hosts
fi
SH
```

For an initialized space, retry the affected command. If `space create`
stopped before initializing the account disk, run `yoloctl space start NAME`
with the build that created the space, or
`yoloctl space start NAME --update-controller` with the newer build.

To adopt the updated boot provisioning on an initialized space, you can
instead run `yoloctl space recreate NAME` with the new build. Back up files
stored only inside containers first. Recreation keeps the native account
disk, accounts, box declarations and host workdirs. It discards containers,
sessions and VM-local workdirs.

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
