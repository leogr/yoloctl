package openshell

import (
	"runtime/debug"
	"testing"
)

func staticHelperBuildFixture() *debug.BuildInfo {
	info := &debug.BuildInfo{Path: "github.com/leogr/yoloctl/cmd/yoloctl-openshell-helper", GoVersion: "go1.27.0"}
	for k, v := range map[string]string{"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64", "-trimpath": "true"} {
		info.Settings = append(info.Settings, debug.BuildSetting{Key: k, Value: v})
	}
	return info
}

func TestDedicatedProducerContractRejectsCLIAndDynamicOrModifiedBuild(t *testing.T) {
	if err := validateStaticHelperBuild(staticHelperBuildFixture(), "go1.27.0", "amd64"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*debug.BuildInfo){
		func(i *debug.BuildInfo) { i.Path = "github.com/leogr/yoloctl/cmd/yoloctl" },
		func(i *debug.BuildInfo) { i.GoVersion = "go1.27.1" },
		func(i *debug.BuildInfo) {
			i.Settings = append(i.Settings, debug.BuildSetting{Key: "CGO_ENABLED", Value: "1"})
		},
		func(i *debug.BuildInfo) {
			i.Settings = append(i.Settings, debug.BuildSetting{Key: "-tags", Value: "unqualified"})
		},
		func(i *debug.BuildInfo) {
			i.Settings = append(i.Settings, debug.BuildSetting{Key: "vcs.revision", Value: "unknown"})
		},
	} {
		i := staticHelperBuildFixture()
		mutate(i)
		if err := validateStaticHelperBuild(i, "go1.27.0", "amd64"); err == nil {
			t.Fatal("unqualified helper producer accepted")
		}
	}
}

// Distribution Go toolchains can bake in a debug-information-only experiment;
// the stripped helper accepts it, while any other experiment stays refused.
func TestDedicatedProducerContractAcceptsOnlyDebugInfoExperiments(t *testing.T) {
	for value, accepted := range map[string]bool{
		"nodwarf5": true, "dwarf5": true, "dwarf5,nodwarf5": true,
		"greenteagc": false, "nodwarf5,greenteagc": false, "nodwarf5,": false,
	} {
		i := staticHelperBuildFixture()
		i.Settings = append(i.Settings, debug.BuildSetting{Key: "GOEXPERIMENT", Value: value})
		if err := validateStaticHelperBuild(i, "go1.27.0", "amd64"); (err == nil) != accepted {
			t.Errorf("GOEXPERIMENT=%s: accepted=%v, error %v", value, accepted, err)
		}
	}
}

// A toolchain reporting "goX.Y.Z X:nodwarf5" records and matches the plain pin;
// any other experiment suffix keeps the version unequal and is refused.
func TestHelperGoVersionDropsOnlyDebugInfoExperiments(t *testing.T) {
	for version, want := range map[string]string{
		"go1.27.3": "go1.27.3", "go1.27.3 X:nodwarf5": "go1.27.3", "go1.27.3 X:dwarf5,nodwarf5": "go1.27.3",
		"go1.27.3-X:nodwarf5": "go1.27.3", "go1.27.3-X:greenteagc": "go1.27.3-X:greenteagc",
		"go1.27.3 X:greenteagc": "go1.27.3 X:greenteagc", "go1.27.3 X:nodwarf5,greenteagc": "go1.27.3 X:nodwarf5,greenteagc",
	} {
		if got := HelperGoVersion(version); got != want {
			t.Errorf("HelperGoVersion(%q) = %q, want %q", version, got, want)
		}
	}
	for version, accepted := range map[string]bool{"go1.27.0 X:nodwarf5": true, "go1.27.0-X:nodwarf5": true, "go1.27.0 X:greenteagc": false} {
		i := staticHelperBuildFixture()
		i.GoVersion = version
		if err := validateStaticHelperBuild(i, "go1.27.0", "amd64"); (err == nil) != accepted {
			t.Errorf("GoVersion %q: accepted=%v, error %v", version, accepted, err)
		}
	}
}
