package features

// Codex checks for bwrap on PATH at startup, even when the managed profile
// delegates confinement to OpenShell. Install the distro package in the image
// to make the helper available without enabling a nested sandbox.
func codexInstall(npm *npmInstaller) string {
	return aptInstallDockerfile("bubblewrap") + npm.install("@openai/codex", "latest")
}
