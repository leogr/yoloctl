package lima

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/leogr/yoloctl/internal/safepath"
	"gopkg.in/yaml.v3"
)

// InstanceNestedVirtualization reads the instance file, not Lima's override file
// or a guest observation. nil means the instance does not configure the key.
func InstanceNestedVirtualization(name string) (*bool, error) {
	dir, err := InstanceDir(name)
	if err != nil {
		return nil, err
	}
	raw, err := safepath.ReadFile(dir, "lima.yaml", 1<<20)
	if err != nil {
		return nil, err
	}
	return nestedVirtualizationInYAML(raw)
}

func nestedVirtualizationInYAML(raw []byte) (*bool, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	var root, extra yaml.Node
	if err := dec.Decode(&root); err != nil {
		return nil, err
	}
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("expected exactly one YAML document")
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("expected top-level mapping")
	}
	var result *bool
	nodes := root.Content[0].Content
	for i := 0; i+1 < len(nodes); i += 2 {
		if nodes[i].Value != "nestedVirtualization" {
			continue
		}
		if result != nil {
			return nil, errors.New("duplicate nestedVirtualization key")
		}
		n := nodes[i+1]
		if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" {
			return nil, fmt.Errorf("nestedVirtualization is not a boolean")
		}
		var value bool
		if err := n.Decode(&value); err != nil {
			return nil, err
		}
		result = &value
	}
	return result, nil
}
