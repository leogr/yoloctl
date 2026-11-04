// Package nativerelease describes managed native CLI distributions. The catalog
// contains data, not agent launch commands: maintenance never executes an agent.
package nativerelease

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

const AgyBinary = "/home/sandbox/.local/share/yoloctl/agy/agy"

// Release identifies a reviewed distribution with an architecture-specific
// JSON manifest containing version, url and sha512. Custom profiles may select
// a distribution, but cannot supply an arbitrary maintenance command or URL.
type Release struct {
	ManifestBase string `json:"manifest_base"`
	ArtifactBase string `json:"artifact_base"`
	Member       string `json:"member"`
	Binary       string `json:"binary"`
}

func Lookup(name string) (Release, error) {
	switch name {
	case "antigravity":
		return Release{
			ManifestBase: "https://antigravity-cli-auto-updater-974169037036.us-central1.run.app/manifests/linux_",
			ArtifactBase: "https://storage.googleapis.com/antigravity-public/antigravity-cli/",
			Member:       "antigravity", Binary: AgyBinary,
		}, nil
	default:
		return Release{}, fmt.Errorf("unknown native_release %q", name)
	}
}

func (r Release) ManifestURL(arch string) (string, error) {
	if arch != "amd64" && arch != "arm64" {
		return "", fmt.Errorf("native release does not support Linux architecture %q", arch)
	}
	return r.ManifestBase + arch + ".json", nil
}

func (r Release) MetadataPath() string { return r.Binary + ".json" }

//go:embed install.py
var installer string

// InstallArgs returns a bounded, controller-owned maintenance program. Python
// isolated mode ignores user imports and PYTHON* environment variables. The
// downloaded binary is verified and installed, never executed here.
func (r Release) InstallArgs() []string {
	b, _ := json.Marshal(r)
	return []string{"/usr/bin/python3", "-I", "-c", installer, string(b)}
}

// Dockerfile installs through the same program used for runtime maintenance.
// Base64 keeps source newlines out of Dockerfile's instruction parser.
func (r Release) Dockerfile() string {
	b, _ := json.Marshal(r)
	return fmt.Sprintf("RUN /usr/bin/python3 -I -c 'import base64; exec(base64.b64decode(\"%s\"))' '%s'\nRUN chown -R sandbox:sandbox /home/sandbox/.local\n",
		base64.StdEncoding.EncodeToString([]byte(installer)), string(b))
}
