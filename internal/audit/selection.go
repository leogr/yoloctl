package audit

import (
	"bytes"
	"errors"
	"io"

	"gopkg.in/yaml.v3"
)

// SelectionDocument confines the editor fragment to Falco's rules key while
// preserving selector bytes for Falco itself to interpret. It imposes no file,
// line, selector-count or validation-time capacity.
func SelectionDocument(raw []byte) ([]byte, error) {
	if bytes.HasPrefix(raw, []byte{0xef, 0xbb, 0xbf}) {
		return nil, errors.New("audit selection must not start with a BOM")
	}
	first := true
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		text := bytes.TrimLeft(line, " ")
		if len(text) > 0 && text[0] == '\t' {
			return nil, errors.New("audit selection must use spaces for indentation")
		}
		if len(text) == 0 || text[0] == '#' {
			continue
		}
		if first {
			first = false
			if !bytes.HasPrefix(line, []byte("rules:")) ||
				(len(line) > 6 && line[6] != ' ' && line[6] != '\t') {
				return nil, errors.New("audit selection must begin with rules: at column zero")
			}
			continue
		}
		if line[0] == ' ' {
			continue
		}
		if line[0] == '-' && (len(line) == 1 || line[1] == ' ' || line[1] == '\t') {
			continue
		}
		return nil, errors.New("audit selection cannot contain another root key or document")
	}
	if first {
		return []byte("rules: []\n"), nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var doc yaml.Node
	if err := decoder.Decode(&doc); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("audit selection must be one YAML document")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("audit selection must be an explicit rules mapping")
	}
	root := doc.Content[0]
	if len(root.Content) != 2 || root.Content[0].Kind != yaml.ScalarNode ||
		root.Content[0].Tag != "!!str" || root.Content[0].Value != "rules" ||
		root.Content[1].Kind != yaml.SequenceNode {
		return nil, errors.New("audit selection must contain only rules with an explicit sequence")
	}
	return bytes.Clone(raw), nil
}
