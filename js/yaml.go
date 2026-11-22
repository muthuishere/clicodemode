package js

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlSeparator reports whether line is a "---" document separator and
// returns any content that follows it on the same line ("--- foo").
func yamlSeparator(line string) (rest string, ok bool) {
	l := strings.TrimRight(line, " \t\r")
	if l == "---" || l == "..." {
		return "", true
	}
	if strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "---\t") {
		return strings.TrimSpace(l[3:]), true
	}
	return "", false
}

// yamlDecode parses one buffered document. ok is false for an empty document
// (only blank lines, comments or directives).
func yamlDecode(lines []string) (Value, bool, error) {
	text := strings.Join(lines, "\n")
	if strings.TrimSpace(text) == "" {
		return nil, false, nil
	}
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(text), &n); err != nil {
		return nil, false, &Throw{Value: makeError("SyntaxError", fmt.Sprintf("yaml: %v in document starting %q", err, clip(strings.TrimSpace(text))))}
	}
	if n.Kind == 0 || (n.Kind == yaml.DocumentNode && len(n.Content) == 0) {
		return nil, false, nil
	}
	v, err := yamlValue(&n, 0)
	if err != nil {
		return nil, false, &Throw{Value: makeError("SyntaxError", "yaml: "+err.Error())}
	}
	return v, true, nil
}

// yamlValue converts a node to a JS value, keeping mapping key order.
func yamlValue(n *yaml.Node, depth int) (Value, error) {
	if depth > 1000 {
		return nil, fmt.Errorf("document nested too deeply (or alias cycle)")
	}
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil, nil
		}
		return yamlValue(n.Content[0], depth+1)
	case yaml.AliasNode:
		return yamlValue(n.Alias, depth+1)
	case yaml.SequenceNode:
		arr := &Array{Elems: make([]Value, 0, len(n.Content))}
		for _, c := range n.Content {
			v, err := yamlValue(c, depth+1)
			if err != nil {
				return nil, err
			}
			arr.Elems = append(arr.Elems, v)
		}
		return arr, nil
	case yaml.MappingNode:
		o := NewObject()
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, val := n.Content[i], n.Content[i+1]
			if k.Kind == yaml.ScalarNode && k.Tag == "!!merge" {
				if err := yamlMerge(o, val, depth); err != nil {
					return nil, err
				}
				continue
			}
			kv, err := yamlValue(k, depth+1)
			if err != nil {
				return nil, err
			}
			vv, err := yamlValue(val, depth+1)
			if err != nil {
				return nil, err
			}
			o.Set(toString(kv), vv)
		}
		return o, nil
	case yaml.ScalarNode:
		return yamlScalar(n)
	}
	return nil, fmt.Errorf("unsupported node kind %d", n.Kind)
}

// yamlMerge applies a "<<" merge key: existing keys win.
func yamlMerge(o *Object, val *yaml.Node, depth int) error {
	srcs := []*yaml.Node{val}
	if val.Kind == yaml.SequenceNode {
		srcs = val.Content
	}
	for _, s := range srcs {
		v, err := yamlValue(s, depth+1)
		if err != nil {
			return err
		}
		m, ok := v.(*Object)
		if !ok {
			return fmt.Errorf("merge key needs a mapping")
		}
		for _, k := range m.keys {
			if _, has := o.Get(k); !has {
				mv, _ := m.Get(k)
				o.Set(k, mv)
			}
		}
	}
	return nil
}

func yamlScalar(n *yaml.Node) (Value, error) {
	switch n.ShortTag() {
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return nil, err
		}
		return b, nil
	case "!!int":
		var i int64
		if err := n.Decode(&i); err == nil {
			return float64(i), nil
		}
		var u uint64
		if err := n.Decode(&u); err == nil {
			return float64(u), nil
		}
		f, err := strconv.ParseFloat(n.Value, 64)
		if err != nil {
			return n.Value, nil
		}
		return f, nil
	case "!!float":
		var f float64
		if err := n.Decode(&f); err != nil {
			return nil, err
		}
		return f, nil
	}
	return n.Value, nil // strings, timestamps, binary and custom tags stay text
}
