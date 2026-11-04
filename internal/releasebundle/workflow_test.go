package releasebundle

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseWorkflowKeepsManualAndBuildJobsReadOnly(t *testing.T) {
	raw, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			If          string            `yaml:"if"`
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	if workflow.Permissions["contents"] != "read" || len(workflow.Permissions) != 1 {
		t.Fatal(workflow.Permissions)
	}
	expectedPermissions := map[string]map[string]string{
		"identity": {"contents": "read"}, "gate": {"contents": "read"},
		"dist": {"contents": "read"}, "macos": {"contents": "read"},
		"install-smoke": {"contents": "read"},
		"publish":       {"actions": "read", "contents": "write", "id-token": "write", "attestations": "write"},
	}
	if len(workflow.Jobs) != len(expectedPermissions) {
		t.Fatal("release jobs changed without a permission contract")
	}
	pin := regexp.MustCompile(`@[0-9a-f]{40}$`)
	for name, job := range workflow.Jobs {
		if name == "publish" {
			if !strings.Contains(job.If, "github.event_name == 'push'") || !strings.Contains(job.If, "refs/tags/v") {
				t.Fatal("manual publication is not excluded")
			}
		}
		effective := job.Permissions
		if effective == nil {
			effective = workflow.Permissions
		}
		if !reflect.DeepEqual(effective, expectedPermissions[name]) {
			t.Fatalf("%s effective permissions = %v, want %v", name, effective, expectedPermissions[name])
		}

		for _, step := range job.Steps {
			if step.Uses != "" && !pin.MatchString(step.Uses) {
				t.Fatalf("mutable action %s", step.Uses)
			}
		}
	}
	if _, ok := workflow.Jobs["install-smoke"]; !ok {
		t.Fatal("missing host installer gate")
	}
}

func TestNativeBuildJobsPrepareOpenShellToolchain(t *testing.T) {
	for _, tc := range []struct{ file, job, build string }{
		{"ci.yml", "build", "make build"},
		{"release.yml", "dist", "make dist"},
		{"release.yml", "install-smoke", "make dist"},
	} {
		raw, err := os.ReadFile("../../.github/workflows/" + tc.file)
		if err != nil {
			t.Fatal(err)
		}
		var workflow struct {
			Jobs map[string]struct {
				Steps []struct {
					ID   string `yaml:"id"`
					Run  string `yaml:"run"`
					Uses string `yaml:"uses"`
				} `yaml:"steps"`
			} `yaml:"jobs"`
		}
		if err := yaml.Unmarshal(raw, &workflow); err != nil {
			t.Fatal(err)
		}
		python, setup, build := -1, -1, -1
		for index, step := range workflow.Jobs[tc.job].Steps {
			if strings.HasPrefix(step.Uses, "actions/setup-python@") {
				python = index
			}
			if step.ID == "native-toolchain" && step.Run == "bash scripts/ci-openshell-toolchain.sh" {
				setup = index
			}
			for _, line := range strings.Split(step.Run, "\n") {
				if build < 0 && strings.HasPrefix(strings.TrimSpace(line), tc.build) {
					build = index // The first build must follow setup, even in a multiline step.
				}
			}
		}
		if python < 0 || setup <= python || build <= setup {
			t.Fatalf("%s/%s lacks Python/toolchain before native build: %d %d %d", tc.file, tc.job, python, setup, build)
		}
	}
}
