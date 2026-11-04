package openshell

import (
	"context"
	"debug/buildinfo"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	helperinputs "github.com/leogr/yoloctl"
)

// Exercise the real Go producer format, not just synthetic ELF/BuildInfo.
// These bounded compilation fixtures are inspected only: no helper is run,
// installed, qualified as a release or invoked with native/kernel authority.
func TestHelperProducerRealTrimpathBinaries(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	build := func(t *testing.T, target, cgo string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		name := filepath.Join(t.TempDir(), "producer-fixture")
		cmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-trimpath", "-ldflags=-s -w -buildid=", "-o", name, target)
		cmd.Dir = root
		for _, env := range os.Environ() {
			key, _, _ := strings.Cut(env, "=")
			switch key {
			case "GOOS", "GOARCH", "GOFLAGS", "GOWORK", "GOENV", "GOTOOLCHAIN", "CGO_ENABLED", "GOMAXPROCS":
				continue
			}
			cmd.Env = append(cmd.Env, env)
		}
		cmd.Env = append(cmd.Env, "GOOS=linux", "GOARCH="+runtime.GOARCH, "GOFLAGS=", "GOWORK=off", "GOENV=off", "GOTOOLCHAIN=local", "CGO_ENABLED="+cgo, "GOMAXPROCS=2")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("real producer fixture build: %v\n%s", err, out)
		}
		return name
	}
	name := build(t, "./cmd/yoloctl-openshell-helper", "0")
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	info, err := buildinfo.Read(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.GoVersion != runtime.Version() {
		t.Fatal("fixture compiler differs from test toolchain", info.GoVersion, runtime.Version())
	}
	for _, setting := range info.Settings {
		if setting.Key == "-ldflags" {
			t.Fatal("trimpath fixture unexpectedly exposes linker flags; requalify producer assumption")
		}
	}
	paths, err := helperinputs.ModulePaths()
	if err != nil {
		t.Fatal(err)
	}
	selected := HelperPins{GoVersion: runtime.Version()}
	for _, path := range paths {
		found := false
		for _, module := range info.Deps {
			if module.Path == path {
				selected.Inputs.Modules = append(selected.Inputs.Modules, helperinputs.Module{Path: module.Path, Version: module.Version, Sum: module.Sum})
				found = true
				break
			}
		}
		if !found {
			t.Fatal("real helper omitted qualified module", path)
		}
	}
	if err := CheckHelperProducer(file, selected, runtime.GOARCH); err != nil {
		t.Fatal("real prescribed trimpath helper rejected", err)
	}
	wrongArch := "arm64"
	if runtime.GOARCH == wrongArch {
		wrongArch = "amd64"
	}
	if err := CheckHelperProducer(file, selected, wrongArch); err == nil {
		t.Fatal("wrong real-binary architecture accepted")
	}
	t.Run("wrong-command", func(t *testing.T) {
		other := build(t, "./scripts/openshell-helper-inputs.go", "0")
		file, err := os.Open(other)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		if err := CheckHelperProducer(file, selected, runtime.GOARCH); err == nil {
			t.Fatal("real static maintenance CLI substituted for dedicated helper")
		}
	})
	t.Run("dynamic", func(t *testing.T) {
		if _, err := exec.LookPath("cc"); err != nil {
			t.Skip("C compiler unavailable for real dynamic producer negative")
		}
		other := build(t, "./cmd/yoloctl-openshell-helper", "1")
		file, err := os.Open(other)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = file.Close() }()
		contract, err := ELFContract(file, runtime.GOARCH)
		if err != nil {
			t.Fatal(err)
		}
		if contract == "static" {
			t.Fatal("dynamic fixture did not acquire its expected system loader")
		}
		if err := CheckHelperProducer(file, selected, runtime.GOARCH); err == nil {
			t.Fatal("real dynamic helper accepted")
		}
	})
}
