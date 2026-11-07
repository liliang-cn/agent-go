package domain

import (
	"encoding/json"
	"sort"
)

// StrictJSONSchema returns a copy of a JSON schema that satisfies OpenAI's
// strict structured outputs: every object schema, at any depth (properties,
// items, anyOf/oneOf/allOf, $defs/definitions), gets additionalProperties
// false and lists all of its property keys in required. A property the
// caller left optional becomes nullable instead, so "may be absent" is still
// expressible: the model answers null, which decodes to the Go zero value.
//
// The input is never modified. A schema that cannot be round-tripped through
// JSON is returned as it came.
func StrictJSONSchema(schema interface{}) interface{} {
	if schema == nil {
		return nil
	}
	var raw []byte
	switch s := schema.(type) {
	case []byte:
		raw = s
	case json.RawMessage:
		raw = s
	case string:
		raw = []byte(s)
	default:
		b, err := json.Marshal(schema)
		if err != nil {
			return schema
		}
		raw = b
	}
	var tree interface{}
	if err := json.Unmarshal(raw, &tree); err != nil {
		return schema
	}
	return strictNode(tree)
}

// StrictJSONSchemaBytes is StrictJSONSchema for a schema held as JSON text.
func StrictJSONSchemaBytes(schema []byte) []byte {
	if len(schema) == 0 {
		return schema
	}
	out, err := json.Marshal(StrictJSONSchema(json.RawMessage(schema)))
	if err != nil {
		return schema
	}
	return out
}

func strictNode(n interface{}) interface{} {
	switch v := n.(type) {
	case []interface{}:
		for i := range v {
			v[i] = strictNode(v[i])
		}
		return v
	case map[string]interface{}:
		return strictObject(v)
	}
	return n
}

func strictObject(m map[string]interface{}) map[string]interface{} {
	// Walk the children first. "properties", "$defs" and "definitions" are
	// maps of name to schema; their keys are names, not keywords.
	for k, child := range m {
		switch k {
		case "properties", "$defs", "definitions", "patternProperties":
			if sub, ok := child.(map[string]interface{}); ok {
				for name, s := range sub {
					sub[name] = strictNode(s)
				}
			}
		case "enum", "const", "default", "examples", "required":
			// values, not schemas
		default:
			m[k] = strictNode(child)
		}
	}

	props, isObject := m["properties"].(map[string]interface{})
	if t, ok := m["type"].(string); ok && t == "object" {
		isObject = true
	}
	if !isObject {
		return m
	}
	if props == nil {
		props = map[string]interface{}{}
		m["properties"] = props
	}

	required := map[string]bool{}
	if list, ok := m["required"].([]interface{}); ok {
		for _, r := range list {
			if s, ok := r.(string); ok {
				required[s] = true
			}
		}
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	all := make([]interface{}, 0, len(keys))
	for _, k := range keys {
		if !required[k] {
			props[k] = makeNullable(props[k])
		}
		all = append(all, k)
	}
	m["required"] = all
	m["additionalProperties"] = false
	return m
}

// makeNullable lets a property answer null as well as its own type.
func makeNullable(n interface{}) interface{} {
	m, ok := n.(map[string]interface{})
	if !ok {
		return n
	}
	switch t := m["type"].(type) {
	case string:
		m["type"] = []interface{}{t, "null"}
	case []interface{}:
		for _, x := range t {
			if x == "null" {
				return m
			}
		}
		m["type"] = append(t, "null")
	default:
		for _, key := range []string{"anyOf", "oneOf"} {
			if branches, ok := m[key].([]interface{}); ok {
				for _, b := range branches {
					if bm, ok := b.(map[string]interface{}); ok && bm["type"] == "null" {
						return m
					}
				}
				m[key] = append(branches, map[string]interface{}{"type": "null"})
				return m
			}
		}
		// No type to widen (a bare $ref, say): wrap it.
		return map[string]interface{}{"anyOf": []interface{}{m, map[string]interface{}{"type": "null"}}}
	}
	if enum, ok := m["enum"].([]interface{}); ok {
		has := false
		for _, e := range enum {
			if e == nil {
				has = true
			}
		}
		if !has {
			m["enum"] = append(enum, nil)
		}
	}
	return m
}
