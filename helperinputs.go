// Package helperinputs binds the separately released OpenShell helper to this
// checkout's production inputs. It is a leaf at the module root because Go
// embed patterns cannot traverse to a parent directory. No generated copy or
// make invocation is needed to notice an added or edited production file.
package helperinputs

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// The directory patterns deliberately include new files and embed data. The
// runtime walker selects production package inputs, not tests or an inventory
// generated from a previous checkout. An import outside this embedded closure
// refuses qualification until the embedding and the release proof are updated.
//
//go:embed *.go all:cmd/yoloctl-openshell-helper all:internal/openshell all:internal/vmanchor all:internal/workercheckpoint all:internal/operator all:internal/atomicfile all:internal/buildinfo all:internal/layout all:internal/safepath all:internal/spaceenv scripts/openshell-helper-recipe.json go.mod go.sum
var inputs embed.FS

const (
	Schema      = 1
	modulePath  = "github.com/leogr/yoloctl"
	commandPath = "cmd/yoloctl-openshell-helper"
)

type Module struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	Sum     string `json:"sum"`
}

// Identity is a source and dependency binding, never execution authority or a
// replacement for the reviewed helper binary hash/native activation exchange.
type Identity struct {
	Schema       int      `json:"schema"`
	SourceSHA256 string   `json:"source_sha256"`
	Modules      []Module `json:"modules"`
}

type recipe struct {
	Schema        int      `json:"schema"`
	Command       string   `json:"command"`
	GOOS          string   `json:"goos"`
	CGOEnabled    string   `json:"cgo_enabled"`
	Architectures []string `json:"architectures"`
	Flags         []string `json:"flags"`
	Modules       []string `json:"modules"`
}

// Current reads only bytes embedded by this compilation and its module build
// information. It never opens a checkout, invokes a compiler or refreshes pins.
// The producer's Go version is separately pinned in helper build qualification;
// an unrelated CLI/toolchain upgrade does not silently change helper bytes.
func Current() (Identity, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Identity{}, errors.New("OpenShell helper source identity needs compiled module information")
	}
	return identify(inputs, info)
}

// CheckModules compares the modules actually compiled into an executable with
// the qualified helper closure. CLI-only dependencies are not helper inputs.
func CheckModules(info *debug.BuildInfo, expected []Module) error {
	paths := make([]string, len(expected))
	for i, module := range expected {
		paths[i] = module.Path
	}
	actual, err := selectedModules(info, paths)
	if err != nil {
		return err
	}
	if !slices.Equal(actual, expected) {
		return errors.New("compiled helper dependency versions differ from qualification")
	}
	return nil
}

// ModulePaths is the ordered dependency inventory selected by the embedded
// helper recipe. Manifest shape checks use it without trusting runtime build
// information or accepting a smaller caller-supplied dependency list.
func ModulePaths() ([]string, error) {
	raw, err := inputs.ReadFile("scripts/openshell-helper-recipe.json")
	if err != nil {
		return nil, err
	}
	var selected recipe
	if err := json.Unmarshal(raw, &selected); err != nil {
		return nil, err
	}
	return slices.Clone(selected.Modules), nil
}

func identify(tree fs.FS, info *debug.BuildInfo) (Identity, error) {
	raw, err := fs.ReadFile(tree, "scripts/openshell-helper-recipe.json")
	if err != nil {
		return Identity{}, err
	}
	var selected recipe
	if err := json.Unmarshal(raw, &selected); err != nil {
		return Identity{}, err
	}
	if selected.Schema != Schema || selected.Command != commandPath || selected.GOOS != "linux" || selected.CGOEnabled != "0" ||
		!slices.Equal(selected.Architectures, []string{"amd64", "arm64"}) ||
		!slices.Equal(selected.Flags, []string{"-buildvcs=false", "-trimpath", "-ldflags=-s -w -buildid="}) {
		return Identity{}, errors.New("unqualified OpenShell helper build recipe")
	}
	if !slices.IsSorted(selected.Modules) || len(selected.Modules) == 0 {
		return Identity{}, errors.New("helper dependency inventory is not canonical")
	}
	for i, p := range selected.Modules {
		if p == "" || (i > 0 && selected.Modules[i-1] == p) {
			return Identity{}, errors.New("invalid helper dependency inventory")
		}
	}
	names, err := sourceNames(tree, selected.Modules)
	if err != nil {
		return Identity{}, err
	}
	names = append(names, "scripts/openshell-helper-recipe.json")
	modules, err := selectedModules(info, selected.Modules)
	if err != nil {
		return Identity{}, err
	}
	selection, checksums, err := dependencyInputs(tree, modules)
	if err != nil {
		return Identity{}, err
	}
	slices.Sort(names)
	hash := sha256.New()
	_, _ = fmt.Fprintln(hash, "yoloctl-openshell-helper-inputs-v1")
	for _, name := range slices.Compact(names) {
		body, err := fs.ReadFile(tree, name)
		if err != nil {
			return Identity{}, err
		}
		// Length-framing distinguishes paths, concatenations and embedded NULs.
		_, _ = fmt.Fprintf(hash, "%d:%s:%d:", len(name), name, len(body))
		_, _ = hash.Write(body)
	}
	for _, input := range []struct {
		name string
		body []byte
	}{{"go.mod/helper-selection", selection}, {"go.sum/helper-selection", checksums}} {
		_, _ = fmt.Fprintf(hash, "%d:%s:%d:", len(input.name), input.name, len(input.body))
		_, _ = hash.Write(input.body)
	}
	return Identity{Schema, hex.EncodeToString(hash.Sum(nil)), modules}, nil
}

// Module files are embedded directly, but only the helper's actual dependency
// selection is part of its identity. An unrelated host-CLI dependency update
// must not force a new static helper. Go/toolchain/default semantics and every
// directive affecting a helper module remain bound alongside compiled sums.
func dependencyInputs(tree fs.FS, modules []Module) ([]byte, []byte, error) {
	raw, err := fs.ReadFile(tree, "go.mod")
	if err != nil {
		return nil, nil, err
	}
	file, err := modfile.Parse("go.mod", raw, nil)
	if err != nil {
		return nil, nil, err
	}
	if file.Module == nil || file.Module.Mod.Path != modulePath || file.Go == nil {
		return nil, nil, errors.New("helper source module/Go selection is incomplete")
	}
	selected := map[string]string{}
	for _, module := range modules {
		selected[module.Path] = module.Version
	}
	selection := struct {
		Module    string   `json:"module"`
		Go        string   `json:"go"`
		Toolchain string   `json:"toolchain"`
		Godebug   []string `json:"godebug"`
		Require   []string `json:"require"`
		Exclude   []string `json:"exclude"`
		Replace   []string `json:"replace"`
	}{Module: file.Module.Mod.Path, Go: file.Go.Version}
	if file.Toolchain != nil {
		selection.Toolchain = file.Toolchain.Name
	}
	for _, entry := range file.Godebug {
		selection.Godebug = append(selection.Godebug, entry.Key+"="+entry.Value)
	}
	for _, entry := range file.Require {
		if _, ok := selected[entry.Mod.Path]; ok {
			selection.Require = append(selection.Require, fmt.Sprintf("%s@%s indirect=%t", entry.Mod.Path, entry.Mod.Version, entry.Indirect))
		}
	}
	for _, entry := range file.Exclude {
		if _, ok := selected[entry.Mod.Path]; ok {
			selection.Exclude = append(selection.Exclude, entry.Mod.Path+"@"+entry.Mod.Version)
		}
	}
	for _, entry := range file.Replace {
		if _, ok := selected[entry.Old.Path]; ok {
			selection.Replace = append(selection.Replace, entry.Old.Path+"@"+entry.Old.Version+"=>"+entry.New.Path+"@"+entry.New.Version)
		}
	}
	slices.Sort(selection.Require)
	slices.Sort(selection.Exclude)
	slices.Sort(selection.Replace)
	selectionRaw, err := json.Marshal(selection)
	if err != nil {
		return nil, nil, err
	}
	raw, err = fs.ReadFile(tree, "go.sum")
	if err != nil {
		return nil, nil, err
	}
	var sums []string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		if version, ok := selected[fields[0]]; ok && (fields[1] == version || fields[1] == version+"/go.mod") {
			sums = append(sums, strings.Join(fields, " "))
		}
	}
	slices.Sort(sums)
	return selectionRaw, []byte(strings.Join(sums, "\n")), nil
}

func selectedModules(info *debug.BuildInfo, paths []string) ([]Module, error) {
	if info == nil {
		return nil, errors.New("missing compiled helper dependencies")
	}
	var selected []Module
	for _, name := range paths {
		var found *debug.Module
		for _, candidate := range info.Deps {
			if candidate.Path == name {
				if found != nil {
					return nil, errors.New("duplicate compiled helper dependency")
				}
				found = candidate
			}
		}
		// Refuse replacements, including untracked local directories. Supporting
		// one requires adding its actual bytes to the embedded/proved inventory.
		if found == nil || found.Replace != nil || !strings.HasPrefix(found.Version, "v") || !strings.HasPrefix(found.Sum, "h1:") {
			return nil, fmt.Errorf("OpenShell helper dependency %s is missing, replaced or unqualified", name)
		}
		selected = append(selected, Module{name, found.Version, found.Sum})
	}
	return selected, nil
}

func sourceNames(tree fs.FS, modules []string) ([]string, error) {
	queue := []string{".", commandPath}
	seen := map[string]bool{}
	names := map[string]bool{}
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		if seen[dir] {
			continue
		}
		seen[dir] = true
		entries, err := fs.ReadDir(tree, dir)
		if err != nil {
			return nil, fmt.Errorf("helper production dependency %s is outside the embedded source closure: %w", dir, err)
		}
		production := 0
		for _, entry := range entries {
			base := entry.Name()
			if entry.IsDir() || strings.HasPrefix(base, "_") || strings.HasPrefix(base, ".") || strings.HasSuffix(base, "_test.go") {
				continue
			}
			name := path.Join(dir, base)
			if path.Ext(base) != ".go" {
				// Include native assembler/headers and prebuilt object inputs, even
				// when selected only on the other supported guest architecture.
				if slices.Contains([]string{".s", ".S", ".h", ".syso", ".c", ".cc", ".cpp"}, path.Ext(base)) {
					names[name] = true
				}
				continue
			}
			production++
			names[name] = true
			body, err := fs.ReadFile(tree, name)
			if err != nil {
				return nil, err
			}
			file, err := parser.ParseFile(token.NewFileSet(), name, body, parser.ParseComments)
			if err != nil {
				return nil, fmt.Errorf("helper source %s: %w", name, err)
			}
			for _, spec := range file.Imports {
				imported, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return nil, err
				}
				switch {
				case imported == "C":
					return nil, errors.New("helper source must not require cgo")
				case imported == modulePath:
					queue = append(queue, ".")
				case strings.HasPrefix(imported, modulePath+"/"):
					queue = append(queue, strings.TrimPrefix(imported, modulePath+"/"))
				case strings.Contains(strings.Split(imported, "/")[0], "."):
					if !coveredModule(imported, modules) {
						return nil, fmt.Errorf("helper import %s is outside its dependency qualification", imported)
					}
				}
			}
			// The identity package's own broad embed is the acquisition mechanism,
			// not a request to count tests or unrelated snapshot data as inputs.
			if dir != "." {
				if err := embedNames(tree, dir, file, names); err != nil {
					return nil, err
				}
			}
		}
		if production == 0 {
			return nil, fmt.Errorf("helper package %s has no embedded production source", dir)
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	slices.Sort(result)
	return result, nil
}

func coveredModule(imported string, modules []string) bool {
	for _, name := range modules {
		if imported == name || strings.HasPrefix(imported, name+"/") {
			return true
		}
	}
	return false
}

func embedNames(tree fs.FS, dir string, file *ast.File, names map[string]bool) error {
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if !strings.HasPrefix(comment.Text, "//go:embed ") {
				continue
			}
			patterns, err := embedPatterns(strings.TrimPrefix(comment.Text, "//go:embed "))
			if err != nil {
				return err
			}
			for _, pattern := range patterns {
				all := strings.HasPrefix(pattern, "all:")
				pattern = strings.TrimPrefix(pattern, "all:")
				if !fs.ValidPath(pattern) || pattern == "." {
					return errors.New("invalid helper embed pattern")
				}
				matches, err := fs.Glob(tree, path.Join(dir, pattern))
				if err != nil || len(matches) == 0 {
					return fmt.Errorf("helper embed pattern %q has no captured input", pattern)
				}
				for _, match := range matches {
					err := fs.WalkDir(tree, match, func(name string, entry fs.DirEntry, err error) error {
						if err != nil {
							return err
						}
						if name != match && !all && (strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_")) {
							if entry.IsDir() {
								return fs.SkipDir
							}
							return nil
						}
						if !entry.IsDir() {
							names[name] = true
						}
						return nil
					})
					if err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func embedPatterns(text string) ([]string, error) {
	var result []string
	for text = strings.TrimSpace(text); text != ""; text = strings.TrimSpace(text) {
		if text[0] != '"' && text[0] != '`' {
			end := strings.IndexAny(text, " \t\r\n")
			if end < 0 {
				end = len(text)
			}
			result = append(result, text[:end])
			text = text[end:]
			continue
		}
		quote := text[0]
		end := 1
		for end < len(text) {
			if text[end] == quote {
				break
			}
			if quote == '"' && text[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(text) {
			return nil, errors.New("unterminated helper embed pattern")
		}
		value, err := strconv.Unquote(text[:end+1])
		if err != nil {
			return nil, err
		}
		result = append(result, value)
		text = text[end+1:]
	}
	return result, nil
}
