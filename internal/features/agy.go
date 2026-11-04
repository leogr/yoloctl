package features

import (
	_ "embed"
	"encoding/base64"
	"fmt"

	"github.com/leogr/yoloctl/internal/nativerelease"
)

//go:embed agy-launch.py
var agyLaunch string

func agyInstallDockerfile() string {
	release, err := nativerelease.Lookup("antigravity")
	if err != nil {
		panic(err)
	}
	return release.Dockerfile() + fmt.Sprintf(`RUN mkdir -p /usr/local/lib/yoloctl \
    && echo '%s' | base64 -d > /usr/local/lib/yoloctl/agy-launch.py \
    && printf '%%s\n' '#!/bin/sh' 'exec /usr/bin/python3 -I /usr/local/lib/yoloctl/agy-launch.py "$@"' > /usr/local/bin/agy \
    && chmod 0755 /usr/local/bin/agy`, base64.StdEncoding.EncodeToString([]byte(agyLaunch)))
}
