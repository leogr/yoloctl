package lima

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"runtime"
	"strings"

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
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.binary, "list", "--format", "json")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	WriteFilteredStderr(c.stderr, stderr.String())
	if err != nil {
		return nil, err
	}
	all, err := parseInstances(&stdout)
	if err != nil {
		return nil, err
	}
	out := make([]Instance, 0, len(all))
	for _, inst := range all {
		space, ok := spaceFromInstance(inst.Name)
		if !ok {
			continue
		}
		inst.Name = space
		out = append(out, inst)
	}
	return out, nil
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
