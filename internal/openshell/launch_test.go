package openshell

import (
	"encoding/json"
	"github.com/leogr/yoloctl/internal/mainlaunch"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestReadOnlyLaunchIntentUsesRealCwdAndOnlyItsAuthoritativeBit(t *testing.T) {
	raw, err := os.ReadFile("testdata/qualification-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	release, err := LoadQualifiedRelease(raw, sha256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, workdir := range []string{"/workdir", "/workdir/home/user/project"} {
		for _, readOnly := range []bool{false, true} {
			for _, unconfined := range []bool{false, true} {
				in := bootstrapInput()
				in.Workdir, in.ReadOnlyWorkdir = workdir, readOnly
				in.Binding.Qualification = release.Identity()
				in.Environment["OPENSHELL_WORKSPACE_READ_ONLY"] = "true"
				config, err := NewLaunchConfig(in, release, strings.Repeat("f", 64), unconfined)
				if err != nil {
					t.Fatal(err)
				}
				args, err := config.SupervisorArgs(nil)
				if err != nil {
					t.Fatal(err)
				}
				if slices.Contains(args, "--workspace-read-only") != readOnly {
					t.Fatal("read-only flag did not follow authoritative intent")
				}
				index := slices.Index(args, "--workdir")
				if index < 0 || args[index+1] != workdir {
					t.Fatal("real project cwd was substituted")
				}
				if !reflect.DeepEqual(args[len(args)-3:], []string{"--", "sleep", "infinity"}) {
					t.Fatal("explicit idle child was lost")
				}
				boundary, err := config.BoundaryArgs()
				if err != nil {
					t.Fatal(err)
				}
				if slices.Contains(boundary, "--unmediated-network") != unconfined {
					t.Fatal("unmediated mode inferred from another input")
				}
				if boundary[len(boundary)-1] != boundaryBootstrapPath || slices.Contains(boundary, workdir) {
					t.Fatal("boundary launch received workspace chown argument")
				}
				if strings.Contains(strings.Join(args, " "), "test-secret") || strings.Contains(strings.Join(boundary, " "), "test-secret") {
					t.Fatal("workload environment was put in launch argv")
				}
			}
		}
	}
}

func TestLaunchRefusesOldSchemaMixedPairAndIdentityCollision(t *testing.T) {
	raw, err := os.ReadFile("testdata/qualification-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	release, err := LoadQualifiedRelease(raw, sha256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	in := bootstrapInput()
	in.Binding.Qualification = release.Identity()
	config, err := NewLaunchConfig(in, release, strings.Repeat("f", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*LaunchConfig){
		func(c *LaunchConfig) { c.Schema = 1 }, func(c *LaunchConfig) { c.HelperSHA256 = "old" },
		func(c *LaunchConfig) { c.Binding.Qualification = "0.1.6" },
		func(c *LaunchConfig) { c.UID = SupervisorUID }, func(c *LaunchConfig) { c.GID = SupervisorGID },
		func(c *LaunchConfig) { c.Workdir = "" },
	} {
		modified := config
		change(&modified)
		if _, err := modified.SupervisorArgs(nil); err == nil {
			t.Fatal("unsupported runtime config produced launch arguments")
		}
		if _, err := modified.BoundaryArgs(); err == nil {
			t.Fatal("unsupported runtime config produced boundary arguments")
		}
	}
	if _, err := NewLaunchConfig(in, QualifiedRelease{}, strings.Repeat("f", 64), false); err == nil {
		t.Fatal("unqualified pair accepted")
	}
	in.Binding.Qualification = strings.Repeat("0", 64)
	if _, err := NewLaunchConfig(in, release, strings.Repeat("f", 64), false); err == nil {
		t.Fatal("mixed qualification accepted")
	}
}

func TestBootstrapProtocolFixtureMatchesEmittedFieldsAndFlag(t *testing.T) {
	raw, err := os.ReadFile("../../third_party/openshell/bootstrap-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Schema           int      `json:"schema"`
		Version          string   `json:"protocol_fixture_version"`
		WorkspaceFlag    string   `json:"workspace_read_only_flag"`
		WorkspaceField   string   `json:"workspace_read_only_field"`
		WorkspaceDefault bool     `json:"workspace_read_only_default"`
		BoundaryFields   []string `json:"boundary_fields"`
		DescriptorFields []string `json:"descriptor_fields"`
		AuthFields       []string `json:"auth_fields"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Schema != BootstrapSchema || fixture.Version != protocolFixtureVersion || fixture.WorkspaceFlag != "--workspace-read-only" || fixture.WorkspaceField != "workspace_read_only" || fixture.WorkspaceDefault {
		t.Fatal("bootstrap/flag qualification fixture is stale")
	}
	for _, record := range []struct {
		value  any
		fields []string
	}{
		{boundaryConfig{}, fixture.BoundaryFields}, {runtimeDescriptor{}, fixture.DescriptorFields}, {authBundle{}, fixture.AuthFields},
	} {
		raw, err := json.Marshal(record.value)
		if err != nil {
			t.Fatal(err)
		}
		var actual map[string]any
		if err := json.Unmarshal(raw, &actual); err != nil {
			t.Fatal(err)
		}
		if len(actual) != len(record.fields) {
			t.Fatal("bootstrap record shape changed without a fixture update")
		}
		for _, field := range record.fields {
			if _, ok := actual[field]; !ok {
				t.Fatal("qualified protocol field missing:", field)
			}
		}
	}
}

// A box whose record says so runs the fixed launcher as the supervisor's main
// process, with a terminal and the main-exit marker, by one of the three fixed
// shells; any other shell is refused. Its argv carries no workload text.
func TestLaunchMainLauncherArgs(t *testing.T) {
	raw, err := os.ReadFile("testdata/qualification-fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	release, err := LoadQualifiedRelease(raw, sha256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	in := bootstrapInput()
	in.Binding.Qualification = release.Identity()
	config, err := NewLaunchConfig(in, release, strings.Repeat("f", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	config.MainLauncher = true
	for _, shell := range LauncherShells {
		args, err := config.SupervisorArgs(shell)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.Index(args, "--")
		want := append([]string{"-i", "--main-exit-marker", SupervisorMainExitMarker}, "--")
		if i < 3 || !reflect.DeepEqual(args[i-3:i+1], want) || !reflect.DeepEqual(args[i+1:len(args)-1], shell) || args[len(args)-1] != mainlaunch.LauncherScript {
			t.Fatalf("main launcher argv %q", args)
		}
		if strings.Contains(strings.Join(args, " "), "test-secret") {
			t.Fatal("workload environment was put in launch argv")
		}
	}
	for _, shell := range [][]string{nil, {"/bin/bash", "-c"}, {"/bin/zsh", "-c"}} {
		if _, err := config.SupervisorArgs(shell); err == nil {
			t.Fatalf("shell %q accepted", shell)
		}
	}
}
