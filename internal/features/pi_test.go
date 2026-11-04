package features

import (
	"strings"
	"testing"
)

func TestPiInstallProvidesOfflineSearchTools(t *testing.T) {
	dockerfile, err := GenerateDockerfile("base", []string{"pi"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Node >=22.19", "ripgrep fd-find", "@earendil-works/pi-coding-agent@latest --prefix=" + NPMGlobalPrefix} {
		if !strings.Contains(dockerfile, want) {
			t.Errorf("Pi install missing %q", want)
		}
	}
	if strings.Contains(dockerfile, "/home/sandbox/.pi") {
		t.Fatal("image installation must not seed root-owned native account state")
	}
}
