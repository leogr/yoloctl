// Package devcontainer is a minimal devcontainer.json reader, focused
// on the fields yoloctl needs at agentbox lifecycle hooks. Today that's
// only forwardPorts for create-time auto-forwarding. Add fields here when
// the lifecycle hooks need more.
//
// devcontainer.json is JSONC (allows // line comments, /* block */
// comments, and trailing commas). The decoder here strips those before
// handing the bytes to encoding/json, so plain-JSON files work too.
package devcontainer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// File names checked by FindForProject, in order. The first existing
// path wins.
var conventionalPaths = []string{
	".devcontainer/devcontainer.json",
	".devcontainer.json",
}

// Config carries only the fields the yoloctl lifecycle reads. Everything
// else in the source devcontainer.json is silently dropped.
type Config struct {
	// ForwardPorts is the parsed forwardPorts field — every entry is
	// normalized to (published, target) port numbers (a bare integer N
	// in the source becomes (N, N)). The field names mirror the box
	// manifest's Docker-Compose-style Port shape (published = host port,
	// target = container port) so the mapping into agentbox.Port is direct.
	ForwardPorts []PortMapping
}

// PortMapping is a single (published, target) port pair from forwardPorts,
// in the Docker-Compose-style vocabulary the box manifest uses: Published is
// the host port, Target is the container port.
type PortMapping struct {
	Published int
	Target    int
}

// FindForWorkdir looks for a devcontainer.json under workdir at the
// standard locations and parses it. Returns (nil, nil) when no file is
// present — the lifecycle hooks treat that as "no auto-forward".
func FindForWorkdir(workdir string) (*Config, error) {
	for _, rel := range conventionalPaths {
		path := filepath.Join(workdir, rel)
		if _, err := os.Stat(path); err == nil {
			return Parse(path)
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	}
	return nil, nil
}

// Parse reads a devcontainer.json from path. Returns an error if the
// file is unreadable, structurally broken, or has a forwardPorts entry
// that isn't an int / "HOST:CONTAINER" / "PORT" string.
func Parse(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	stripped := stripJSONCComments(raw)
	stripped = stripTrailingCommas(stripped)
	type rawConfig struct {
		ForwardPorts []json.RawMessage `json:"forwardPorts"`
	}
	var rc rawConfig
	if err := json.Unmarshal(stripped, &rc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg := &Config{}
	for i, entry := range rc.ForwardPorts {
		mapping, err := parsePortEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("%s: forwardPorts[%d]: %w", path, i, err)
		}
		cfg.ForwardPorts = append(cfg.ForwardPorts, mapping)
	}
	return cfg, nil
}

// parsePortEntry handles the two shapes the devcontainer spec allows:
// a JSON number (mapped PORT:PORT) or a string "HOST:CONTAINER" /
// "PORT". Anything else is a parse error.
func parsePortEntry(raw json.RawMessage) (PortMapping, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return PortMapping{}, errors.New("empty entry")
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return PortMapping{}, err
		}
		parts := strings.SplitN(s, ":", 2)
		published, err := strconv.Atoi(parts[0])
		if err != nil {
			return PortMapping{}, fmt.Errorf("invalid host port %q", parts[0])
		}
		target := published
		if len(parts) == 2 {
			target, err = strconv.Atoi(parts[1])
			if err != nil {
				return PortMapping{}, fmt.Errorf("invalid container port %q", parts[1])
			}
		}
		return PortMapping{Published: published, Target: target}, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return PortMapping{}, fmt.Errorf("expected number or string, got %s", trimmed)
	}
	return PortMapping{Published: n, Target: n}, nil
}

// stripJSONCComments removes JSONC line and block comments. Comments
// inside strings are left intact. Single-pass scan, no regex.
func stripJSONCComments(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString := false
	escaped := false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == '/' && i+1 < len(src) {
			next := src[i+1]
			if next == '/' {
				// Line comment — skip to end of line.
				for i < len(src) && src[i] != '\n' {
					i++
				}
				if i < len(src) {
					out = append(out, '\n')
				}
				continue
			}
			if next == '*' {
				// Block comment — skip to closing */.
				i += 2
				for i+1 < len(src) && (src[i] != '*' || src[i+1] != '/') {
					i++
				}
				i++ // land on '/'
				continue
			}
		}
		out = append(out, ch)
	}
	return out
}

// stripTrailingCommas removes commas that immediately precede a ']'
// or '}' (ignoring whitespace). encoding/json rejects them; JSONC
// allows them.
func stripTrailingCommas(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString := false
	escaped := false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == ',' {
			// Look ahead past whitespace for the next non-space byte.
			j := i + 1
			for j < len(src) && (src[j] == ' ' || src[j] == '\t' || src[j] == '\n' || src[j] == '\r') {
				j++
			}
			if j < len(src) && (src[j] == ']' || src[j] == '}') {
				// Drop the comma; keep the whitespace + closing brace.
				continue
			}
		}
		out = append(out, ch)
	}
	return out
}
