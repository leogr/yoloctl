package features

// Install ripgrep ahead of time so OpenCode never needs to download it through
// the workload proxy. npm's postinstall installs the native executable at
// opencode-ai/bin/opencode.exe on both Linux architectures, under whichever
// prefix this image uses for the package. Keep both paths in sync with
// network.opencode; do not grant Node the native agent's egress.
func opencodeInstall(npm *npmInstaller) string {
	return aptInstallDockerfile("ripgrep") + npm.install("opencode-ai", "latest") + `
RUN test -x ` + npm.prefix("opencode-ai") + `/lib/node_modules/opencode-ai/bin/opencode.exe`
}
