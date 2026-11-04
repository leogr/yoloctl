package helperinputs

import (
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"
	"testing/fstest"
)

func inputFixture(t *testing.T) (fstest.MapFS, *debug.BuildInfo) {
	t.Helper()
	raw, err := inputs.ReadFile("scripts/openshell-helper-recipe.json")
	if err != nil {
		t.Fatal(err)
	}
	tree := fstest.MapFS{
		"helperinputs.go": {Data: []byte("package helperinputs\n")},
		"cmd/yoloctl-openshell-helper/main_linux.go": {Data: []byte("package main\nimport _ \"github.com/leogr/yoloctl/internal/openshell\"\n")},
		"internal/openshell/runtime_linux.go":        {Data: []byte("package openshell\n")},
		"go.mod":                                     {Data: []byte("module github.com/leogr/yoloctl\ngo 1.27\n")},
		"go.sum":                                     {Data: []byte("fixture\n")},
		"scripts/openshell-helper-recipe.json":       {Data: raw},
	}
	info := &debug.BuildInfo{}
	for _, p := range []string{"golang.org/x/crypto", "golang.org/x/mod", "golang.org/x/sys", "gopkg.in/yaml.v3"} {
		info.Deps = append(info.Deps, &debug.Module{Path: p, Version: "v1.0.0", Sum: "h1:fixture"})
	}
	return tree, info
}

func TestProductionInputsNoticeNewFilesAndIgnoreOutsideCLIAndTests(t *testing.T) {
	tree, info := inputFixture(t)
	before, err := identify(tree, info)
	if err != nil {
		t.Fatal(err)
	}
	tree["internal/openshell/added_linux.go"] = &fstest.MapFile{Data: []byte("package openshell\nconst added = 1\n")}
	after, err := identify(tree, info)
	if err != nil || before.SourceSHA256 == after.SourceSHA256 {
		t.Fatal("new production input was missed", err)
	}
	tree["internal/openshell/added_linux.go"].Data = []byte("package openshell\nconst added = 2\n")
	edited, err := identify(tree, info)
	if err != nil || edited.SourceSHA256 == after.SourceSHA256 {
		t.Fatal("changed production input was missed", err)
	}
	tree["cmd/yoloctl/unrelated.go"] = &fstest.MapFile{Data: []byte("package main\nconst unrelated = 1\n")}
	tree["internal/openshell/added_test.go"] = &fstest.MapFile{Data: []byte("not even production Go source")}
	unchanged, err := identify(tree, info)
	if err != nil || unchanged.SourceSHA256 != edited.SourceSHA256 {
		t.Fatal("unrelated CLI/test edit changed helper identity", err)
	}
}

func TestSourceClosureRefusesUncapturedDependencyAndCgo(t *testing.T) {
	for _, imported := range []string{"github.com/leogr/yoloctl/internal/newdependency", "example.com/unqualified/module", "C"} {
		t.Run(imported, func(t *testing.T) {
			tree, info := inputFixture(t)
			tree["internal/openshell/runtime_linux.go"].Data = []byte("package openshell\nimport _ " + strconvQuote(imported) + "\n")
			if _, err := identify(tree, info); err == nil {
				t.Fatal("uncaptured source dependency accepted")
			}
		})
	}
}

func strconvQuote(s string) string { raw, _ := json.Marshal(s); return string(raw) }

func TestSourceIdentityIncludesActualEmbedData(t *testing.T) {
	tree, info := inputFixture(t)
	tree["internal/openshell/runtime_linux.go"].Data = []byte("package openshell\nimport _ \"embed\"\n//go:embed \"testdata/data with spaces\"\nvar data string\n")
	tree["internal/openshell/testdata/data with spaces"] = &fstest.MapFile{Data: []byte("one")}
	before, err := identify(tree, info)
	if err != nil {
		t.Fatal(err)
	}
	tree["internal/openshell/testdata/data with spaces"].Data = []byte("two")
	after, err := identify(tree, info)
	if err != nil || before.SourceSHA256 == after.SourceSHA256 {
		t.Fatal("build-selected embedded bytes were missed", err)
	}
	delete(tree, "internal/openshell/testdata/data with spaces")
	if _, err := identify(tree, info); err == nil {
		t.Fatal("missing build-selected embed input accepted")
	}
}

func TestModuleSelectionIgnoresUnrelatedAndRefusesReplacement(t *testing.T) {
	tree, info := inputFixture(t)
	before, err := identify(tree, info)
	if err != nil {
		t.Fatal(err)
	}
	info.Deps = append(info.Deps, &debug.Module{Path: "example.com/cli-only", Version: "v2.0.0", Sum: "h1:unrelated"})
	after, err := identify(tree, info)
	if err != nil || !equalIdentity(before, after) {
		t.Fatal("unrelated CLI dependency contaminated helper module selection", err)
	}
	tree["go.mod"].Data = append(tree["go.mod"].Data, []byte("require example.com/cli-only v2.0.0+incompatible\n")...)
	tree["go.sum"].Data = append(tree["go.sum"].Data, []byte("\nexample.com/cli-only v2.0.0+incompatible h1:unrelated\n")...)
	withModuleEdit, err := identify(tree, info)
	if err != nil || !equalIdentity(before, withModuleEdit) {
		t.Fatal("outside-closure module-file edit changed helper identity", err)
	}
	info.Deps[0].Replace = &debug.Module{Path: "../untracked"}
	if _, err := identify(tree, info); err == nil {
		t.Fatal("untracked replaced dependency accepted")
	}
}

func equalIdentity(a, b Identity) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestEmbeddedClosureCapturesCurrentCommandAndRuntime(t *testing.T) {
	raw, err := inputs.ReadFile("scripts/openshell-helper-recipe.json")
	if err != nil {
		t.Fatal(err)
	}
	var selected recipe
	if err := json.Unmarshal(raw, &selected); err != nil {
		t.Fatal(err)
	}
	names, err := sourceNames(inputs, selected.Modules)
	if err != nil {
		t.Fatal(err)
	}
	var command, runtime, operator, buildIdentity bool
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			t.Fatal("test source entered helper identity", name)
		}
		command = command || name == commandPath+"/main_linux.go"
		runtime = runtime || name == "internal/openshell/runtime_main_linux.go"
		operator = operator || name == "internal/operator/endpoint.go"
		buildIdentity = buildIdentity || name == "internal/buildinfo/buildinfo.go"
	}
	if !command || !runtime || !operator || !buildIdentity {
		t.Fatal("helper closure missed command/runtime/operator/buildinfo")
	}
}

func TestProductionInputsFollowAddedOperatorDependencyAndItsNewFiles(t *testing.T) {
	tree, info := inputFixture(t)
	tree["internal/openshell/runtime_linux.go"].Data = []byte("package openshell\nimport _ \"github.com/leogr/yoloctl/internal/operator\"\n")
	if _, err := identify(tree, info); err == nil {
		t.Fatal("missing imported operator source became qualified")
	}
	tree["internal/operator/endpoint.go"] = &fstest.MapFile{Data: []byte("package operator\n")}
	before, err := identify(tree, info)
	if err != nil {
		t.Fatal(err)
	}
	tree["internal/operator/added_linux.go"] = &fstest.MapFile{Data: []byte("package operator\nconst added = 1\n")}
	added, err := identify(tree, info)
	if err != nil || equalIdentity(before, added) {
		t.Fatal("added imported operator input missed", err)
	}
	tree["internal/operator/added_linux.go"].Data = []byte("package operator\nconst added = 2\n")
	edited, err := identify(tree, info)
	if err != nil || equalIdentity(added, edited) {
		t.Fatal("edited imported operator input missed", err)
	}
	tree["internal/operator/added_test.go"] = &fstest.MapFile{Data: []byte("excluded test source")}
	unchanged, err := identify(tree, info)
	if err != nil || !equalIdentity(edited, unchanged) {
		t.Fatal("imported operator test contaminated source identity", err)
	}
}
