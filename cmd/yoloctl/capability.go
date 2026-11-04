package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/spf13/cobra"
)

// newCapabilityCmd is the `capability` catalog command tree:
// ls / show / copy / edit / destroy over the built-in + custom capability
// registry. A capability is a named positive grant (network egress,
// fs.workdir.write, and/or fs.pty.allocate) the OpenShell policy composer
// unions in.
func newCapabilityCmd() *cobra.Command {
	return newCatalogCmd(capabilityKind())
}

func capabilityKind() catalogKind {
	return catalogKind{
		name:         "capability",
		short:        "Manage capabilities (positive-grant policy presets: built-in + custom)",
		builtinNames: func() ([]string, error) { return capabilities.BuiltinNames(), nil },
		customNames:  customCapabilityNames,
		isBuiltin:    capabilities.IsBuiltin,
		exists:       capabilityExists,
		validateName: func(name string) error { return layout.ValidateName("capability", name) },
		writeShow:    writeCapabilityShow,
		writeShowJSON: func(w io.Writer, name string) error {
			c, err := capabilities.Lookup(name)
			if err != nil {
				return err
			}
			return writeCapabilityShowJSON(w, c, capabilities.IsBuiltin(name))
		},
		copy:              copyCapability,
		customPath:        capabilityCustomPath,
		validateAfterEdit: func(name string) error { _, err := capabilities.Lookup(name); return err },
		deleteCustom:      capabilities.DeleteUser,
		referrers:         capabilityReferrers,
	}
}

// customCapabilityNames returns the names of user-defined capabilities only
// (LoadUser keys), sorted.
func customCapabilityNames() ([]string, error) {
	user, err := capabilities.LoadUser()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(user))
	for n := range user {
		out = append(out, n)
	}
	return out, nil
}

// capabilityExists reports whether name resolves to a built-in or custom
// capability.
func capabilityExists(name string) (bool, error) {
	if capabilities.IsBuiltin(name) {
		return true, nil
	}
	user, err := capabilities.LoadUser()
	if err != nil {
		return false, err
	}
	_, ok := user[name]
	return ok, nil
}

// capabilityCustomPath returns the on-disk path of a custom capability YAML.
func capabilityCustomPath(name string) (string, error) {
	dir, err := layout.CapabilitiesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".yaml"), nil
}

// copyCapability copies a built-in OR custom capability SRC to a new custom
// capability newName. SaveUser projects the resolved capability back to the
// high-level `grants` authoring shape (toGrants), so a built-in's network/fs
// grants become a valid hand-authorable YAML the loader re-reads identically.
func copyCapability(src, newName string) error {
	c, err := capabilities.Lookup(src)
	if err != nil {
		return err
	}
	c.Name = newName // the policy_name base derives from this on next load
	return capabilities.SaveUser(c)
}

func writeCapabilityShow(w io.Writer, name string) error {
	c, err := capabilities.Lookup(name)
	if err != nil {
		return err
	}
	source := "custom"
	if capabilities.IsBuiltin(name) {
		source = "built-in"
	}
	_, _ = fmt.Fprintf(w, "Name:        %s\n", c.Name)
	_, _ = fmt.Fprintf(w, "Source:      %s\n", source)
	if c.Description != "" {
		_, _ = fmt.Fprintf(w, "Description: %s\n", c.Description)
	}
	if len(c.Network) == 0 && len(c.Filesystem) == 0 {
		_, _ = fmt.Fprintln(w, "Grants:      (none)")
		return nil
	}
	if len(c.Network) > 0 {
		_, _ = fmt.Fprintln(w, "Network egress:")
		for _, g := range c.Network {
			for _, e := range g.Endpoints {
				_, _ = fmt.Fprintf(w, "  %s ports=%v", e.Host, e.Ports)
				if e.AllowEncodedSlash {
					_, _ = fmt.Fprint(w, " allow_encoded_slash=true")
				}
				_, _ = fmt.Fprintln(w)
			}
			if len(g.Binaries) > 0 {
				_, _ = fmt.Fprintf(w, "    binaries: %v\n", g.Binaries)
			}
		}
	}
	if len(c.Filesystem) > 0 {
		_, _ = fmt.Fprintln(w, "Filesystem:")
		for _, f := range c.Filesystem {
			_, _ = fmt.Fprintf(w, "  %s (%s)\n", f.Path, f.Access)
		}
	}
	return nil
}

func writeCapabilityShowJSON(w io.Writer, c capabilities.Capability, builtin bool) error {
	type endpointJSON struct {
		Host              string `json:"host"`
		Ports             []int  `json:"ports"`
		AllowEncodedSlash bool   `json:"allow_encoded_slash,omitempty"`
	}
	type networkJSON struct {
		PolicyName string         `json:"policy_name"`
		Endpoints  []endpointJSON `json:"endpoints"`
		Binaries   []string       `json:"binaries,omitempty"`
	}
	type fsJSON struct {
		Path   string `json:"path"`
		Access string `json:"access"`
	}
	type capJSON struct {
		Name        string        `json:"name"`
		Source      string        `json:"source"`
		Description string        `json:"description,omitempty"`
		Network     []networkJSON `json:"network,omitempty"`
		Filesystem  []fsJSON      `json:"filesystem,omitempty"`
	}
	source := "custom"
	if builtin {
		source = "built-in"
	}
	out := capJSON{Name: c.Name, Source: source, Description: c.Description}
	for _, g := range c.Network {
		nj := networkJSON{PolicyName: g.PolicyName, Binaries: g.Binaries}
		for _, e := range g.Endpoints {
			nj.Endpoints = append(nj.Endpoints, endpointJSON{Host: e.Host, Ports: e.Ports, AllowEncodedSlash: e.AllowEncodedSlash})
		}
		out.Network = append(out.Network, nj)
	}
	for _, f := range c.Filesystem {
		out.Filesystem = append(out.Filesystem, fsJSON{Path: f.Path, Access: string(f.Access)})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
