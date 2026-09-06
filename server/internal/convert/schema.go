package convert

import (
	"encoding/json"
	"strings"
)

// CleanSchema converts the JSON Schema subset used by clients into Gemini's
// Schema representation. It never modifies the client's schema.
func CleanSchema(schema any) map[string]any {
	root := AsMap(cloneValue(schema))
	if root == nil {
		return map[string]any{"type": "OBJECT", "properties": map[string]any{}}
	}
	nodes := 0
	var clean func(map[string]any, map[string]bool, int) map[string]any
	clean = func(in map[string]any, visiting map[string]bool, depth int) map[string]any {
		nodes++
		if depth > 24 || nodes > 4096 {
			return map[string]any{"type": "STRING", "description": "Recursive value encoded as JSON"}
		}
		m := AsMap(cloneValue(in))
		if ref := AsString(m["$ref"]); ref != "" {
			delete(m, "$ref")
			if strings.HasPrefix(ref, "#/") && !visiting[ref] {
				var target any = root
				for _, key := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
					key = strings.ReplaceAll(strings.ReplaceAll(key, "~1", "/"), "~0", "~")
					target = AsMap(target)[key]
				}
				if obj := AsMap(target); obj != nil {
					seen := map[string]bool{}
					for k, v := range visiting {
						seen[k] = v
					}
					seen[ref] = true
					resolved := clean(obj, seen, depth+1)
					for k, v := range resolved {
						if _, ok := m[k]; !ok {
							m[k] = v
						}
					}
				} else {
					m["type"] = "STRING"
				}
			} else {
				m["type"] = "STRING"
			}
		}
		for _, key := range []string{"anyOf", "oneOf"} {
			if options := AsSlice(m[key]); len(options) > 0 {
				delete(m, key)
				var actual []any
				nullable := false
				for _, v := range options {
					o := AsMap(v)
					if AsString(o["type"]) == "null" {
						nullable = true
					} else if o != nil {
						actual = append(actual, clean(o, visiting, depth+1))
					}
				}
				if nullable {
					m["nullable"] = true
				}
				if len(actual) == 1 {
					for k, v := range AsMap(actual[0]) {
						if _, ok := m[k]; !ok {
							m[k] = v
						}
					}
				} else if len(actual) > 1 {
					best := AsMap(actual[0])
					score := -1
					var types []string
					for _, v := range actual {
						branch := AsMap(v)
						n := len(AsMap(branch["properties"])) * 10
						if branch["items"] != nil {
							n += 5
						}
						if n > score {
							score = n
							best = branch
						}
						types = append(types, AsString(branch["type"]))
					}
					for k, v := range best {
						if m[k] == nil {
							m[k] = v
						}
					}
					m["description"] = strings.TrimSpace(AsString(m["description"]) + " [Accepts: " + strings.Join(types, " | ") + "]")
				}
			}
		}
		if types := AsSlice(m["type"]); len(types) > 0 {
			delete(m, "type")
			for _, v := range types {
				s := AsString(v)
				if s == "null" {
					m["nullable"] = true
				} else if m["type"] == nil {
					m["type"] = s
				}
			}
		}
		if all := AsSlice(m["allOf"]); len(all) > 0 {
			delete(m, "allOf")
			for _, v := range all {
				if sub := AsMap(v); sub != nil {
					for k, v := range clean(sub, visiting, depth+1) {
						if k == "properties" {
							props := AsMap(m[k])
							if props == nil {
								props = map[string]any{}
							}
							for pk, pv := range AsMap(v) {
								props[pk] = pv
							}
							m[k] = props
						} else if k == "required" {
							m[k] = append(AsSlice(m[k]), AsSlice(v)...)
						} else if m[k] == nil {
							m[k] = v
						}
					}
				}
			}
		}
		if v, ok := m["const"]; ok {
			m["enum"] = []any{v}
			delete(m, "const")
		}
		for _, key := range []string{"minimum", "maximum", "minItems", "maxItems", "minLength", "maxLength", "pattern", "format", "multipleOf", "exclusiveMinimum", "exclusiveMaximum"} {
			if v, ok := m[key]; ok {
				raw, _ := json.Marshal(v)
				m["description"] = strings.TrimSpace(AsString(m["description"]) + " [Constraint: " + key + "=" + string(raw) + "]")
			}
		}
		if vals := AsSlice(m["enum"]); len(vals) > 0 {
			allStrings := true
			for _, v := range vals {
				if _, ok := v.(string); !ok {
					allStrings = false
				}
			}
			if !allStrings {
				raw, _ := json.Marshal(vals)
				m["description"] = strings.TrimSpace(AsString(m["description"]) + " [Allowed values: " + string(raw) + "]")
				delete(m, "enum")
			}
		}
		out := map[string]any{}
		allowed := map[string]bool{"type": true, "description": true, "properties": true, "required": true, "items": true, "enum": true, "nullable": true, "propertyOrdering": true}
		for k, v := range m {
			if !allowed[k] {
				continue
			}
			switch k {
			case "type":
				v = strings.ToUpper(AsString(v))
			case "properties":
				p := map[string]any{}
				for name, sv := range AsMap(v) {
					if obj := AsMap(sv); obj != nil {
						p[name] = clean(obj, visiting, depth+1)
					}
				}
				v = p
			case "items":
				if obj := AsMap(v); obj != nil {
					v = clean(obj, visiting, depth+1)
				} else {
					continue
				}
			}
			out[k] = v
		}
		if out["type"] == nil && out["properties"] != nil {
			out["type"] = "OBJECT"
		}
		if out["type"] == nil && out["items"] != nil {
			out["type"] = "ARRAY"
		}
		if out["type"] == "ARRAY" && out["items"] == nil {
			out["items"] = map[string]any{"type": "STRING"}
		}
		if out["type"] == "OBJECT" {
			delete(out, "items")
			if out["properties"] == nil {
				out["properties"] = map[string]any{}
			}
		}
		if required := AsSlice(out["required"]); len(required) > 0 {
			var valid []any
			props := AsMap(out["properties"])
			seen := map[string]bool{}
			for _, v := range required {
				k := AsString(v)
				if props[k] != nil && !seen[k] {
					valid = append(valid, k)
					seen[k] = true
				}
			}
			if len(valid) == 0 {
				delete(out, "required")
			} else {
				out["required"] = valid
			}
		}
		return out
	}
	return clean(root, map[string]bool{}, 0)
}
