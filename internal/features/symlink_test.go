package features

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/leogr/yoloctl/internal/capabilities"
)

// lnSymlink matches `ln -s TARGET LINK` with any combined short flags.
var lnSymlink = regexp.MustCompile(`\bln\s+-[a-zA-Z]*s[a-zA-Z]*\s+(\S+)\s+(\S+)`)

// OpenShell attributes egress to the resolved executable, so a granted
// binary that a feature installs as a symlink can never match: the go
// feature's /usr/local/bin/go once silently denied every module download.
func TestNoCapabilityGrantsAFeatureSymlink(t *testing.T) {
	links := map[string]string{} // link path -> feature
	for _, name := range Names() {
		dockerfile, err := GenerateDockerfile("base", []string{name})
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		for _, m := range lnSymlink.FindAllStringSubmatch(dockerfile, -1) {
			links[strings.TrimSuffix(m[2], ";")] = name
		}
	}
	if links["/usr/local/bin/go"] != "go" {
		t.Fatalf("the cross-check no longer sees the go feature's PATH symlink: %v", links)
	}
	for _, name := range capabilities.Names() {
		c, err := capabilities.Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, grant := range c.Network {
			for _, binary := range grant.Binaries {
				if feature, ok := links[binary]; ok {
					t.Errorf("%s grants %s, a symlink the %s feature creates; grant its resolved target", name, binary, feature)
				}
			}
		}
	}
}

func TestGoFeatureKeepsItsToolchainAndGrantsTheRealBinary(t *testing.T) {
	f, err := Lookup("go")
	if err != nil {
		t.Fatal(err)
	}
	// Go's installation default, read by every go process: an image ENV may
	// not reach a confined exec.
	for _, want := range []string{
		"sed -i 's/^GOTOOLCHAIN=.*/GOTOOLCHAIN=local/' /usr/local/go/go.env",
		"grep -qx 'GOTOOLCHAIN=local' /usr/local/go/go.env",
		"ln -sf /usr/local/go/bin/go /usr/local/bin/go",
	} {
		if !strings.Contains(f.Install.Dockerfile, want) {
			t.Errorf("go Dockerfile lacks %q:\n%s", want, f.Install.Dockerfile)
		}
	}
	if strings.Contains(f.Install.Dockerfile, "ENV GOTOOLCHAIN") {
		t.Error("GOTOOLCHAIN must live in go.env, not an image ENV")
	}
	c, err := capabilities.Lookup("network.go-modules")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Network) != 1 || !slices.Equal(c.Network[0].Binaries, []string{"/usr/local/go/bin/go"}) {
		t.Errorf("network.go-modules grants %+v, want the resolved /usr/local/go/bin/go", c.Network)
	}
}
