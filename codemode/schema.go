package codemode

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// validate checks args against the supported subset of JSON Schema:
// type, properties, required, enum, items, additionalProperties:false.
// Unknown keywords are ignored (the host's handler remains the final judge).
func validate(schema json.RawMessage, args map[string]any) []string {
	if len(schema) == 0 {
		return nil
	}
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return []string{"operation has an invalid input schema"}
	}
	var errs []string
	validateValue(s, args, "", &errs)
	return errs
}

// unknownArg stands for a value check cannot know statically (a variable,
// a call, ...). validateValue accepts it for any schema.
type unknownArg struct{}

func validateValue(s map[string]any, v any, path string, errs *[]string) {
	if _, ok := v.(unknownArg); ok {
		return
	}
	at := path
	if at == "" {
		at = "args"
	}
	if t, ok := s["type"]; ok {
		types := []string{}
		switch tt := t.(type) {
		case string:
			types = append(types, tt)
		case []any:
			for _, x := range tt {
				if str, ok := x.(string); ok {
					types = append(types, str)
				}
			}
		}
		match := false
		for _, ty := range types {
			if typeMatches(ty, v) {
				match = true
				break
			}
		}
		if !match && len(types) > 0 {
			*errs = append(*errs, fmt.Sprintf("%s must be %s, got %s", at, strings.Join(types, " or "), jsonType(v)))
			return
		}
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(v) && jsonType(e) == jsonType(v) {
				found = true
				break
			}
		}
		if !found {
			b, _ := json.Marshal(enum)
			*errs = append(*errs, fmt.Sprintf("%s must be one of %s", at, b))
		}
	}
	switch val := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				name, _ := r.(string)
				if _, present := val[name]; !present {
					*errs = append(*errs, fmt.Sprintf("%s is required", join(path, name)))
				}
			}
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ps, ok := props[k].(map[string]any); ok {
				validateValue(ps, val[k], join(path, k), errs)
			} else if ap, ok := s["additionalProperties"].(bool); ok && !ap {
				*errs = append(*errs, fmt.Sprintf("%s is not an allowed property", join(path, k)))
			}
		}
	case []any:
		if items, ok := s["items"].(map[string]any); ok {
			for i, e := range val {
				validateValue(items, e, fmt.Sprintf("%s[%d]", at, i), errs)
			}
		}
	}
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func typeMatches(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	case "null":
		return v == nil
	}
	return true
}

func jsonType(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		if x == math.Trunc(x) {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}
