package openshell

import (
	"debug/buildinfo"
	"errors"
	"io"
	"runtime/debug"
	"strings"

	helperinputs "github.com/leogr/yoloctl"
)

// CheckHelperProducer inspects bytes without executing them. It is required
// before replacement; metadata never substitutes for the artifact/source proof
// or native execution authority.
func CheckHelperProducer(file io.ReaderAt, selected HelperPins, arch string) error {
	contract, err := ELFContract(file, arch)
	if err != nil {
		return err
	}
	if contract != "static" {
		return errors.New("separate OpenShell helper must be a static Linux executable")
	}
	info, err := buildinfo.Read(file)
	if err != nil {
		return errors.New("static helper has no valid compiled Go producer information")
	}
	if err := validateStaticHelperBuild(info, selected.GoVersion, arch); err != nil {
		return err
	}
	if len(info.Deps) != len(selected.Inputs.Modules) {
		return errors.New("static helper dependency closure differs from qualification")
	}
	return helperinputs.CheckModules(info, selected.Inputs.Modules)
}

func validateStaticHelperBuild(info *debug.BuildInfo, goVersion, arch string) error {
	if info == nil || info.Path != "github.com/leogr/yoloctl/cmd/yoloctl-openshell-helper" || HelperGoVersion(info.GoVersion) != goVersion {
		return errors.New("not the qualified dedicated helper command/toolchain")
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		if _, exists := settings[setting.Key]; exists {
			return errors.New("duplicate helper producer setting")
		}
		settings[setting.Key] = setting.Value
		if strings.HasPrefix(setting.Key, "vcs") {
			return errors.New("helper build contains unqualified VCS stamping")
		}
	}
	// Go deliberately omits -ldflags from BuildInfo with -trimpath (issue
	// 52372). Linker flags are bound by the frozen recipe/build proof and exact
	// binary hash; requiring an unavailable metadata field rejects real builds.
	for key, expected := range map[string]string{"CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": arch, "-trimpath": "true"} {
		if settings[key] != expected {
			return errors.New("helper build differs from the static production recipe")
		}
	}
	for _, key := range []string{"-race", "-asan", "-msan", "-tags", "GOEXPERIMENT"} {
		if key == "GOEXPERIMENT" && debugInfoOnlyExperiment(settings[key]) {
			continue
		}
		if settings[key] != "" && settings[key] != "false" {
			return errors.New("helper build has an unqualified compiler option")
		}
	}
	return nil
}

// HelperGoVersion is the helper toolchain version as recorded and compared. Go
// reports a baked-in GOEXPERIMENT as "goX.Y.Z X:list" or "goX.Y.Z-X:list"; a
// list made only of debug-information experiments (debugInfoOnlyExperiment) is
// dropped. Any other suffix is kept, so the goX.Y.Z pin check still refuses it.
func HelperGoVersion(version string) string {
	for _, separator := range []string{" X:", "-X:"} {
		if base, experiments, found := strings.Cut(version, separator); found && debugInfoOnlyExperiment(experiments) {
			return base
		}
	}
	return version
}

// debugInfoOnlyExperiment accepts the GOEXPERIMENT values some distribution Go
// toolchains bake in that change only DWARF debug information (dwarf5,
// nodwarf5), which the helper recipe strips (-ldflags=-s -w). Every other
// experiment stays an unqualified compiler option.
func debugInfoOnlyExperiment(value string) bool {
	if value == "" {
		return false
	}
	for _, experiment := range strings.Split(value, ",") {
		if experiment != "dwarf5" && experiment != "nodwarf5" {
			return false
		}
	}
	return true
}
