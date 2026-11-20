#!/bin/sh
# GitHub CLI from GitHub's official apt repository. Apt trusts only the reviewed
# keys, so it verifies the repository metadata and package before installing.
set -eu

gh_keyfile=$1
gh_keyring=${2:-/etc/apt/keyrings/githubcli-archive-keyring.gpg}
gh_sources=${3:-/etc/apt/sources.list.d/github-cli.sources}
gh_bin=${4:-/usr/bin/gh}
gh_keys='2C6106201985B60E6C7AC87323F3D4EA75716059 7F38BBB59D064DBCB3D84D725612B36462313325'
gh_work=$(mktemp -d)
trap 'rm -rf -- "$gh_work"' EXIT

# Isolate GnuPG from personal settings/keyrings; the file must hold exactly the
# primary keys GitHub publishes, and only those are exported for apt.
gh_found=$(gpg --no-options --batch --homedir "$gh_work" --with-colons \
    --import-options show-only --import "$gh_keyfile" \
    | awk -F: '$1 == "pub" { primary = 1; next } $1 == "fpr" && primary { print $10 } { primary = 0 }' \
    | sort | tr '\n' ' ')
if [ "$gh_found" != "$gh_keys " ]; then
    echo 'yoloctl github feature: unexpected GitHub CLI signing keys' >&2
    exit 1
fi
gpg --no-options --batch --homedir "$gh_work" --import "$gh_keyfile"
# shellcheck disable=SC2086 # the fixed fingerprint list splits into arguments
gpg --no-options --batch --homedir "$gh_work" --export $gh_keys > "$gh_work/keyring.gpg"
install -d -m 0755 "${gh_keyring%/*}"
install -m 0644 "$gh_work/keyring.gpg" "$gh_keyring"
printf 'Types: deb\nURIs: https://cli.github.com/packages\nSuites: stable\nComponents: main\nArchitectures: %s\nSigned-By: %s\n' \
    "$(dpkg --print-architecture)" "$gh_keyring" > "$gh_sources"

apt-get -o Acquire::Retries=3 update --error-on=any
apt-get -o Acquire::Retries=3 -o Dir::Cache::Archives=/var/cache/yoloctl/apt \
    -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends gh
"$gh_bin" --version
