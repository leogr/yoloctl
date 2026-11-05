package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/spf13/cobra"
)

// agentCatalogCommands returns the catalog verbs (ls / show / copy / edit /
// destroy) added to the existing `agent` command tree. The
// Built-in agents (agy, claude, codex, gemini, opencode, pi) are embedded read-only; custom definitions
// live on disk. Copy/edit/destroy gate on agentprofile.IsBuiltin.
func agentCatalogCommands() []*cobra.Command {
	k := agentKind()
	return []*cobra.Command{
		newCatalogLsCmd(k),
		newCatalogShowCmd(k),
		newCatalogCopyCmd(k),
		newCatalogEditCmd(k),
		newCatalogDestroyCmd(k),
	}
}

func agentKind() catalogKind {
	return catalogKind{
		name:         "agent",
		short:        "Manage agents",
		builtinNames: agentprofile.BuiltinNames,
		customNames:  customAgentNames,
		isBuiltin:    agentprofile.IsBuiltin,
		exists:       agentExists,
		validateName: func(name string) error { return layout.ValidateName("agent-profile", name) },
		writeShow:    writeAgentShow,
		writeShowJSON: func(w io.Writer, name string) error {
			p, err := agentprofile.Lookup(name)
			if err != nil {
				return err
			}
			return writeAgentShowJSON(w, p, agentprofile.IsBuiltin(name))
		},
		copy:              copyAgent,
		customPath:        func(name string) (string, error) { return layout.AgentPath(name) },
		validateAfterEdit: func(name string) error { _, err := agentprofile.Lookup(name); return err },
		deleteCustom:      agentprofile.Delete,
		referrers:         agentReferrers,
	}
}

// customAgentNames returns the on-disk custom agent definitions, sorted.
func customAgentNames() ([]string, error) {
	all, err := agentprofile.Names()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for _, n := range all {
		if !agentprofile.IsBuiltin(n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// agentExists reports whether name resolves to any agent profile (built-in or
// custom). A wrapped fs.ErrNotExist from Lookup is "not found", not an error.
func agentExists(name string) (bool, error) {
	_, err := agentprofile.Lookup(name)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// copyAgent copies a built-in OR custom agent profile SRC to a new custom
// profile newName. The resolved profile is re-saved under the new name.
func copyAgent(src, newName string) error {
	p, err := agentprofile.Lookup(src)
	if err != nil {
		return err
	}
	p.Name = newName
	return p.Save()
}

func writeAgentShow(w io.Writer, name string) error {
	p, err := agentprofile.Lookup(name)
	if err != nil {
		return err
	}
	source := "custom"
	if agentprofile.IsBuiltin(name) {
		source = "built-in"
	}
	_, _ = fmt.Fprintf(w, "Name:        %s\n", p.Name)
	_, _ = fmt.Fprintf(w, "Source:      %s\n", source)
	_, _ = fmt.Fprintf(w, "Image:       %s\n", p.Image)
	_, _ = fmt.Fprintf(w, "Command:     %s\n", p.Command)
	if len(p.DefaultArgs) > 0 {
		_, _ = fmt.Fprintf(w, "Default args: %v\n", p.DefaultArgs)
	}
	if p.NativeRelease != "" {
		_, _ = fmt.Fprintf(w, "Native release: %s\n", p.NativeRelease)
	}
	if p.NpmPackage != "" {
		_, _ = fmt.Fprintf(w, "npm package: %s\n", p.NpmPackage)
	}
	if p.StateContainerPath != "" {
		_, _ = fmt.Fprintf(w, "State path:  %s\n", p.StateContainerPath)
		for _, mount := range p.StateMounts {
			_, _ = fmt.Fprintf(w, "State view:  %s -> %s\n", mount.Source, mount.Destination)
		}
	}
	if len(p.StateArchiveExcludes) > 0 {
		_, _ = fmt.Fprintf(w, "Archive excludes: %v\n", p.StateArchiveExcludes)
	}
	if len(p.RequiresFeatures) > 0 {
		_, _ = fmt.Fprintf(w, "Requires features: %v\n", p.RequiresFeatures)
	}
	if p.Auth != nil {
		_, _ = fmt.Fprintf(w, "Auth:        credential_file=%s", p.Auth.CredentialFile)
		if len(p.Auth.ImportFiles) > 0 {
			_, _ = fmt.Fprintf(w, " import_files=%v", p.Auth.ImportFiles)
		}
		_, _ = fmt.Fprintln(w)
	}
	return nil
}

func writeAgentShowJSON(w io.Writer, p *agentprofile.Profile, builtin bool) error {
	type agentJSON struct {
		Name                 string                    `json:"name"`
		Source               string                    `json:"source"`
		Image                string                    `json:"image"`
		Command              string                    `json:"command"`
		DefaultArgs          []string                  `json:"default_args,omitempty"`
		NpmPackage           string                    `json:"npm_package,omitempty"`
		NativeRelease        string                    `json:"native_release,omitempty"`
		StateContainerPath   string                    `json:"state_container_path,omitempty"`
		StateMounts          []agentprofile.StateMount `json:"state_mounts,omitempty"`
		StateArchiveExcludes []string                  `json:"state_archive_excludes,omitempty"`
		RequiresFeatures     []string                  `json:"requires_features,omitempty"`
		LaunchEnv            map[string]string         `json:"launch_env,omitempty"`
	}
	source := "custom"
	if builtin {
		source = "built-in"
	}
	out := agentJSON{
		Name:                 p.Name,
		Source:               source,
		Image:                p.Image,
		Command:              p.Command,
		DefaultArgs:          p.DefaultArgs,
		NpmPackage:           p.NpmPackage,
		NativeRelease:        p.NativeRelease,
		StateContainerPath:   p.StateContainerPath,
		StateMounts:          p.StateMounts,
		StateArchiveExcludes: p.StateArchiveExcludes,
		RequiresFeatures:     p.RequiresFeatures,
		LaunchEnv:            p.LaunchEnv,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
