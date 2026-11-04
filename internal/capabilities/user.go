package capabilities

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"gopkg.in/yaml.v3"
)

// Custom capability authoring schema. A user-authored capability YAML exposes
// HIGH-LEVEL yoloctl
// grants only — it MUST NOT carry raw OpenShell policy fragments (the
// internal Network/Filesystem shape the built-in registry uses). The loader
// parses this shape and translates it into the internal Capability so the
// composer, the compiled policy, and the byte-golden contract are unchanged.
//
// Grant kinds are network.egress, workdir.write, and pty.allocate; add more
// only when the implementation needs them.

// userCapabilityDoc is the on-disk custom-capability document. KnownFields
// decoding rejects any field not listed here — in particular the OLD
// low-level fields (top-level network:/filesystem:, policy_name, endpoints),
// which is how the "no raw OpenShell fragments" rule is enforced.
type userCapabilityDoc struct {
	SchemaVersion int    `yaml:"schema_version"`
	Name          string `yaml:"name"`
	Description   string `yaml:"description"`
	Grants        Grants `yaml:"grants"`
}

// Grants is the high-level authoring grant set. Each grant kind translates to
// the internal positive-grant representation (see translate).
type Grants struct {
	Network NetworkGrants `yaml:"network"`
	Workdir WorkdirGrant  `yaml:"workdir"`
	// PTY is omitted from serialized documents when unset so `capability copy`
	// output for pre-existing capabilities stays byte-identical.
	PTY PTYGrant `yaml:"pty,omitempty"`
}

// NetworkGrants groups the network grant kinds. Only egress exists today.
type NetworkGrants struct {
	Egress []EgressGrant `yaml:"egress"`
}

// EgressGrant is one high-level egress allowance: a set of hosts reachable on
// a set of ports, optionally attributed to specific binaries. It lowers to a
// NetworkGrant with one Endpoint per host (sharing this entry's ports).
type EgressGrant struct {
	Hosts    []string `yaml:"hosts"`
	Ports    []int    `yaml:"ports"`
	Binaries []string `yaml:"binaries"`
	// AllowEncodedSlash permits HTTP URLs containing %2F on these hosts.
	// The compiler owns the required OpenShell REST configuration.
	AllowEncodedSlash bool `yaml:"allow_encoded_slash,omitempty"`
}

// WorkdirGrant is the workdir grant kind. write:true lowers to the same
// FSGrant the built-in fs.workdir.write produces.
type WorkdirGrant struct {
	Write bool `yaml:"write"`
}

// PTYGrant is the pseudo-terminal grant kind. allocate:true lowers to the same
// FSGrants the built-in fs.pty.allocate produces (PTYGrants). The device paths
// stay a private compiler detail: a custom capability names the intent, never
// /dev paths — which also keeps users clear of the /dev/ptmx symlink trap.
type PTYGrant struct {
	Allocate bool `yaml:"allocate"`
}

// empty reports whether no grant kind is populated.
func (g Grants) empty() bool {
	return len(g.Network.Egress) == 0 && !g.Workdir.Write && !g.PTY.Allocate
}

// translate lowers the high-level authoring grants into the internal
// Capability (the same Network/Filesystem shape the built-in registry uses),
// so the downstream composer and policy output are identical to a built-in.
//
//   - each grants.network.egress[i] becomes a NetworkGrant whose PolicyName is
//     derived from the capability name (policyNameBase: a leading "network."
//     is stripped, then sanitized; with multiple egress entries each gets a
//     "-<i>" suffix), with one Endpoint{Host, Ports} per host (Ports = that
//     egress entry's ports) and Binaries = that entry's binaries;
//   - grants.workdir.write:true becomes FSGrant{Path: "/workdir", Access:
//     ReadWrite} — identical to the built-in fs.workdir.write;
//   - grants.pty.allocate:true becomes PTYGrants() — identical to the built-in
//     fs.pty.allocate.
func (g Grants) translate(capName string) Capability {
	out := Capability{Name: capName}
	base := policyNameBase(capName)
	for i, eg := range g.Network.Egress {
		policyName := base
		if len(g.Network.Egress) > 1 {
			policyName = fmt.Sprintf("%s-%d", base, i)
		}
		endpoints := make([]Endpoint, 0, len(eg.Hosts))
		for _, h := range eg.Hosts {
			endpoints = append(endpoints, Endpoint{Host: h, Ports: eg.Ports, AllowEncodedSlash: eg.AllowEncodedSlash})
		}
		out.Network = append(out.Network, NetworkGrant{
			PolicyName: policyName,
			Endpoints:  endpoints,
			Binaries:   eg.Binaries,
		})
	}
	if g.Workdir.Write {
		out.Filesystem = append(out.Filesystem, FSGrant{Path: workdirGrantPath, Access: ReadWrite})
	}
	if g.PTY.Allocate {
		out.Filesystem = append(out.Filesystem, PTYGrants()...)
	}
	return out
}

// workdirGrantPath is the in-container workdir root a workdir.write grant
// lowers to — kept in sync with the built-in fs.workdir.write FSGrant and the
// composer's workdirPath. (Duplicated as a literal rather than imported to
// avoid an internal/agentbox → internal/capabilities cycle inversion.)
const workdirGrantPath = "/workdir"

// policyNameBase derives the stable openshell network_policies map key base
// from a capability name: a leading "network." prefix is stripped (so a
// custom network.mytools maps to "mytools", matching the built-in convention
// where network.github → "github"), then any character outside [a-zA-Z0-9._-]
// is replaced with "-". The capability name itself is already validated to
// that charset, so sanitization is belt-and-suspenders; the strip keeps the
// generated policy key human-readable.
func policyNameBase(capName string) string {
	base := strings.TrimPrefix(capName, "network.")
	if base == "" {
		base = capName
	}
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// LoadUser discovers user-defined capabilities under layout.CapabilitiesDir().
// Each *.yaml is one custom capability authored in the HIGH-LEVEL grants
// shape; the filename (without .yaml) must equal the capability's name. The
// document is strictly decoded
// (KnownFields) so the OLD low-level shape (top-level network:/filesystem:,
// policy_name, endpoints) is rejected with a pointer to the grants: shape —
// this is the "custom YAML must not expose raw OpenShell policy fragments"
// rule. Returns an empty map (no error) when the dir is absent.
func LoadUser() (map[string]Capability, error) {
	dir, err := layout.CapabilitiesDir()
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
	out := make(map[string]Capability, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		path := filepath.Join(dir, e.Name())
		// A custom capability may NOT shadow a built-in name:
		// same-name overrides of built-ins are not allowed — they cause stale
		// copies and upgrade ambiguity. Reject loudly rather than silently
		// override or silently ignore.
		if IsBuiltin(name) {
			return nil, fmt.Errorf("%s: %q is a built-in capability; custom items may not reuse a built-in name (copy it to a new name instead)", path, name)
		}
		if name == RetiredPrivilegedCapability {
			return nil, fmt.Errorf("%s: %w; remove this custom item", path, ErrRetiredPrivileged)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		c, err := parseUserCapability(name, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out[name] = c
	}
	return out, nil
}

// parseUserCapability decodes one custom-capability document (high-level
// grants shape), translates it into the internal Capability, and validates
// the result. Pure over (name, raw) so it is unit-testable without touching
// the filesystem.
func parseUserCapability(name string, raw []byte) (Capability, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var doc userCapabilityDoc
	if err := dec.Decode(&doc); err != nil {
		// A field-not-found error here is almost always the OLD low-level
		// shape (network:/filesystem:/policy_name/endpoints at the top
		// level). Point the user at the supported authoring shape.
		return Capability{}, fmt.Errorf(
			"%w; custom capabilities use the high-level `grants:` shape "+
				"(grants.network.egress / grants.workdir.write / grants.pty.allocate) and must NOT carry "+
				"raw OpenShell policy fields (network:/filesystem:/policy_name/endpoints)",
			err)
	}
	if doc.Name == "" {
		doc.Name = name
	}
	if doc.Name != name {
		return Capability{}, fmt.Errorf("capability name %q must equal filename %q", doc.Name, name)
	}
	if doc.Grants.empty() {
		return Capability{}, fmt.Errorf("capability %q: grants is empty; declare at least one grant (grants.network.egress, grants.workdir.write, or grants.pty.allocate)", doc.Name)
	}
	for i, eg := range doc.Grants.Network.Egress {
		if len(eg.Hosts) == 0 {
			return Capability{}, fmt.Errorf("capability %q: grants.network.egress[%d] has no hosts", doc.Name, i)
		}
		if len(eg.Ports) == 0 {
			return Capability{}, fmt.Errorf("capability %q: grants.network.egress[%d] has no ports; the rego matches endpoint.ports[_] so an egress grant with no ports can never match", doc.Name, i)
		}
	}
	c := doc.Grants.translate(doc.Name)
	c.Description = doc.Description
	if err := c.Validate(); err != nil {
		return Capability{}, err
	}
	return c, nil
}

// toGrants reverses translate: it projects an internal Capability back onto the
// high-level authoring grants shape, so a built-in (or any internal Capability)
// can be serialized into a valid custom-capability document that LoadUser can
// re-read. Within one NetworkGrant, endpoints are grouped by their ports list
// and URL parsing permissions to preserve each endpoint's grant. A workdir-write
// FSGrant lowers back to workdir.write:true and the
// PTY grant set (keyed on its /dev/pts read_write entry) lowers back to
// pty.allocate:true. Any other filesystem grant has no high-level authoring
// representation today, so it is dropped (the only built-in FS grants are
// fs.workdir.write and fs.pty.allocate).
func toGrants(c Capability) Grants {
	var g Grants
	for _, n := range c.Network {
		// Group hosts by ports and URL parsing permissions. Copy must not
		// broaden a relaxed parser setting to strict endpoints on the same port.
		type endpointKey struct {
			ports             string
			allowEncodedSlash bool
		}
		type bucket struct {
			ports []int
			hosts []string
		}
		var order []endpointKey
		buckets := map[endpointKey]*bucket{}
		for _, e := range n.Endpoints {
			key := endpointKey{ports: portsKey(e.Ports), allowEncodedSlash: e.AllowEncodedSlash}
			b, ok := buckets[key]
			if !ok {
				b = &bucket{ports: e.Ports}
				buckets[key] = b
				order = append(order, key)
			}
			b.hosts = append(b.hosts, e.Host)
		}
		for _, key := range order {
			b := buckets[key]
			g.Network.Egress = append(g.Network.Egress, EgressGrant{
				Hosts:             b.hosts,
				Ports:             b.ports,
				Binaries:          n.Binaries,
				AllowEncodedSlash: key.allowEncodedSlash,
			})
		}
	}
	for _, f := range c.Filesystem {
		if f.Path == workdirGrantPath && f.Access == ReadWrite {
			g.Workdir.Write = true
		}
		if f.Path == ptyAllocatePath && f.Access == ReadWrite {
			g.PTY.Allocate = true
		}
	}
	return g
}

// ptyAllocatePath is the PTYGrants entry that identifies the PTY grant set when
// projecting an internal Capability back onto the authoring shape (toGrants).
const ptyAllocatePath = "/dev/pts"

// portsKey renders a ports list to a stable string key for grouping endpoints
// that share the same ports set within a NetworkGrant.
func portsKey(ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ",")
}

// SaveUser writes a custom capability to layout.CapabilitiesDir() in the
// high-level authoring grants shape. The capability is projected back to the
// grants shape (toGrants), so the resulting file is exactly what a user would
// hand-author and LoadUser re-reads it identically. Refuses to clobber a
// built-in name (the disjoint-namespace invariant) and validates the result
// before writing. Used by `capability copy`.
func SaveUser(c Capability) error {
	if err := layout.ValidateName("capability", c.Name); err != nil {
		return err
	}
	if IsBuiltin(c.Name) {
		return fmt.Errorf("%q is a built-in capability; choose a different name for the custom copy", c.Name)
	}
	if c.Name == RetiredPrivilegedCapability {
		return ErrRetiredPrivileged
	}
	doc := userCapabilityDoc{
		SchemaVersion: 1,
		Name:          c.Name,
		Description:   c.Description,
		Grants:        toGrants(c),
	}
	if doc.Grants.empty() {
		return fmt.Errorf("capability %q has no representable grants to copy", c.Name)
	}
	// Round-trip through the parser to guarantee the file we write loads back
	// into a valid capability (the file is the contract, not the in-memory form).
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if _, err := parseUserCapability(c.Name, raw); err != nil {
		return fmt.Errorf("serialized custom capability %q is invalid: %w", c.Name, err)
	}
	dir, err := layout.CapabilitiesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, c.Name+".yaml"), raw, 0o644)
}

// DeleteUser removes a custom capability file. Refuses a built-in name (there is
// no file to remove and the catalog command should reject it earlier). Returns
// fs.ErrNotExist when no custom file exists for name. Used by `capability
// destroy`.
func DeleteUser(name string) error {
	if err := layout.ValidateName("capability", name); err != nil {
		return err
	}
	if IsBuiltin(name) {
		return fmt.Errorf("%q is a built-in capability and cannot be deleted", name)
	}
	dir, err := layout.CapabilitiesDir()
	if err != nil {
		return err
	}
	return os.Remove(filepath.Join(dir, name+".yaml"))
}
