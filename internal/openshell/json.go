package openshell

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// checkJSONKeys closes encoding/json's duplicate-field last-value-wins path
// before decoding root-owned authority records. Typed decoding still rejects
// unknown fields and enforces each record's actual schema.
func checkJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errors.New("OpenShell JSON exceeds nesting bound")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, nested := token.(json.Delim)
		if !nested {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate or invalid OpenShell JSON object field")
				}
				seen[key] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("unexpected OpenShell JSON closing delimiter")
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter == '{' && closing != json.Delim('}') || delimiter == '[' && closing != json.Delim(']') {
			return errors.New("mismatched OpenShell JSON closing delimiter")
		}
		return nil
	}
	if err := value(0); err != nil {
		return fmt.Errorf("OpenShell JSON: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing OpenShell JSON data")
	}
	return nil
}
