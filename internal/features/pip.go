package features

// Reuse the base's Python instead of installing Ubuntu's different default
// interpreter. Both aliases must resolve to the executable network.pypi grants.
// A real temporary venv verifies ensurepip as well as importing venv: distro
// packages can provide the module without the wheels needed to seed pip.
//
// Pip's normal per-user cache and --user install scheme live under sandbox's
// home. Do not set user=true or an install prefix: those override project venvs.
// The launchers fill ensurepip's missing unversioned pip command and keep pip3
// aligned with python3 even if another feature installs the distro's pip.
const pipInstallDockerfile = `RUN if [ "$(readlink -f /usr/local/bin/python)" != /usr/bin/python3.13 ] \
        || [ "$(readlink -f /usr/local/bin/python3)" != /usr/bin/python3.13 ]; then \
        echo 'pip requires base Python aliases at /usr/local/bin/python and python3 resolving to /usr/bin/python3.13 (the PyPI policy executable)' >&2; exit 1; \
    fi \
    && /usr/local/bin/python3 -m pip --version \
    && pip_check_dir=$(mktemp -d /tmp/yoloctl-pip-check.XXXXXX) \
    && /usr/local/bin/python3 -m venv "$pip_check_dir" \
    && "$pip_check_dir/bin/python" -m pip --version \
    && rm -rf "$pip_check_dir" \
    && rm -f /usr/local/bin/pip /usr/local/bin/pip3 \
    && printf '%s\n' '#!/bin/sh' 'exec /usr/local/bin/python3 -m pip "$@"' > /usr/local/bin/pip \
    && chmod 0755 /usr/local/bin/pip \
    && ln -sf pip /usr/local/bin/pip3 \
    && install -d -o sandbox -g sandbox \
        /home/sandbox/.local /home/sandbox/.local/bin \
        /home/sandbox/.cache /home/sandbox/.cache/pip
ENV PATH=/home/sandbox/.local/bin:$PATH`
