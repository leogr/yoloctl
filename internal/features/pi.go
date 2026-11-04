package features

// Pi's offline startup uses image-provided search tools instead of downloading
// them into the account. Upstream accepts Ubuntu's fdfind executable directly.
// Use the same npm install path as runtime maintenance; no native installer or
// launcher is necessary. Verified against Pi v1.0.3.
var piInstallDockerfile = `RUN node -e 'const [major, minor] = process.versions.node.split(".").map(Number); if (major < 22 || (major === 22 && minor < 19)) { console.error("Pi requires Node >=22.19; rebuild the base image"); process.exit(1); }'
` + aptInstallDockerfile("ripgrep fd-find") + npmPackageInstallDockerfile("@earendil-works/pi-coding-agent", "latest")
