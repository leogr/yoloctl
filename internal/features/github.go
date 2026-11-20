package features

import (
	_ "embed"
	"encoding/base64"
	"fmt"
)

// GitHub CLI's apt keyring, checked against the fingerprints GitHub publishes.
// Key rotation is an explicit source update; never discover a trust key at
// build. gh itself follows the repository's latest release.
// https://github.com/cli/cli/blob/trunk/docs/install_linux.md
//
//go:embed github-cli-key.asc
var githubCLIKey string

//go:embed github-install.sh
var githubInstallScript string

func githubInstallDockerfile() string {
	return aptInstallDockerfile("ca-certificates gnupg") + fmt.Sprintf(`RUN %s \
    gh_tmp="$(mktemp -d)" \
    && trap 'rm -rf -- "$gh_tmp"' EXIT \
    && printf '%%s' '%s' | base64 -d > "$gh_tmp/install.sh" \
    && printf '%%s' '%s' | base64 -d > "$gh_tmp/key.asc" \
    && sh "$gh_tmp/install.sh" "$gh_tmp/key.asc"`, aptCacheMounts, base64.StdEncoding.EncodeToString([]byte(githubInstallScript)), base64.StdEncoding.EncodeToString([]byte(githubCLIKey)))
}
