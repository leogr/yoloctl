package features

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestGeminiImageSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	feature, err := Lookup("gemini")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(feature.RequiresCapabilities, []string{"network.gemini"}) {
		t.Fatalf("Gemini grants unexpected capabilities: %v", feature.RequiresCapabilities)
	}
	recipe, _, installs, err := InstallPlan("base", []string{"gemini"})
	if err != nil || !installs {
		t.Fatalf("Gemini install plan: installs=%v, error=%v", installs, err)
	}
	// Decode the JSON actually emitted by the Dockerfile. A quoting error or
	// putting defaults in the override file would change runtime behavior.
	re := regexp.MustCompile("'([^']+)' > (/etc/gemini-cli/[^ ]+)")
	files := map[string]map[string]any{}
	for _, match := range re.FindAllStringSubmatch(recipe, -1) {
		var settings map[string]any
		if err := json.Unmarshal([]byte(match[1]), &settings); err != nil {
			t.Fatal(err)
		}
		files[match[2]] = settings
	}
	want := map[string]map[string]any{
		"/etc/gemini-cli/settings.json": {
			"general": map[string]any{"enableAutoUpdate": false, "enableAutoUpdateNotification": false},
		},
		"/etc/gemini-cli/system-defaults.json": {
			"privacy": map[string]any{"usageStatisticsEnabled": false},
			"tools":   map[string]any{"shell": map[string]any{"enableInteractiveShell": false}},
		},
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("image settings = %#v, want %#v", files, want)
	}
	if strings.Contains(recipe, "/home/sandbox/.gemini") {
		t.Fatal("image recipe writes agent-owned account state")
	}
}
