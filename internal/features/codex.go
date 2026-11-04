package features

// Codex checks for bwrap on PATH at startup, even when the managed profile
// delegates confinement to OpenShell. Install the distro package in the image
// to make the helper available without enabling a nested sandbox.
var codexInstallDockerfile = aptInstallDockerfile("bubblewrap") + npmPackageInstallDockerfile("@openai/codex", "latest")
