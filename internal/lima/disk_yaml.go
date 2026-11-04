package lima

import (
	"bytes"
	"fmt"
	"os"
	"strconv"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"gopkg.in/yaml.v3"
)

// Update only the disk owned by this space. Unrelated disks and comments
// survive; an already-correct document is left byte-for-byte untouched.
func setInstanceAccountDisk(path, id string, format bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return err
	}
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("expected Lima configuration mapping: %s", path)
	}
	doc := root.Content[0]
	disks := mappingValue(doc, "additionalDisks")
	changed := false
	if disks == nil {
		disks = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		doc.Content = append(doc.Content, yamlScalar("additionalDisks", "!!str"), disks)
		changed = true
	}
	if disks.Kind != yaml.SequenceNode {
		return fmt.Errorf("additionalDisks must be a sequence: %s", path)
	}
	name := layout.AccountDiskName(id)
	var owned *yaml.Node
	for _, disk := range disks.Content {
		if disk.Kind == yaml.ScalarNode && disk.Value == name {
			if owned != nil {
				return fmt.Errorf("duplicate account disk %s", name)
			}
			*disk = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: disk.HeadComment, LineComment: disk.LineComment, FootComment: disk.FootComment}
			owned = disk
			changed = true
		} else if disk.Kind == yaml.MappingNode {
			n := mappingValue(disk, "name")
			if n != nil && n.Value == name {
				if owned != nil {
					return fmt.Errorf("duplicate account disk %s", name)
				}
				owned = disk
			}
		}
	}
	if owned == nil {
		owned = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		disks.Content = append(disks.Content, owned)
		changed = true
	}
	for _, field := range []struct{ key, value, tag string }{{"name", name, "!!str"}, {"format", strconv.FormatBool(format), "!!bool"}, {"fsType", "ext4", "!!str"}} {
		value := mappingValue(owned, field.key)
		if value == nil {
			owned.Content = append(owned.Content, yamlScalar(field.key, "!!str"), yamlScalar(field.value, field.tag))
			changed = true
		} else if value.Kind != yaml.ScalarNode || value.Value != field.value || value.Tag != field.tag {
			value.Kind, value.Value, value.Tag, value.Content, value.Alias = yaml.ScalarNode, field.value, field.tag, nil, nil
			changed = true
		}
	}
	if !changed {
		return nil
	}
	out, err := marshalYAML(&root)
	if err != nil {
		return err
	}
	if bytes.Equal(raw, out) {
		return nil
	}
	return atomicfile.Write(path, out, 0600)
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
func yamlScalar(value, tag string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}
