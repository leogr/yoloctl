package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/features"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/spf13/cobra"
)

// newFeatureCmd is the `feature` catalog command tree:
// ls / show / copy / edit / destroy over the built-in + custom feature
// registry. A feature is an installer that declares the capabilities it
// requires.
func newFeatureCmd() *cobra.Command {
	return newCatalogCmd(featureKind())
}

func featureKind() catalogKind {
	return catalogKind{
		name:         "feature",
		short:        "Manage features (installable components: built-in + custom)",
		builtinNames: func() ([]string, error) { return features.BuiltinNames(), nil },
		customNames:  customFeatureNames,
		isBuiltin:    features.IsBuiltin,
		exists:       featureExists,
		validateName: func(name string) error { return layout.ValidateName("feature", name) },
		writeShow:    writeFeatureShow,
		writeShowJSON: func(w io.Writer, name string) error {
			f, err := features.Lookup(name)
			if err != nil {
				return err
			}
			return writeFeatureShowJSON(w, f, features.IsBuiltin(name))
		},
		copy:              copyFeature,
		customPath:        featureCustomPath,
		validateAfterEdit: func(name string) error { _, err := features.Lookup(name); return err },
		deleteCustom:      features.DeleteUser,
		referrers:         featureReferrers,
	}
}

func customFeatureNames() ([]string, error) {
	user, err := features.LoadUserFeatures()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(user))
	for n := range user {
		out = append(out, n)
	}
	return out, nil
}

func featureExists(name string) (bool, error) {
	if features.IsBuiltin(name) {
		return true, nil
	}
	user, err := features.LoadUserFeatures()
	if err != nil {
		return false, err
	}
	_, ok := user[name]
	return ok, nil
}

func featureCustomPath(name string) (string, error) {
	dir, err := layout.FeaturesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".yaml"), nil
}

// copyFeature copies a built-in OR custom feature SRC to a new custom feature
// newName. Features are stored verbatim (no translation), so SaveUser marshals
// the resolved feature directly.
func copyFeature(src, newName string) error {
	f, err := features.Lookup(src)
	if err != nil {
		return err
	}
	f.Name = newName
	return features.SaveUser(f)
}

func writeFeatureShow(w io.Writer, name string) error {
	f, err := features.Lookup(name)
	if err != nil {
		return err
	}
	source := "custom"
	if features.IsBuiltin(name) {
		source = "built-in"
	}
	_, _ = fmt.Fprintf(w, "Name:        %s\n", f.Name)
	_, _ = fmt.Fprintf(w, "Source:      %s\n", source)
	if f.Description != "" {
		_, _ = fmt.Fprintf(w, "Description: %s\n", f.Description)
	}
	if len(f.RequiresCapabilities) > 0 {
		_, _ = fmt.Fprintf(w, "Requires capabilities: %v\n", f.RequiresCapabilities)
	} else {
		_, _ = fmt.Fprintln(w, "Requires capabilities: (none)")
	}
	if f.Install == nil {
		_, _ = fmt.Fprintln(w, "Install:     (none — tool lives in the base image)")
		return nil
	}
	_, _ = fmt.Fprintf(w, "Install kind: %s\n", f.Install.Kind)
	switch f.Install.Kind {
	case features.InstallNpm:
		version := f.Install.Version
		if version == "" {
			version = "latest"
		}
		_, _ = fmt.Fprintf(w, "  package: %s@%s\n", f.Install.Package, version)
	case features.InstallDockerfile:
		_, _ = fmt.Fprintf(w, "  dockerfile:\n%s\n", f.Install.Dockerfile)
	}
	return nil
}

func writeFeatureShowJSON(w io.Writer, f features.Feature, builtin bool) error {
	type installJSON struct {
		Kind       string `json:"kind"`
		Package    string `json:"package,omitempty"`
		Version    string `json:"version,omitempty"`
		Dockerfile string `json:"dockerfile,omitempty"`
	}
	type featureJSON struct {
		Name                 string       `json:"name"`
		Source               string       `json:"source"`
		Description          string       `json:"description,omitempty"`
		RequiresCapabilities []string     `json:"requires_capabilities,omitempty"`
		Install              *installJSON `json:"install,omitempty"`
	}
	source := "custom"
	if builtin {
		source = "built-in"
	}
	out := featureJSON{
		Name:                 f.Name,
		Source:               source,
		Description:          f.Description,
		RequiresCapabilities: f.RequiresCapabilities,
	}
	if f.Install != nil {
		out.Install = &installJSON{
			Kind:       f.Install.Kind,
			Package:    f.Install.Package,
			Version:    f.Install.Version,
			Dockerfile: f.Install.Dockerfile,
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
