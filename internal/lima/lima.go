package lima

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"gopkg.in/yaml.v3"
)

const DefaultBinary = "limactl"

// Client wraps the limactl binary.
type Client struct {
	binary string
	stdout io.Writer
	stderr io.Writer
}

// New returns a Client that shells out to limactl on PATH and writes
// command output to os.Stdout / os.Stderr.
func New() *Client {
	return &Client{
		binary: DefaultBinary,
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

// Create defines a Lima instance from the given template bytes. The instance
// is configured but not started — call Start to boot it.
func (c *Client) Create(ctx context.Context, name string, template []byte) error {
	if err := ValidateInstancePaths(name); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, c.binary, "create", "--name="+InstanceName(name), "--tty=false", "-")
	cmd.Stdin = bytes.NewReader(template)
	cmd.Stdout = c.stdout
	return RunWithFilteredStderr(cmd, c.stderr)
}

// Start boots a previously-created instance and blocks until it is Running.
func (c *Client) Start(ctx context.Context, name string) error {
	if err := ValidateInstancePaths(name); err != nil {
		return err
	}
	return c.run(ctx, "start", "--tty=false", InstanceName(name))
}

// Ensure makes the named instance exist and be Running. If missing, creates
// from template and starts. If stopped, starts. If already running, no-op.
func (c *Client) Ensure(ctx context.Context, name string, template []byte) error {
	instances, err := c.List(ctx)
	if err != nil {
		return err
	}
	for _, inst := range instances {
		if inst.Name != name {
			continue
		}
		if inst.Status == "Running" {
			return nil
		}
		return c.Start(ctx, name)
	}
	if err := c.Create(ctx, name, template); err != nil {
		return err
	}
	return c.Start(ctx, name)
}

// Stop gracefully stops a running instance.
func (c *Client) Stop(ctx context.Context, name string) error {
	return c.run(ctx, "stop", "--tty=false", InstanceName(name))
}

// Delete removes an instance. force=true also stops it if running.
func (c *Client) Delete(ctx context.Context, name string, force bool) error {
	args := []string{"delete", "--tty=false"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, InstanceName(name))
	return c.run(ctx, args...)
}

// CopyTo copies a local file into the named space's VM at remotePath via
// `limactl copy`. The copy runs as the VM user: a privileged destination
// needs staging through /tmp plus `sudo install` on the VM side.
func (c *Client) CopyTo(ctx context.Context, space, localPath, remotePath string) error {
	return c.run(ctx, "copy", localPath, InstanceName(space)+":"+remotePath)
}

func (c *Client) run(ctx context.Context, args ...string) error {
	if spaceenv.Native() {
		return errors.New("VM controller cannot execute host Lima operations")
	}
	cmd := exec.CommandContext(ctx, c.binary, args...)
	cmd.Stdout = c.stdout
	return RunWithFilteredStderr(cmd, c.stderr)
}

// InstanceConfigPath returns the path of the active lima.yaml for the
// named instance. Lima persists each instance's config there and re-reads
// it on the next Start. Returns fs.ErrNotExist if the instance directory
// is missing.
func InstanceConfigPath(name string) (string, error) {
	dir, err := InstanceDir(name)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("lima instance dir %s is not a directory", dir)
	}
	return filepath.Join(dir, "lima.yaml"), nil
}

// SetInstanceMounts rewrites only the `mounts:` section of the named
// instance's active lima.yaml, preserving the rest. The original config
// is Lima's already-resolved form (with `images:` populated, `base:`
// stripped) — we must not overwrite it wholesale, or Lima rejects the
// instance on its next Start with "base must be empty for YAML
// validation".
//
// Lima reads the updated config at next Start; callers must Stop + Start
// (or Restart) for changes to take effect on a running instance.
//
// Returns fs.ErrNotExist if the instance has not been created yet.
func (c *Client) SetInstanceMounts(name string, mounts []Mount) error {
	path, err := InstanceConfigPath(name)
	if errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	updated, err := setMountsInYAML(raw, mounts)
	if err != nil {
		return fmt.Errorf("rewrite mounts in %s: %w", path, err)
	}
	if bytes.Equal(raw, updated) {
		return nil
	}
	// Owner-only: only this user's limactl reads the instance config, and
	// every mounts rewrite has produced a 0600 file.
	return atomicfile.Write(path, updated, 0o600)
}

// setMountsInYAML returns a copy of raw with its top-level `mounts:` key
// replaced by the supplied mounts. If the input has no `mounts:` key, one
// is appended at the end of the top-level mapping. Comments and other
// keys are preserved (yaml.v3 Node round-trip).
func setMountsInYAML(raw []byte, mounts []Mount) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}
	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected top-level mapping, got kind %d", doc.Kind)
	}
	mountsNode, err := buildMountsNode(mounts)
	if err != nil {
		return nil, err
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == "mounts" {
			var current, wanted any
			if err := doc.Content[i+1].Decode(&current); err != nil {
				return nil, err
			}
			if err := mountsNode.Decode(&wanted); err != nil {
				return nil, err
			}
			if reflect.DeepEqual(current, wanted) {
				return raw, nil
			}
			doc.Content[i+1] = mountsNode
			return marshalYAML(&root)
		}
	}
	// No mounts key — append one.
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "mounts"}
	doc.Content = append(doc.Content, key, mountsNode)
	return marshalYAML(&root)
}

// buildMountsNode produces a yaml.Node sequence equivalent to the YAML
// rendered by MarshalMounts. An empty input becomes an empty sequence
// (`mounts: []`).
func buildMountsNode(mounts []Mount) (*yaml.Node, error) {
	rendered := "[]"
	if len(mounts) > 0 {
		rendered = MarshalMounts(mounts)
	}
	var parsed yaml.Node
	if err := yaml.Unmarshal([]byte(rendered), &parsed); err != nil {
		return nil, fmt.Errorf("re-parse mounts: %w", err)
	}
	if parsed.Kind != yaml.DocumentNode || len(parsed.Content) == 0 {
		return nil, fmt.Errorf("expected document with one sequence, got kind %d", parsed.Kind)
	}
	return parsed.Content[0], nil
}

func marshalYAML(n *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
