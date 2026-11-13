package strictyaml

import (
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// Marshal encodes v like yaml.Marshal and proves that the bytes read back as
// the same YAML before anyone writes them. gopkg.in/yaml.v3 v3.0.1 can emit a
// multi-line string whose first line is empty or indented as a block scalar
// with an indentation indicator that it then cannot parse (or parses as other
// data), so a saved declaration would be refused by every later load. When the
// plain encoding does not read back, multi-line strings are written
// double-quoted instead; an encoding that still does not read back is an error
// and nothing is written.
func Marshal(v any) ([]byte, error) {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	// The expected YAML, built without emitting the strings that may break:
	// yaml.v3's Node.Encode itself marshals and parses, so each multi-line
	// string is encoded as a single-line token in a copy of v and put back
	// into the node afterwards.
	t := tokens{nonce: rand.Text(), values: map[string]string{}}
	var want yaml.Node
	if err := want.Encode(t.copy(reflect.ValueOf(v)).Interface()); err != nil {
		return nil, err
	}
	t.restore(&want)
	if readsBackAs(raw, &want) {
		return raw, nil
	}
	quoteMultiline(&want)
	if raw, err = yaml.Marshal(&want); err != nil {
		return nil, err
	}
	if !readsBackAs(raw, &want) {
		return nil, errors.New("YAML encoding does not read back as the value it encodes")
	}
	return raw, nil
}

// tokens stands in for multi-line strings while a value is encoded.
type tokens struct {
	nonce  string
	values map[string]string // token → string
}

// copy returns a copy of v whose multi-line strings are tokens; v itself is
// not changed. Unexported fields are copied as they are (YAML ignores them).
func (t *tokens) copy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.String:
		s := v.String()
		if !strings.Contains(s, "\n") {
			return v
		}
		token := fmt.Sprintf("strictyaml-%s-%d", t.nonce, len(t.values))
		t.values[token] = s
		out := reflect.New(v.Type()).Elem()
		out.SetString(token)
		return out
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(t.copy(v.Elem()))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(t.copy(v.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for i := range v.NumField() {
			if out.Field(i).CanSet() {
				out.Field(i).Set(t.copy(v.Field(i)))
			}
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := range v.Len() {
			out.Index(i).Set(t.copy(v.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(v.Type()).Elem()
		for i := range v.Len() {
			out.Index(i).Set(t.copy(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		for it := v.MapRange(); it.Next(); {
			out.SetMapIndex(t.copy(it.Key()), t.copy(it.Value()))
		}
		return out
	}
	return v
}

// restore puts the strings back in place of their tokens.
func (t *tokens) restore(n *yaml.Node) {
	if s, ok := t.values[n.Value]; ok && n.Kind == yaml.ScalarNode {
		n.Value, n.Tag, n.Style = s, "!!str", 0
	}
	for _, c := range n.Content {
		t.restore(c)
	}
}

// readsBackAs reports whether raw parses to the same YAML as want: the same
// kinds, resolved tags, scalar values and structure; styles, positions and
// comments aside.
func readsBackAs(raw []byte, want *yaml.Node) bool {
	var got yaml.Node
	if err := yaml.Unmarshal(raw, &got); err != nil {
		return false
	}
	return sameNode(content(&got), content(want))
}

func content(n *yaml.Node) *yaml.Node {
	if n.Kind == yaml.DocumentNode && len(n.Content) == 1 {
		return n.Content[0]
	}
	return n
}

func sameNode(a, b *yaml.Node) bool {
	if a.Kind != b.Kind || a.ShortTag() != b.ShortTag() || len(a.Content) != len(b.Content) {
		return false
	}
	if a.Kind == yaml.ScalarNode && a.Value != b.Value {
		return false
	}
	for i := range a.Content {
		if !sameNode(a.Content[i], b.Content[i]) {
			return false
		}
	}
	return true
}

// quoteMultiline writes every multi-line string scalar double-quoted, a style
// with no indentation to infer.
func quoteMultiline(n *yaml.Node) {
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!str" && strings.Contains(n.Value, "\n") {
		n.Style = yaml.DoubleQuotedStyle
	}
	for _, c := range n.Content {
		quoteMultiline(c)
	}
}
