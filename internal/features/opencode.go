package features

// Install ripgrep ahead of time so OpenCode never needs to download it through
// the workload proxy. npm's postinstall installs the native executable at
// opencode-ai/bin/opencode.exe on both Linux architectures. Keep this path in
// sync with network.opencode; do not grant Node the native agent's egress.
var opencodeInstallDockerfile = aptInstallDockerfile("ripgrep") + npmPackageInstallDockerfile("opencode-ai", "latest") + `
RUN test -x ` + NPMGlobalPrefix + `/lib/node_modules/opencode-ai/bin/opencode.exe`
