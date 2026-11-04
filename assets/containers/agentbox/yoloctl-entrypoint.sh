#!/bin/sh
# yoloctl box entrypoint shim.
#
# Runs as root before openshell-sandbox. Remaps the in-container `sandbox`
# user's UID/GID to match the host developer (sourced from
# YOLOCTL_HOST_UID/YOLOCTL_HOST_GID env vars). openshell-sandbox resolves the
# user by name, so its numeric UID/GID are free to vary.
#
# When YOLOCTL_HOST_UID is unset, this is a no-op exec and the image's
# sandbox:2000 ownership remains unchanged.
#
# The shim never re-chowns /workdir — its ownership is decided by the
# mount source:
#   - Ephemeral VM-local workdir: chown'd at create time before the
#     container starts; sandbox UID matches whatever was set then.
#   - Host-mode workdir bind: files keep the host owner;
#     the remap aligns sandbox to that UID so writes round-trip cleanly.

set -eu

# Refuse the root group before changing either identity or home ownership.
# Other collisions, including macOS GID 20, retain their existing semantics.
case "${YOLOCTL_HOST_GID:-}" in
    ''|*[!0]*) ;;
    *) echo "yoloctl: refusing to map sandbox to host GID 0 (root group)" >&2; exit 1 ;;
esac

if [ -n "${YOLOCTL_HOST_UID:-}" ]; then
    current_uid=$(id -u sandbox)
    if [ "$current_uid" != "$YOLOCTL_HOST_UID" ]; then
        usermod -u "$YOLOCTL_HOST_UID" sandbox
        # /home/sandbox was created with the old UID at image build time;
        # re-own so the sandbox user can read its own home after the remap.
        [ -d /home/sandbox ] && chown -R sandbox /home/sandbox 2>/dev/null || true
    fi
fi

if [ -n "${YOLOCTL_HOST_GID:-}" ]; then
    current_gid=$(getent group sandbox | cut -d: -f3)
    if [ "$current_gid" != "$YOLOCTL_HOST_GID" ]; then
        # macOS commonly uses GID 20, already assigned to dialout in Ubuntu.
        # Keep the sandbox name required by policy and leave the existing
        # group intact; both names may resolve to the host's numeric GID.
        groupmod -o -g "$YOLOCTL_HOST_GID" sandbox
        [ -d /home/sandbox ] && chgrp -R sandbox /home/sandbox 2>/dev/null || true
    fi
fi

exec "$@"
