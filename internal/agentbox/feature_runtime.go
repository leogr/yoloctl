package agentbox

import (
	"context"
	"path"
	"strings"

	"github.com/leogr/yoloctl/internal/agentprofile"
	"github.com/leogr/yoloctl/internal/capabilities"
	"github.com/leogr/yoloctl/internal/ocifeature"
)

// FeatureGrant is one folder a dev container Feature's containerEnv names,
// with the access the box policy gives it, or Skip when it gets none.
type FeatureGrant struct {
	Ref       string
	Source    string
	Path      string
	ReadWrite bool
	Note      string // why a would-be writable home is read-only
	Skip      string
}

// FeatureRuntime is what a box's OCI Features contribute at runtime: folders
// for the policy and declared environment for box processes.
type FeatureRuntime struct {
	Grants []FeatureGrant
	Env    []ocifeature.EnvEntry
}

func hasOCIFeature(names []string) bool {
	for _, name := range names {
		if ocifeature.IsReference(name) {
			return true
		}
	}
	return false
}

// featurePlanLenient resolves the box's locked OCI Features offline with the
// lenient feature list, so read paths keep working with a missing profile. A
// box without OCI Features never reaches the resolver.
func featurePlanLenient(c *Config) (ocifeature.Plan, bool) {
	names := effectiveFeatures(c)
	if !hasOCIFeature(names) {
		return ocifeature.Plan{}, false
	}
	plan, err := ocifeature.Resolve(context.Background(), nil, names, c.FeatureOptions, c.FeatureLocks)
	if err != nil {
		// Fail closed for access: without a resolvable plan no Feature folder
		// or variable is granted. Writes resolve strictly before persistence.
		return ocifeature.Plan{}, false
	}
	return plan, true
}

// FeatureRuntimeOf returns the folders and environment a box's OCI Features
// contribute, after the protected, mount, network-binary and profile rules.
func FeatureRuntimeOf(c *Config) (FeatureRuntime, error) {
	plan, ok := featurePlanLenient(c)
	if !ok {
		return FeatureRuntime{}, nil
	}
	eff, err := EffectiveCapabilities(c)
	if err != nil {
		return FeatureRuntime{}, err
	}
	return FeatureRuntime{Grants: featureGrants(plan, eff, c.Mounts), Env: featureEnv(plan, c)}, nil
}

func featureGrants(plan ocifeature.Plan, eff *capabilities.EffectiveSet, mounts []Mount) []FeatureGrant {
	// Every network-granted executable in the catalog, not only the box's
	// current grants: a program written over one while the box lacks the grant
	// would otherwise hold it once the capability is added.
	var binaries []string
	add := func(capab capabilities.Capability) {
		for _, g := range capab.Network {
			binaries = append(binaries, g.Binaries...)
		}
	}
	for _, name := range capabilities.Names() {
		if capab, err := capabilities.Lookup(name); err == nil {
			add(capab)
		}
	}
	for _, capab := range eff.Capabilities() {
		add(capab)
	}
	var out []FeatureGrant
	for _, cand := range plan.PathCandidates() {
		g := FeatureGrant{Ref: cand.Ref, Source: cand.Source, Path: cand.Path, ReadWrite: cand.ReadWrite}
		switch {
		case ocifeature.ProtectedPath(g.Path):
			g.Skip = "protected"
		default:
			for _, m := range mounts {
				target := path.Clean(m.Target)
				if pathWithin(g.Path, target) || pathWithin(target, g.Path) {
					g.Skip = "overlaps the mount at " + target
					break
				}
			}
		}
		if g.Skip == "" && g.ReadWrite {
			for _, b := range binaries {
				// Either direction conflicts: the binary (or its pattern) inside
				// the folder, or the folder inside the pattern's literal prefix.
				if prefix := globPrefix(b); pathWithin(prefix, g.Path) || pathWithin(g.Path, prefix) {
					g.ReadWrite = false
					g.Note = "read-only: holds " + b + ", which a network grant names"
					break
				}
			}
		}
		out = append(out, g)
	}
	return out
}

// globPrefix is the literal part of a policy binary path before any glob.
func globPrefix(p string) string {
	if i := strings.IndexAny(p, "*?["); i >= 0 {
		return path.Dir(p[:i] + "x")
	}
	return p
}

func pathWithin(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// featureEnv adds the profile rule to the Feature's declared environment: a
// name the agent profile sets (launch_env, secret injection) is skipped.
func featureEnv(plan ocifeature.Plan, c *Config) []ocifeature.EnvEntry {
	owned := map[string]bool{}
	if c.HasAgent() {
		if p, err := agentprofile.Lookup(c.Agent); err == nil {
			for key := range p.LaunchEnv {
				owned[key] = true
			}
			for _, s := range p.SecretInjection {
				owned[s.EnvName()] = true
			}
		}
	}
	env := plan.Environment()
	for i, e := range env {
		if e.Skip == "" && owned[e.Key] {
			env[i].Value, env[i].Skip = "", "set by the agent profile"
		}
	}
	return env
}

// FeatureEnvironment is the declared Feature environment delivered to box
// processes, below the box's own Env (which wins on conflicts).
func FeatureEnvironment(c *Config) map[string]string {
	plan, ok := featurePlanLenient(c)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, e := range featureEnv(plan, c) {
		if e.Skip != "" {
			continue
		}
		if _, overridden := c.Env[e.Key]; overridden {
			continue
		}
		out[e.Key] = e.Value
	}
	return out
}

// workloadLiterals are the declared literals a native child bootstrap gets:
// the dev container Feature environment first, then the box's own Env, which
// replaces it. The private account map still overrides both later.
// Runtime-control keys keep the reserved namespace filtering.
func workloadLiterals(c *Config) map[string]string {
	out := make(map[string]string, len(c.Env))
	for key, value := range FeatureEnvironment(c) {
		out[key] = value
	}
	for key, value := range c.Env {
		if !strings.HasPrefix(key, "OPENSHELL_") {
			out[key] = value.Literal
		}
	}
	return out
}

// privilegedFeatureEnvironment is the map the privileged endpoint applies
// below the account map. Its children otherwise get no box literals, so a
// box --env value for a key the Feature declares (and may deliver) rides
// here too: the box value wins over the Feature's, as on the native path.
func privilegedFeatureEnvironment(c *Config) map[string]string {
	plan, ok := featurePlanLenient(c)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, e := range featureEnv(plan, c) {
		if e.Skip != "" {
			continue
		}
		if v, overridden := c.Env[e.Key]; overridden {
			out[e.Key] = v.Literal
		} else {
			out[e.Key] = e.Value
		}
	}
	return out
}
