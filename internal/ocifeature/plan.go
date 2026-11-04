package ocifeature

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/distribution/reference"
)

type Option struct {
	Type        string          `json:"type"`
	Default     json.RawMessage `json:"default"`
	Enum        []string        `json:"enum"`
	Proposals   []string        `json:"proposals"`
	Description string          `json:"description"`
}
type Metadata struct {
	ID            string                     `json:"id"`
	Version       string                     `json:"version"`
	Name          string                     `json:"name"`
	Options       map[string]Option          `json:"options"`
	ContainerEnv  map[string]string          `json:"containerEnv"`
	DependsOn     map[string]json.RawMessage `json:"dependsOn"`
	InstallsAfter []string                   `json:"installsAfter"`
}

// Locks retains every selected reference, including aliases of the same
// artifact. Equal manifest/option combinations install only once.
type Plan struct{ Locks []Lock }

var optionName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`)
var envName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var featureID = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

func (l Lock) Definition() (Metadata, error) {
	var m Metadata
	if len(l.Metadata) > maxMetadata {
		return m, fmt.Errorf("oversize feature metadata")
	}
	if e := json.Unmarshal([]byte(l.Metadata), &m); e != nil {
		return m, e
	}
	if !featureID.MatchString(m.ID) || m.Version == "" {
		return m, fmt.Errorf("invalid feature id/version")
	}
	seen := map[string]bool{}
	for name, o := range m.Options {
		upper := strings.ToUpper(name)
		if !optionName.MatchString(name) || seen[upper] {
			return m, fmt.Errorf("invalid or colliding option %q", name)
		}
		seen[upper] = true
		if _, e := o.value(o.Default); e != nil {
			return m, fmt.Errorf("option %s default: %w", name, e)
		}
	}
	for k, v := range m.ContainerEnv {
		if !envName.MatchString(k) || strings.ContainsAny(v, "\x00\r\n") || len(v) > 4096 {
			return m, fmt.Errorf("invalid containerEnv %s", k)
		}
		rest := regexp.MustCompile(`\$\{(?:containerEnv:)?[A-Za-z_][A-Za-z0-9_]*\}`).ReplaceAllString(v, "")
		if strings.Contains(rest, "${") {
			return m, fmt.Errorf("unsupported containerEnv substitution for %s", k)
		}
	}
	return m, nil
}
func (l Lock) Compatible() error {
	var fields map[string]json.RawMessage
	if e := json.Unmarshal([]byte(l.Metadata), &fields); e != nil {
		return e
	}
	allowed := strings.Fields("id version name description options containerEnv dependsOn installsAfter documentationURL licenseURL keywords legacyIds deprecated customizations")
	for key, raw := range fields {
		if slices.Contains(allowed, key) {
			continue
		}
		// False/empty optional runtime requirements have no effect.
		if slices.Contains([]string{"privileged", "init"}, key) && string(raw) == "false" {
			continue
		}
		if slices.Contains([]string{"capAdd", "securityOpt", "mounts"}, key) && string(raw) == "[]" {
			continue
		}
		return fmt.Errorf("unsupported Feature property %q: runtime authority and lifecycle remain controlled by yoloctl", key)
	}

	return nil
}

func (o Option) value(raw json.RawMessage) (string, error) {
	switch o.Type {
	case "boolean":
		var v bool
		if len(raw) == 0 || string(raw) == "null" {
			return "", fmt.Errorf("boolean required")
		}
		if e := json.Unmarshal(raw, &v); e != nil {
			return "", e
		}
		return strconv.FormatBool(v), nil
	case "string":
		var v string
		if len(raw) == 0 || string(raw) == "null" {
			return "", fmt.Errorf("string required")
		}
		if e := json.Unmarshal(raw, &v); e != nil {
			return "", e
		}
		if len(v) > 4096 || strings.ContainsAny(v, "\x00\r\n") {
			return "", fmt.Errorf("invalid option value")
		}
		if len(o.Enum) > 0 && !slices.Contains(o.Enum, v) {
			return "", fmt.Errorf("value must be one of %s", strings.Join(o.Enum, ", "))
		}
		return v, nil
	default:
		return "", fmt.Errorf("unsupported option type %q", o.Type)
	}
}
func (m Metadata) Values(supplied map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for key, o := range m.Options {
		raw := o.Default
		if v, ok := supplied[key]; ok {
			if o.Type == "boolean" {
				if v != "true" && v != "false" {
					return nil, fmt.Errorf("option %s must be true or false", key)
				}
				raw = []byte(v)
			} else {
				raw, _ = json.Marshal(v)
			}
		}
		v, e := o.value(raw)
		if e != nil {
			return nil, fmt.Errorf("option %s: %w", key, e)
		}
		out[key] = v
	}
	for key := range supplied {
		if _, ok := m.Options[key]; !ok {
			return nil, fmt.Errorf("unknown option %q", key)
		}
	}
	return out, nil
}
func Alias(ref string) string {
	n, e := Parse(ref)
	if e != nil {
		return ref
	}
	return path.Base(reference.Path(n))
}

// ApplyOptions accepts alias.option=value or the full OCI reference as the
// qualifier; ambiguous aliases fail rather than selecting a different Feature.
func ApplyOptions(names []string, previous map[string]map[string]string, specs []string) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for k, v := range previous {
		if slices.Contains(names, k) {
			out[k] = maps.Clone(v)
		}
	}
	for _, spec := range specs {
		key, value, ok := strings.Cut(spec, "=")
		i := strings.LastIndex(key, ".")
		if !ok || i <= 0 || !optionName.MatchString(key[i+1:]) {
			return nil, fmt.Errorf("feature option must be FEATURE.OPTION=VALUE: %q", spec)
		}
		qualifier, name := key[:i], key[i+1:]
		matches := []string{}
		if slices.Contains(names, qualifier) {
			matches = []string{qualifier}
		} else {
			for _, ref := range names {
				if Alias(ref) == qualifier && !slices.Contains(matches, ref) {
					matches = append(matches, ref)
				}
			}
		}
		if len(matches) != 1 || !IsReference(matches[0]) {
			return nil, fmt.Errorf("feature option qualifier %q is absent, ambiguous, or not an OCI Feature; use its full reference", qualifier)
		}
		if out[matches[0]] == nil {
			out[matches[0]] = map[string]string{}
		}
		out[matches[0]][name] = value
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
func dependencyOptions(raw json.RawMessage) (map[string]string, error) {
	var values map[string]json.RawMessage
	if len(raw) > 0 && raw[0] == '"' {
		var v string
		if e := json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		return map[string]string{"version": v}, nil
	}
	if e := json.Unmarshal(raw, &values); e != nil {
		return nil, e
	}
	out := map[string]string{}
	for k, raw := range values {
		var v string
		if string(raw) == "true" || string(raw) == "false" {
			v = string(raw)
		} else if e := json.Unmarshal(raw, &v); e != nil {
			return nil, e
		}
		out[k] = v
	}
	return out, nil
}
func nodeKey(l Lock) string {
	b, _ := json.Marshal(l.Options)
	return l.Digest + "\x00" + string(b)
}

// Resolve expands hard dependencies then orders all Features by hard and soft
// edges. A nil registry permits offline reads using persisted locks only.
func Resolve(ctx context.Context, r *Registry, names []string, options map[string]map[string]string, previous []Lock) (Plan, error) {
	if len(names) > 64 || len(previous) > 64 {
		return Plan{}, fmt.Errorf("too many Features")
	}
	for name := range options {
		if !slices.Contains(names, name) || !IsReference(name) {
			return Plan{}, fmt.Errorf("options for unselected OCI Feature %q", name)
		}
	}
	byRef := map[string]Lock{}
	for _, l := range previous {
		if prior, ok := byRef[l.Ref]; ok && (prior.Digest != l.Digest || prior.Layer != l.Layer || prior.Metadata != l.Metadata) {
			return Plan{}, fmt.Errorf("conflicting locks for %s", l.Ref)
		}
		byRef[l.Ref] = l
	}
	nodes := map[string]Lock{}
	aliases := map[string]map[string]Lock{}
	edges := map[string][]string{}
	order := []string{}
	visiting := map[string]bool{}
	activeRefs := map[string]bool{}
	var visit func(string, map[string]string) (string, error)
	visit = func(ref string, opts map[string]string) (string, error) {
		if e := ctx.Err(); e != nil {
			return "", e
		}
		activeRefs[ref] = true
		if len(activeRefs) > 64 {
			return "", fmt.Errorf("feature dependency graph exceeds 64 references")
		}
		if _, e := Parse(ref); e != nil {
			return "", e
		}
		l, ok := byRef[ref]
		if !ok {
			if r == nil {
				return "", fmt.Errorf("OCI Feature %s is not locked; save the declaration to resolve it", ref)
			}
			var e error
			l, e = r.Resolve(ctx, ref)
			if e != nil {
				return "", fmt.Errorf("resolve %s: %w", ref, e)
			}
			byRef[ref] = l
		}
		if e := l.validateIdentity(); e != nil {
			return "", e
		}
		m, e := l.Definition()
		if e == nil {
			e = l.Compatible()
		}
		if e != nil {
			return "", fmt.Errorf("feature %s: %w", ref, e)
		}
		l.Options, e = m.Values(opts)
		if e != nil {
			return "", fmt.Errorf("feature %s: %w", ref, e)
		}
		key := nodeKey(l)
		if aliases[key] == nil {
			aliases[key] = map[string]Lock{}
		}
		aliases[key][l.Ref] = l
		if visiting[key] {
			return "", fmt.Errorf("feature dependency cycle at %s", ref)
		}
		if _, ok := nodes[key]; ok {
			return key, nil
		}
		if len(nodes) >= 64 {
			return "", fmt.Errorf("feature dependency graph exceeds 64 nodes")
		}
		visiting[key] = true
		nodes[key] = l
		order = append(order, key)
		for _, dep := range sortedKeys(m.DependsOn) {
			values, e := dependencyOptions(m.DependsOn[dep])
			if e != nil {
				return "", e
			}
			child, e := visit(dep, values)
			if e != nil {
				return "", e
			}
			edges[key] = append(edges[key], child)
		}
		visiting[key] = false
		return key, nil
	}
	for _, ref := range names {
		if IsReference(ref) {
			if _, e := visit(ref, options[ref]); e != nil {
				return Plan{}, e
			}
		}
	}
	for key, l := range nodes {
		m, _ := l.Definition()
		for _, after := range m.InstallsAfter {
			for other, refs := range aliases {
				for _, candidate := range refs {
					n, _ := Parse(candidate.Ref)
					if key != other && (after == reference.TrimNamed(n).Name() || after == Alias(candidate.Ref)) {
						edges[key] = append(edges[key], other)
						break
					}
				}
			}
		}
	}
	done := map[string]bool{}
	active := map[string]bool{}
	result := Plan{}
	var sortNode func(string) error
	sortNode = func(key string) error {
		if done[key] {
			return nil
		}
		if active[key] {
			return fmt.Errorf("feature installation order cycle at %s", nodes[key].Ref)
		}
		active[key] = true
		sort.Strings(edges[key])
		for _, dep := range edges[key] {
			if e := sortNode(dep); e != nil {
				return e
			}
		}
		active[key] = false
		done[key] = true
		result.Locks = append(result.Locks, nodes[key])
		for _, ref := range sortedKeys(aliases[key]) {
			if ref != nodes[key].Ref {
				result.Locks = append(result.Locks, aliases[key][ref])
			}
		}
		if len(result.Locks) > 64 {
			return fmt.Errorf("feature dependency graph exceeds 64 locks")
		}
		return nil
	}
	for _, key := range order {
		if e := sortNode(key); e != nil {
			return Plan{}, e
		}
	}
	return result, nil
}
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func (p Plan) installations() []Lock {
	seen := map[string]bool{}
	var result []Lock
	for _, l := range p.Locks {
		key := nodeKey(l)
		if !seen[key] {
			seen[key] = true
			result = append(result, l)
		}
	}
	return result
}

func (p Plan) Dockerfile() (string, error) {
	var out strings.Builder
	for i, l := range p.installations() {
		m, e := l.Definition()
		if e == nil {
			e = l.Compatible()
		}
		if e != nil {
			return "", e
		}
		values, e := m.Values(l.Options)
		if e != nil {
			return "", e
		}
		fmt.Fprintf(&out, "\n# OCI Feature: %s@%s layer %s\nUSER root\n", l.Ref, l.Digest, l.Layer.Digest)
		for _, key := range sortedKeys(m.ContainerEnv) {
			value := m.ContainerEnv[key]
			value = strings.ReplaceAll(value, "${containerEnv:", "${")
			fmt.Fprintf(&out, "ENV %s=%s\n", key, strconv.Quote(value))
		}
		fmt.Fprintf(&out, "RUN --mount=type=bind,source=oci/%d,target=/tmp/yoloctl-feature,ro \\\n    cp -R /tmp/yoloctl-feature /tmp/yoloctl-feature-install && cd /tmp/yoloctl-feature-install && env _REMOTE_USER=sandbox _REMOTE_USER_HOME=/home/sandbox _CONTAINER_USER=root _CONTAINER_USER_HOME=/root", i)
		for _, key := range sortedKeys(values) {
			fmt.Fprintf(&out, " %s=%s", strings.ToUpper(key), shellQuote(values[key]))
		}
		out.WriteString(" ./install.sh && cd / && rm -rf /tmp/yoloctl-feature-install\n")
	}
	return out.String(), nil
}

// BuildFiles supplies validated archive contents with deterministic, private
// build-context paths. Installer scripts are never run by this process.
func (p Plan) BuildFiles(ctx context.Context) (map[string][]byte, error) {
	files := map[string][]byte{}
	r := NewRegistry()
	total := 0
	for i, l := range p.installations() {
		entries, e := r.Files(ctx, l)
		if e != nil {
			return nil, e
		}
		for name, data := range entries {
			total += len(data)
			if total > 64<<20 {
				return nil, fmt.Errorf("feature build context exceeds 64 MiB")
			}
			files[fmt.Sprintf("oci/%d/%s", i, name)] = data
		}
	}
	return files, nil
}
