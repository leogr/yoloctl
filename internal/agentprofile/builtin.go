package agentprofile

import (
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
)

// builtinFS holds the read-only agent definitions shipped in the binary.
//
//go:embed builtin/*.yaml
var builtinFS embed.FS

// BuiltinNames returns the sorted names (without the .yaml extension) of
// every agent profile yoloctl ships embedded.
func BuiltinNames() ([]string, error) {
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return nil, fmt.Errorf("read embedded built-ins: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ext))
	}
	sort.Strings(names)
	return names, nil
}

// IsBuiltin reports whether name is one of the agent profiles yoloctl ships
// embedded (claude, codex, ...). The catalog commands use this to reject
// editing/destroying a built-in and to reject a custom item that would shadow
// one. Returns false on an embed-read failure, which surfaces elsewhere.
func IsBuiltin(name string) bool {
	if err := layout.ValidateName("agent-profile", name); err != nil {
		return false
	}
	if _, err := builtinFS.ReadFile("builtin/" + name + ext); err != nil {
		return false
	}
	return true
}

// BuiltinContent returns the embedded YAML bytes for a built-in profile.
// The error wraps fs.ErrNotExist when name is not a shipped built-in (an
// embed.FS miss), so callers can errors.Is it and surface a friendly
// "not a built-in" message.
func BuiltinContent(name string) ([]byte, error) {
	if err := layout.ValidateName("agent-profile", name); err != nil {
		return nil, err
	}
	return builtinFS.ReadFile("builtin/" + name + ext)
}
