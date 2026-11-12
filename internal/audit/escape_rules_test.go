package audit

import (
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type escapeRuleEntry struct {
	Rule, Macro, List, Condition, Output, Priority string
	Enabled                                        *bool
	Items                                          []string
}

func escapeEntries(t *testing.T) map[string]escapeRuleEntry {
	t.Helper()
	var entries []escapeRuleEntry
	if err := yaml.Unmarshal([]byte(Rules), &entries); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]escapeRuleEntry)
	for _, entry := range entries {
		name := entry.Rule
		if name == "" {
			name = entry.Macro
		}
		if name == "" {
			name = entry.List
		}
		if _, exists := result[name]; exists {
			t.Fatal("duplicate rule/macro/list", name)
		}
		entry.Condition = strings.Join(strings.Fields(entry.Condition), " ")
		result[name] = entry
	}
	return result
}

func TestEscapeFamilyPreservesExistingRulesExactly(t *testing.T) {
	before, _, ok := strings.Cut(Rules, "\n# Escape candidates:")
	if !ok {
		t.Fatal("escape family boundary missing")
	}
	// Complete original rules.yaml at 3e04d2b, including the broker exception.
	const old = "6b1b30b7d90ec4a614365e0d083e34d64bcd30fb9aed623129212b9d36742bae"
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(before))); got != old {
		t.Fatalf("pre-existing rules changed: %s", got)
	}
}

func TestEscapeFamilySelectionAndEnablement(t *testing.T) {
	entries := escapeEntries(t)
	const context = `(container.label[yoloctl.audit.schema] = "1" and container.label[yoloctl.audit.runtime_mode] = confined and container.label[yoloctl.openshell.protocol] = "2")`
	if entries["yolo_confined_box"].Condition != context {
		t.Fatal(entries["yolo_confined_box"])
	}
	paths := []string{"/.openshell/runtime/openshell-sandbox", "/.openshell/runtime/openshell-supervisor"}
	if !slices.Equal(entries["yolo_openshell_runtime_paths"].Items, paths) {
		t.Fatal(entries["yolo_openshell_runtime_paths"])
	}
	const descendant = "(yolo_confined_box and proc.aexepath in (yolo_openshell_runtime_paths) and not proc.exepath in (yolo_openshell_runtime_paths) and not proc.exepath = /opt/yoloctl/openshell-helper/yoloctl-openshell-helper)"
	if entries["yolo_runtime_descendant"].Condition != descendant {
		t.Fatal(entries["yolo_runtime_descendant"])
	}
	for name, enabled := range map[string]bool{
		"Yolo PATH directory write": false, "Yolo unexpected confined write": false,
		"Yolo connect outside inner netns": false, "Yolo UID set to root": true,
		"Yolo root process": false, "Yolo container escape": false,
		"Yolo device access": false, "Yolo audit escape test": true,
	} {
		entry, ok := entries[name]
		if !ok {
			t.Fatal("missing", name)
		}
		if got := entry.Enabled == nil || *entry.Enabled; got != enabled {
			t.Fatal(name, "enablement", got)
		}
		if name != "Yolo container escape" && !strings.HasPrefix(entry.Condition, "yolo_runtime_descendant and ") {
			t.Fatal(name, "selector missing")
		}
		for _, process := range []string{"containerd", "buildkitd", "nerdctl", "shim"} {
			if strings.Contains(entry.Condition, process) {
				t.Fatal(name, "runtime exemption", process)
			}
		}
	}
	uid := entries["Yolo UID set to root"]
	if uid.Condition != "yolo_runtime_descendant and evt.type in (setuid, setreuid, setresuid) and evt.res = SUCCESS and (evt.arg.uid = 0 or evt.arg.ruid = 0 or evt.arg.euid = 0 or evt.arg.suid = 0)" || uid.Priority != "CRITICAL" {
		t.Fatal(uid)
	}
	control := entries["Yolo audit escape test"]
	if control.Condition != "yolo_runtime_descendant and evt.type in (execve, execveat) and proc.name = yolo-esc-test" || control.Priority != "NOTICE" || len("yolo-esc-test") > 15 {
		t.Fatal(control)
	}
	// Host classification deliberately has no label requirements: those fields
	// are absent for host threads. It remains disabled, never escape proof.
	if entries["Yolo container escape"].Condition != "evt.type in (execve, execveat) and evt.res = SUCCESS and container.id = host and proc.aexepath in (yolo_openshell_runtime_paths)" {
		t.Fatal(entries["Yolo container escape"])
	}
}

func TestEscapePATHRegexTracksComposerAndDirectChildren(t *testing.T) {
	source, err := parser.ParseFile(token.NewFileSet(), "../agentbox/compose.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var pathDirs []string
	ast.Inspect(source, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "pathDirs" {
			return true
		}
		literal, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			t.Fatal("pathDirs is no longer a literal")
		}
		for _, element := range literal.Elts {
			value, ok := element.(*ast.BasicLit)
			if !ok {
				t.Fatal("nonliteral path directory")
			}
			path, err := strconv.Unquote(value.Value)
			if err != nil {
				t.Fatal(err)
			}
			pathDirs = append(pathDirs, path)
		}
		return false
	})
	if len(pathDirs) == 0 {
		t.Fatal("composer pathDirs not found")
	}
	condition := escapeEntries(t)["Yolo PATH directory write"].Condition
	if strings.Contains(condition, "pmatch") || strings.Contains(condition, "startswith") {
		t.Fatal("subtree matching is not invariant", condition)
	}
	regexes := regexp.MustCompile(`fs\.path\.(name|source|target) regex "([^"]+)"`).FindAllStringSubmatch(condition, -1)
	if len(regexes) != 3 {
		t.Fatal(condition)
	}
	expected := "^(" + strings.Join(pathDirs, "|") + ")/[^/]+$"
	for _, match := range regexes {
		if match[2] != expected {
			t.Fatal("composer and rules diverge", match[2], expected)
		}
	}
	if !strings.Contains(condition, `(yolo_fs_target_mutation and fs.path.target regex "`+expected+`"))`) {
		t.Fatal("symlink payload guard missing", condition)
	}
	re := regexp.MustCompile(expected)
	for _, path := range pathDirs {
		if !re.MatchString(path+"/tool") || re.MatchString(path) || re.MatchString(path+"/project/tool") {
			t.Fatal(path)
		}
	}
	// A writable single-file bind at a direct child remains a counterexample;
	// a writable directory's contents do not match. This is why 1a is disabled.
	if !re.MatchString("/usr/local/bin/tool") || re.MatchString("/usr/local/bin/project/x") {
		t.Fatal("mount characterization changed")
	}
}

func TestEscapeNetworkRequiresKnownTupleAndExplicitConfinement(t *testing.T) {
	condition := escapeEntries(t)["Yolo connect outside inner netns"].Condition
	const expected = `yolo_runtime_descendant and evt.type = connect and evt.res in (SUCCESS, EINPROGRESS) and fd.type in (ipv4, ipv6) and container.label[yoloctl.audit.network_unconfined] = "false" and fd.cip exists and fd.cip != "10.200.0.2" and fd.rnet != "127.0.0.0/8" and fd.rip != "::1"`
	if condition != expected {
		t.Fatal(condition)
	}
	for _, field := range []string{"fd.cip", "fd.rip", "fd.rnet"} {
		if strings.Contains(condition, "not "+field+" =") {
			t.Fatal("missing tuple could look like an escape", condition)
		}
	}
}

func TestEscapeDeviceRuleKeepsPTYAndDescriptorAliases(t *testing.T) {
	condition := escapeEntries(t)["Yolo device access"].Condition
	const expected = "yolo_runtime_descendant and evt.type in (open, openat, openat2) and evt.res = SUCCESS and fs.path.name startswith /dev/ and not fs.path.name in (/dev/null, /dev/zero, /dev/urandom, /dev/random, /dev/tty, /dev/ptmx, /dev/stdin, /dev/stdout, /dev/stderr) and not fs.path.name startswith /dev/pts/ and not fs.path.name startswith /dev/fd/"
	if condition != expected {
		t.Fatal(condition)
	}
}

// These fixtures interpret only the network rule's conjunction of fixed scalar
// comparisons/membership. They do not emulate Falco tuple extraction or runtime
// ancestry; the pinned engine and separate host diagnostic qualify those.
func TestEscapeNetworkPredicateFixtures(t *testing.T) {
	condition := escapeEntries(t)["Yolo connect outside inner netns"].Condition
	membership := regexp.MustCompile(`^([^ ]+) in \(([^)]+)\)$`)
	comparison := regexp.MustCompile(`^([^ ]+) (!=|=) (.+)$`)
	matches := func(event map[string]string) bool {
		for _, clause := range strings.Split(condition, " and ") {
			if clause == "yolo_runtime_descendant" {
				if event[clause] != "true" {
					return false
				}
				continue
			}
			if key, ok := strings.CutSuffix(clause, " exists"); ok {
				if event[key] == "" || event[key] == "<NA>" {
					return false
				}
				continue
			}
			if fields := membership.FindStringSubmatch(clause); fields != nil {
				if !slices.Contains(strings.Split(fields[2], ", "), event[fields[1]]) {
					return false
				}
				continue
			}
			fields := comparison.FindStringSubmatch(clause)
			if fields == nil {
				t.Fatal("unsupported fixture grammar", clause)
			}
			actual := event[fields[1]]
			if actual == "" || actual == "<NA>" {
				return false
			}
			equal := actual == strings.Trim(fields[3], `"`)
			if (fields[2] == "=" && !equal) || (fields[2] == "!=" && equal) {
				return false
			}
		}
		return true
	}
	for _, tc := range []struct {
		name, key, value string
		want             bool
	}{
		{"outside", "fd.cip", "192.0.2.10", true},
		{"in-progress", "evt.res", "EINPROGRESS", true},
		{"same-inner-different-remote", "fd.cip", "10.200.0.2", false},
		{"another-remote-outside", "fd.rip", "198.51.100.2", true},
		{"loopback-v4", "fd.rnet", "127.0.0.0/8", false},
		{"loopback-v6", "fd.rip", "::1", false},
		{"unix-socket", "fd.type", "unix", false},
		{"absent-client", "fd.cip", "", false},
		{"absent-remote", "fd.rip", "", false},
		{"unknown-tuple", "fd.cip", "<NA>", false},
		{"unconfined-network", "container.label[yoloctl.audit.network_unconfined]", "true", false},
		{"missing-network-label", "container.label[yoloctl.audit.network_unconfined]", "", false},
		{"outer-maintenance", "yolo_runtime_descendant", "false", false},
		{"refused-connect", "evt.res", "EPERM", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := map[string]string{
				"yolo_runtime_descendant": "true", "evt.type": "connect", "evt.res": "SUCCESS", "fd.type": "ipv4",
				"container.label[yoloctl.audit.network_unconfined]": "false", "fd.cip": "192.0.2.10", "fd.rip": "198.51.100.1", "fd.rnet": "198.51.100.0/24",
			}
			event[tc.key] = tc.value
			if got := matches(event); got != tc.want {
				t.Fatal(tc.name, got, tc.want)
			}
		})
	}
}
