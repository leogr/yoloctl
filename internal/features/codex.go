package features

// Codex checks for bwrap on PATH at startup, even when the managed profile
// delegates confinement to OpenShell. Install the distro package in the image
// to make the helper available without enabling a nested sandbox.
var codexInstallDockerfile = `RUN --mount=type=cache,id=yoloctl-apt-archives-v1,target=/var/cache/yoloctl/apt,sharing=locked \
    apt-get -o Acquire::Retries=3 update --error-on=any \
    && apt-get -o Acquire::Retries=3 -o Dir::Cache::Archives=/var/cache/yoloctl/apt \
        -o APT::Keep-Downloaded-Packages=true install -y --no-install-recommends bubblewrap \
    && rm -rf /var/lib/apt/lists/*
` + npmPackageInstallDockerfile("@openai/codex", "latest")
