package features

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"gopkg.in/yaml.v3"
)

// LoadUserFeatures discovers user-defined features under
// layout.FeaturesDir(). Each *.yaml file is one Feature; the
// filename (without `.yaml`) must equal the feature's `name:` field
// (so `claude.yaml` defines a feature named `claude`). Returns an
// empty map (no error) when the directory doesn't exist.
//
// Validation:
//   - filename ↔ name mismatch is an error (otherwise users would
//     get surprised by which name to pass on the CLI).
//   - network_policy.name is required when network_policy is set
//     (the composer keys network_policies by this; an empty key
//     would collide silently with other features).
//
// Errors include the offending file path so users see what to fix.
//
// Not cached: every call re-reads the dir. The dir is on the local
// filesystem and contains a handful of small YAML files; the cost
// is sub-millisecond. If feature discovery becomes hot, a
// sync.Once-guarded cache lands here without API changes.
func LoadUserFeatures() (map[string]Feature, error) {
	dir, err := layout.FeaturesDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]Feature, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		path := filepath.Join(dir, e.Name())
		// A custom feature may NOT shadow a built-in name: same-name
		// overrides are rejected loudly.
		if IsBuiltin(name) {
			return nil, fmt.Errorf("%s: %q is a built-in feature; custom items may not reuse a built-in name (copy it to a new name instead)", path, name)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var f Feature
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if f.Name == "" {
			return nil, fmt.Errorf("%s: `name` is required", path)
		}
		if f.Name != name {
			return nil, fmt.Errorf("%s: feature name %q must equal filename %q (rename the file or update the YAML)", path, f.Name, name)
		}
		if err := validateUserFeature(f); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out[name] = f
	}
	return out, nil
}

// validateUserFeature checks a user feature's install + capability shape.
// Network/filesystem access is no longer inline (it lives in
// internal/capabilities); a feature references capabilities by name.
func validateUserFeature(f Feature) error {
	if f.Install != nil {
		switch f.Install.Kind {
		case InstallDockerfile:
			if strings.TrimSpace(f.Install.Dockerfile) == "" {
				return fmt.Errorf("install.dockerfile is required for kind %q", f.Install.Kind)
			}
		case InstallNpm:
			if f.Install.Package == "" {
				return fmt.Errorf("install.package is required for kind %q", f.Install.Kind)
			}
		default:
			return fmt.Errorf("install.kind %q is invalid (want %s|%s)",
				f.Install.Kind, InstallDockerfile, InstallNpm)
		}
	}
	for i, c := range f.RequiresCapabilities {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("requires_capabilities[%d] is empty", i)
		}
	}
	return nil
}

// SaveUser writes a custom feature YAML to layout.FeaturesDir(). Features
// are stored verbatim (no high-level translation — unlike capabilities), so the
// copy is a direct marshal of f. Refuses a built-in name (the disjoint-namespace
// invariant) and validates the result before writing. Used by `feature copy`.
func SaveUser(f Feature) error {
	if err := layout.ValidateName("feature", f.Name); err != nil {
		return err
	}
	if IsBuiltin(f.Name) {
		return fmt.Errorf("%q is a built-in feature; choose a different name for the custom copy", f.Name)
	}
	if err := validateUserFeature(f); err != nil {
		return fmt.Errorf("feature %q: %w", f.Name, err)
	}
	raw, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	dir, err := layout.FeaturesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, f.Name+".yaml"), raw, 0o644)
}

// DeleteUser removes a custom feature file. Refuses a built-in name. Returns
// fs.ErrNotExist when no custom file exists for name. Used by `feature destroy`.
func DeleteUser(name string) error {
	if err := layout.ValidateName("feature", name); err != nil {
		return err
	}
	if IsBuiltin(name) {
		return fmt.Errorf("%q is a built-in feature and cannot be deleted", name)
	}
	dir, err := layout.FeaturesDir()
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, name+".yaml"))
}
