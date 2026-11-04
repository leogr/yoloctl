package features

import "fmt"

// PrivilegedRuntime names the installer that the composer appends after the
// declared and profile features when a box is privileged. The ':' makes the
// name undeclarable (it fails layout.ValidateName and is not an OCI
// reference), so no manifest, custom catalog item or completion can select
// it, and it is absent from Names and BuiltinNames. It requires no
// capabilities and therefore never feeds back into capability resolution.
const PrivilegedRuntime = "yoloctl:privileged-sudo"

// PrivilegedSudoers is the root-owned drop-in granting the mapped sandbox user
// noninteractive root inside the privileged container.
const PrivilegedSudoers = "/etc/sudoers.d/90-yoloctl-privileged"

var privilegedRuntime = Feature{
	Name:        PrivilegedRuntime,
	Description: "sudo for the sandbox user in a privileged box (installed with apt when the base lacks it)",
	Install:     &Install{Kind: InstallDockerfile, Dockerfile: privilegedRuntimeDockerfile()},
}

// The installer never fetches kernel packages or modules. A custom base must
// already provide sudo with an /etc/sudoers.d include, or apt-get to install
// it; otherwise the build fails instead of starting without promised root.
// Its text and the feature's Description (rendered as the Dockerfile's
// "# Feature:" comment) are part of every privileged box's composed image
// identity: changing either is image drift that recreates every privileged box.
func privilegedRuntimeDockerfile() string {
	return fmt.Sprintf(`RUN <<'YOLOCTL_PRIVILEGED_SUDO'
#!/bin/sh
set -eu
if ! command -v sudo >/dev/null 2>&1 || ! command -v visudo >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update
        apt-get install -y --no-install-recommends sudo
        rm -rf /var/lib/apt/lists/*
    fi
fi
if ! command -v sudo >/dev/null 2>&1 || ! command -v visudo >/dev/null 2>&1; then
    echo 'a privileged box needs sudo and visudo in the image; use an apt base or provide them' >&2
    exit 1
fi
if ! id -u sandbox >/dev/null 2>&1; then
    echo 'a privileged box needs the sandbox user in the image' >&2
    exit 1
fi
if ! grep -Eq '^[@#]includedir[[:space:]]+/etc/sudoers\.d[[:space:]]*$' /etc/sudoers; then
    echo 'a privileged box needs /etc/sudoers to include /etc/sudoers.d' >&2
    exit 1
fi
install -d -o root -g root -m 0750 /etc/sudoers.d
tmp=$(mktemp /etc/sudoers.d/.yoloctl-privileged.XXXXXX)
printf '%%s\n' 'sandbox ALL=(ALL:ALL) NOPASSWD: ALL' > "$tmp"
chown root:root "$tmp"
chmod 0440 "$tmp"
visudo -cf "$tmp" >/dev/null
mv -f "$tmp" %s
visudo -c >/dev/null
YOLOCTL_PRIVILEGED_SUDO`, PrivilegedSudoers)
}
