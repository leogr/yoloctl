package lima

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceconfig"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"github.com/leogr/yoloctl/internal/vmapi"
)

// Instance is a parsed Lima instance entry from `limactl list --format json`.
type Instance = vmapi.Instance

// List returns the Lima instances yoloctl manages, named by their space
// (the yolo- instance prefix is stripped back off). Foreign Lima
// instances that happen to share the LIMA_HOME are filtered out, so
// callers — and `yoloctl space ls` — only ever see yoloctl's own VMs.
func (c *Client) List(ctx context.Context) ([]Instance, error) {
	if native := spaceenv.Current(); native != nil {
		return []Instance{{Name: native.Space, Status: "Running", Arch: runtime.GOARCH}}, nil
	}
	homes, err := spaceconfig.LimaHomes()
	if err != nil {
		return nil, err
	}
	out := make([]Instance, 0)
	seen, visited := map[string]bool{}, map[string]bool{}
	for _, home := range homes {
		canonical, err := filepath.EvalSymlinks(home)
		if errors.Is(err, os.ErrNotExist) {
			canonical = home
		} else if err != nil {
			return nil, err
		}
		if visited[canonical] {
			continue
		}
		populated, err := homeHasInstances(home)
		if err != nil {
			return nil, fmt.Errorf("inspect Lima home %q: %w", home, err)
		}
		if !populated {
			continue
		}
		visited[canonical] = true
		var stdout bytes.Buffer
		cmd := c.commandAtHome(ctx, home, "list", "--format", "json")
		cmd.Stdout = &stdout
		err = runWithDiagnostics(cmd)
		if err != nil {
			return nil, err
		}
		all, err := parseInstances(&stdout)
		if err != nil {
			return nil, err
		}
		for _, inst := range all {
			space, ok := spaceFromInstance(inst.Name)
			if !ok {
				continue
			}
			if seen[space] {
				return nil, fmt.Errorf("space %q has conflicting Lima instances in multiple homes", space)
			}
			// Do not report a VM found in a different home as the selected VM.
			b, err := spaceconfig.LoadBootstrap(space)
			if err == nil {
				expected, err := layout.ResolveLimaHome(b.VM.LimaHome)
				if err != nil {
					return nil, err
				}
				if resolved, err := filepath.EvalSymlinks(expected); err == nil {
					expected = resolved
				} else if !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
				if expected != canonical {
					return nil, fmt.Errorf("space %q: Lima instance is outside its recorded home", space)
				}
			} else if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, spaceconfig.ErrLegacySchema) {
				return nil, err
			} else {
				legacy, err := layout.LimaHome()
				if err != nil {
					return nil, err
				}
				if resolved, err := filepath.EvalSymlinks(legacy); err == nil {
					legacy = resolved
				} else if !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
				if legacy != canonical {
					return nil, fmt.Errorf("space %q has an orphan Lima instance in %q; restore its bootstrap before managing it", space, home)
				}
			}
			seen[space] = true
			inst.Name = space
			out = append(out, inst)
		}
	}
	return out, nil
}

// Match stock Lima's instance enumeration: dot/underscore infrastructure is
// not an instance. Only proved absence/emptiness skips a home; errors propagate.
func homeHasInstances(home string) (bool, error) {
	entries, err := os.ReadDir(home)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") || strings.HasPrefix(entry.Name(), "_") {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

func parseInstances(r io.Reader) ([]Instance, error) {
	dec := json.NewDecoder(r)
	var out []Instance
	for {
		var inst Instance
		if err := dec.Decode(&inst); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
		if strings.TrimSpace(inst.Name) == "" {
			continue
		}
		out = append(out, inst)
	}
}
