package features

import (
	_ "embed"
	"encoding/base64"
	"fmt"
)

// AWS's public signing key, checked against its published fingerprint. Key
// rotation is an explicit source update; never discover a trust key at build.
// https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html
//
//go:embed aws-cli-key.asc
var awsCLIKey string

//go:embed aws-install.sh
var awsInstallScript string

func awsInstallDockerfile() string {
	return `RUN case "$(dpkg --print-architecture)" in amd64|arm64) ;; *) echo 'yoloctl aws feature: supported architectures are amd64 and arm64' >&2; exit 1 ;; esac
` + aptInstallDockerfile("ca-certificates curl gnupg gpgv unzip groff less") + fmt.Sprintf(`RUN aws_tmp="$(mktemp -d)" \
    && trap 'rm -rf -- "$aws_tmp"' EXIT \
    && printf '%%s' '%s' | base64 -d > "$aws_tmp/install.sh" \
    && printf '%%s' '%s' | base64 -d > "$aws_tmp/key.asc" \
    && sh "$aws_tmp/install.sh" "$aws_tmp/key.asc"`, base64.StdEncoding.EncodeToString([]byte(awsInstallScript)), base64.StdEncoding.EncodeToString([]byte(awsCLIKey)))
}
