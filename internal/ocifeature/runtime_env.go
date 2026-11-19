package ocifeature

import (
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/capabilities"
)

// EnvEntry is the final value one Feature-declared containerEnv key has after
// every installation, computed like Docker ENV. Skip, when set, says why the
// key does not reach box processes; Value is then empty.
type EnvEntry struct {
	Ref   string
	Key   string
	Value string
	Skip  string
}

// PathCandidate is a folder a Feature's containerEnv names: a PATH entry
// (read-only) or another variable whose value is one absolute path,
// read-write when a PATH entry lies in it (the Feature's own home).
type PathCandidate struct {
	Ref       string
	Source    string // PATH or the variable name
	Path      string
	ReadWrite bool
}

// envReference matches Docker ENV references, braced and unbraced.
var envReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// reservedEnvNames would change how yoloctl, the sandbox, the shell or the
// network path behave; Feature values for them are never delivered.
var reservedEnvNames = strings.Fields("HOME USER LOGNAME SHELL PWD TERM PATH BASH_ENV ENV GLIBC_TUNABLES " +
	"HTTP_PROXY HTTPS_PROXY NO_PROXY ALL_PROXY http_proxy https_proxy no_proxy all_proxy " +
	"SSL_CERT_FILE SSL_CERT_DIR NODE_EXTRA_CA_CERTS REQUESTS_CA_BUNDLE CURL_CA_BUNDLE")

var reservedEnvPrefixes = []string{"LD_", "XDG_", "SSH_", "OPENSHELL_", "YOLOCTL_"}

// ReservedEnv reports whether a Feature may not set name in box processes.
func ReservedEnv(name string) bool {
	if slices.Contains(reservedEnvNames, name) {
		return true
	}
	for _, prefix := range reservedEnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

type envAssignment struct {
	ref, key, raw string
}

func (p Plan) envAssignments() []envAssignment {
	var out []envAssignment
	for _, l := range p.installations() {
		m, err := l.Definition()
		if err != nil {
			continue
		}
		for _, key := range sortedKeys(m.ContainerEnv) {
			out = append(out, envAssignment{ref: l.Ref, key: key, raw: strings.ReplaceAll(m.ContainerEnv[key], "${containerEnv:", "${")})
		}
	}
	return out
}

// Environment expands every Feature containerEnv assignment in Dockerfile
// order (installation order, sorted keys per installation). A reference
// resolves only against Feature values set so far, never the host, box or
// account; a later assignment that cannot be resolved unsets the key rather
// than keeping an earlier value. PATH is never delivered (box processes
// already receive the image search path); reserved names are skipped.
func (p Plan) Environment() []EnvEntry {
	values := map[string]string{}
	final := map[string]EnvEntry{}
	total := 0
	for _, a := range p.envAssignments() {
		value, missing := expandEnv(a.raw, values)
		if missing == "" && total+len(value) > maxExpandedTotal {
			missing = tooLong
		}
		switch {
		case missing == tooLong:
			delete(values, a.key)
			final[a.key] = EnvEntry{Ref: a.ref, Key: a.key, Skip: "expanded value is too large"}
		case missing == "$":
			delete(values, a.key)
			final[a.key] = EnvEntry{Ref: a.ref, Key: a.key, Skip: "uses a $ form yoloctl does not expand"}
		case missing != "":
			delete(values, a.key)
			final[a.key] = EnvEntry{Ref: a.ref, Key: a.key, Skip: "refers to ${" + missing + "}, which no Feature sets"}
		default:
			total += len(value)
			values[a.key] = value
			final[a.key] = EnvEntry{Ref: a.ref, Key: a.key, Value: value}
		}
	}
	out := make([]EnvEntry, 0, len(final))
	delivered := 0
	for _, key := range sortedKeys(final) {
		e := final[key]
		if key == "PATH" {
			continue
		}
		if ReservedEnv(key) {
			e.Value, e.Skip = "", "reserved"
		}
		if e.Skip == "" {
			// Entry count is bounded too (the byte budget allows many empty
			// values); the privileged endpoint refuses maps over 1024 entries.
			if delivered >= maxDeliveredEnv {
				e.Value, e.Skip = "", "too many Feature variables"
			} else {
				delivered++
			}
		}
		out = append(out, e)
	}
	return out
}

// expandEnv substitutes ${NAME} and $NAME from values. It returns the first
// name it cannot resolve, or "$" for any other dollar sign (escapes and
// forms yoloctl does not interpret), so such a value is never delivered.
// The value is bounded while it is built: past maxExpandedValue bytes it
// returns tooLong instead of allocating further.
func expandEnv(raw string, values map[string]string) (string, string) {
	if strings.Contains(strings.ReplaceAll(envReference.ReplaceAllString(raw, ""), "$$", ""), "$") {
		return "", "$"
	}
	var b strings.Builder
	missing, last := "", 0
	for _, loc := range envReference.FindAllStringSubmatchIndex(raw, -1) {
		name := ""
		if loc[2] >= 0 {
			name = raw[loc[2]:loc[3]]
		} else {
			name = raw[loc[4]:loc[5]]
		}
		v, ok := values[name]
		if !ok && missing == "" {
			missing = name
		}
		if b.Len()+(loc[0]-last)+len(v) > maxExpandedValue {
			return "", tooLong
		}
		b.WriteString(raw[last:loc[0]])
		b.WriteString(v)
		last = loc[1]
	}
	if b.Len()+len(raw)-last > maxExpandedValue {
		return "", tooLong
	}
	b.WriteString(raw[last:])
	return b.String(), missing
}

// Expansion budgets, per value and for one Feature plan; tests lower them.
var (
	maxExpandedValue = 4096
	maxExpandedTotal = 64 << 10
	maxDeliveredEnv  = 1024 // the privileged endpoint's own entry limit
)

const tooLong = "\x00too long"

// PathCandidates lists the folders Feature containerEnv names, before the
// policy's protected, mount and network-binary rules. Literal entries of a
// PATH that ends in an inherited ${PATH} still count.
func (p Plan) PathCandidates() []PathCandidate {
	// Each PATH entry expands with the Feature values known at its line; an
	// entry that refers to anything else (the inherited ${PATH}) is skipped.
	var entries []PathCandidate
	values := map[string]string{}
	total := 0
	for _, a := range p.envAssignments() {
		if a.key == "PATH" {
			for _, entry := range strings.Split(a.raw, ":") {
				if expanded, missing := expandEnv(entry, values); missing == "" && cleanAbsolute(expanded) && total+len(expanded) <= maxExpandedTotal {
					total += len(expanded)
					entries = append(entries, PathCandidate{Ref: a.ref, Source: "PATH", Path: expanded})
				}
			}
			continue
		}
		if value, missing := expandEnv(a.raw, values); missing == "" && total+len(value) <= maxExpandedTotal {
			total += len(value)
			values[a.key] = value
		} else {
			delete(values, a.key)
		}
	}
	var out []PathCandidate
	seen := map[string]int{}
	add := func(c PathCandidate) {
		if i, ok := seen[c.Path]; ok {
			out[i].ReadWrite = out[i].ReadWrite || c.ReadWrite
			return
		}
		seen[c.Path] = len(out)
		out = append(out, c)
	}
	for _, e := range p.Environment() {
		if e.Skip != "" || !cleanAbsolute(e.Value) {
			continue
		}
		home := false
		for _, entry := range entries {
			home = home || under(entry.Path, e.Value)
		}
		add(PathCandidate{Ref: e.Ref, Source: e.Key, Path: e.Value, ReadWrite: home})
	}
	for _, entry := range entries {
		add(entry)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func cleanAbsolute(p string) bool {
	return p != "" && !strings.ContainsAny(p, ":${}\x00\n") && path.IsAbs(p) && path.Clean(p) == p
}

// under reports whether p equals root or lies below it.
func under(p, root string) bool {
	return p == root || root == "/" || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

// protectedTrees are never granted to a Feature: not equal, containing or
// below. Every container mount destination yoloctl adds lies in one of them.
var protectedTrees = []string{
	"/proc", "/sys", "/dev", "/etc", "/run", "/boot", "/root", "/home", "/tmp", "/var", "/app", "/workdir", "/.openshell",
	"/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32",
	"/opt/openshell", "/opt/yoloctl",
	"/usr/local/bin", "/usr/local/sbin", "/usr/local/lib/yoloctl", "/usr/local/libexec/yoloctl", "/usr/local/share/airc",
}

// protectedExact are broad roots whose Feature-owned children stay eligible.
var protectedExact = []string{
	"/", "/opt", "/usr", "/usr/local",
	"/usr/local/lib", "/usr/local/libexec", "/usr/local/share", "/usr/local/etc", "/usr/local/include", "/usr/local/src",
}

// ProtectedPath reports whether a Feature folder may never be granted. Of
// /usr only children of /usr/local qualify.
func ProtectedPath(p string) bool {
	if slices.Contains(protectedExact, p) {
		return true
	}
	for _, tree := range protectedTrees {
		if under(p, tree) || under(tree, p) {
			return true
		}
	}
	return under(p, "/usr") && !strings.HasPrefix(p, "/usr/local/")
}

// linkGuard is the final build step for granted Feature folders: a folder
// that is (or passes through) a link resolving into a protected place fails
// the image build, because Landlock follows links when the policy applies.
//
// A folder that may become writable must also not resolve into, or contain,
// a folder that holds a built-in network-granted executable: the policy
// applies that rule to the declared spelling, the build to the real target.
// Read-only folders may be links (a versioned toolchain link, for example).
func linkGuard(paths, writable []string) string {
	if len(paths) == 0 {
		return ""
	}
	var patterns []string
	for _, tree := range protectedTrees {
		patterns = append(patterns, tree, tree+"/*")
		for parent := path.Dir(tree); parent != "/"; parent = path.Dir(parent) {
			patterns = append(patterns, parent)
		}
	}
	patterns = append(patterns, protectedExact...)
	slices.Sort(patterns)
	patterns = slices.Compact(patterns)
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = shellQuote(p)
	}
	return "\n# OCI Feature folders granted to the box must not resolve into protected places\nUSER root\nRUN for p in " + strings.Join(quoted, " ") +
		"; do r=$(readlink -m -- \"$p\") || exit 1; case \"$r\" in " + strings.Join(patterns, "|") +
		") echo \"yoloctl: OCI Feature folder $p resolves to protected $r\" >&2; exit 1;; /usr/local/?*) ;; /usr/*) echo \"yoloctl: OCI Feature folder $p resolves to protected $r\" >&2; exit 1;; esac; done" +
		aliasGuard(writable) + "\n"
}

func aliasGuard(writable []string) string {
	if len(writable) == 0 {
		return ""
	}
	quoted := make([]string, len(writable))
	for i, p := range writable {
		quoted[i] = shellQuote(p)
	}
	var checks []string
	for _, prefix := range networkToolPrefixes() {
		q := shellQuote(prefix)
		checks = append(checks, "case \"$r\" in "+q+"|"+q+"/*) bad \"$p\" \"$r\" "+q+";; esac; case "+q+" in \"$r\"/*) bad \"$p\" \"$r\" "+q+";; esac")
	}
	return "; bad() { echo \"yoloctl: writable OCI Feature folder $1 resolves to $2, which holds the network-granted $3\" >&2; exit 1; }; for p in " + strings.Join(quoted, " ") +
		"; do r=$(readlink -m -- \"$p\") || exit 1; " + strings.Join(checks, "; ") + "; done"
}

// networkToolPrefixes are the literal folders (before any glob) of every
// built-in network-granted executable. Built-in only, so the composed image
// identity does not depend on a host's custom capabilities; the policy also
// applies custom ones to the declared spelling.
func networkToolPrefixes() []string {
	var out []string
	for _, b := range capabilities.BuiltinNetworkBinaries() {
		if i := strings.IndexAny(b, "*?["); i >= 0 {
			b = path.Dir(b[:i] + "x")
		}
		out = append(out, b)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// holdsNetworkTool reports whether folder p holds, lies in or contains the
// literal folder of a built-in network-granted executable.
func holdsNetworkTool(p string) bool {
	for _, prefix := range networkToolPrefixes() {
		if under(prefix, p) || under(p, prefix) {
			return true
		}
	}
	return false
}
