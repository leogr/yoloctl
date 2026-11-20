#!/bin/sh
# GitHub CLI from GitHub's official apt repository. Apt trusts only the reviewed
# keys, so it verifies the repository metadata and package before installing.
set -eu

gh_keyfile=$1
gh_keyring=${2:-/etc/apt/keyrings/githubcli-archive-keyring.gpg}
gh_sources=${3:-/etc/apt/sources.list.d/github-cli.sources}
gh_preferences=${4:-/etc/apt/preferences.d/github-cli}
gh_bin=${5:-/usr/bin/gh}
gh_keys='2C6106201985B60E6C7AC87323F3D4EA75716059 7F38BBB59D064DBCB3D84D725612B36462313325'
gh_work=$(mktemp -d)
trap 'rm -rf -- "$gh_work"' EXIT

# Isolate GnuPG from personal settings/keyrings; the file must hold exactly the
# primary keys GitHub publishes. Neither step imports, so no agent starts.
gh_found=$(gpg --no-options --batch --homedir "$gh_work" --with-colons \
    --import-options show-only --import "$gh_keyfile" \
    | awk -F: '$1 == "pub" { primary = 1; next } $1 == "fpr" && primary { print $10 } { primary = 0 }' \
    | sort | tr '\n' ' ')
if [ "$gh_found" != "$gh_keys " ]; then
    echo 'yoloctl github feature: unexpected GitHub CLI signing keys' >&2
    exit 1
fi
gpg --no-options --batch --homedir "$gh_work" --dearmor < "$gh_keyfile" > "$gh_work/keyring.gpg"
install -d -m 0755 "${gh_keyring%/*}"
install -m 0644 "$gh_work/keyring.gpg" "$gh_keyring"
printf 'Types: deb\nURIs: https://cli.github.com/packages\nSuites: stable\nComponents: main\nArchitectures: %s\nSigned-By: %s\n' \
    "$(dpkg --print-architecture)" "$gh_keyring" > "$gh_sources"
# Take gh only from GitHub, even when the base prefers another release (such
# as APT::Default-Release) or carries its own older gh; no other source is used.
printf 'Package: gh\nPin: origin cli.github.com\nPin-Priority: 1001\n\nPackage: gh\nPin: version *\nPin-Priority: -1\n' > "$gh_preferences"

apt-get -o Acquire::Retries=3 update --error-on=any
apt-get -o Acquire::Retries=3 -o Dir::Cache::Archives=/var/cache/yoloctl/apt \
    -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends gh
"$gh_bin" --version
