#!/bin/sh
# AWS CLI v2 official ZIP installer. Verify before extracting or executing.
set -eu

case "$(dpkg --print-architecture)" in
    amd64) aws_arch=x86_64 ;;
    arm64) aws_arch=aarch64 ;;
    *) echo 'yoloctl aws feature: supported architectures are amd64 and arm64' >&2; exit 1 ;;
esac

aws_keyfile=$1
aws_installdir=${2:-/usr/local/aws-cli}
aws_bindir=${3:-/usr/local/bin}
aws_work=$(mktemp -d)
trap 'rm -rf -- "$aws_work"' EXIT
aws_keyring="$aws_work/keyring"
mkdir -m 0700 "$aws_keyring"

# Isolate GnuPG from personal settings/keyrings and export only the reviewed key.
aws_fingerprint=$(gpg --no-options --batch --homedir "$aws_keyring" --with-colons \
    --import-options show-only --import "$aws_keyfile" | awk -F: '$1 == "fpr" { print $10; exit }')
if [ "$aws_fingerprint" != FB5DB77FD5C118B80511ADA8A6310ACC4672475C ]; then
    echo 'yoloctl aws feature: unexpected AWS CLI signing key' >&2
    exit 1
fi
gpg --no-options --batch --homedir "$aws_keyring" --import "$aws_keyfile"
gpg --no-options --batch --homedir "$aws_keyring" --export "$aws_fingerprint" > "$aws_work/verified-key.gpg"

aws_url="https://awscli.amazonaws.com/awscli-exe-linux-$aws_arch.zip"
curl -fsSL --proto '=https' --tlsv1.2 --retry 3 "$aws_url" -o "$aws_work/aws.zip"
curl -fsSL --proto '=https' --tlsv1.2 --retry 3 "$aws_url.sig" -o "$aws_work/aws.zip.sig"
gpgv --homedir "$aws_keyring" --keyring "$aws_work/verified-key.gpg" "$aws_work/aws.zip.sig" "$aws_work/aws.zip"

unzip -q "$aws_work/aws.zip" -d "$aws_work"
"$aws_work/aws/install" --install-dir "$aws_installdir" --bin-dir "$aws_bindir" --update
"$aws_bindir/aws" --version
