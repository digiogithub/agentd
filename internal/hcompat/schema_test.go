package hcompat_test

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"slices"
	"strings"
	"testing"
)

// openRPC is the vendored herdr-core released API (testdata/NOTICE).
type openRPC struct {
	Methods []struct {
		Name   string `json:"name"`
		Result struct {
			Schema map[string]any `json:"schema"`
		} `json:"result"`
	} `json:"methods"`
	Components struct {
		Schemas map[string]map[string]any `json:"schemas"`
	} `json:"components"`
}

func loadSchema(t *testing.T) *openRPC {
	t.Helper()
	b, err := os.ReadFile("testdata/herdr-core-released.openrpc.json")
	if err != nil {
		t.Fatal(err)
	}
	var d openRPC
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}

func (d *openRPC) result(method string) map[string]any {
	for _, m := range d.Methods {
		if m.Name == method {
			return m.Result.Schema
		}
	}
	return nil
}

func (d *openRPC) schema(name string) map[string]any {
	return d.Components.Schemas[name]
}

// validate checks v against the JSON Schema subset the herdr-core OpenRPC
// document uses ($ref, type, required, properties, additionalProperties,
// items, anyOf, enum, minimum/maximum, minItems/maxItems). It returns the
// problems found, with JSON paths.
func (d *openRPC) validate(s map[string]any, v any, path string) []string {
	if ref, ok := s["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		sub := d.Components.Schemas[name]
		if sub == nil {
			return []string{path + ": unresolved " + ref}
		}
		return d.validate(sub, v, path)
	}
	if alts, ok := s["anyOf"].([]any); ok {
		var all []string
		for _, alt := range alts {
			errs := d.validate(alt.(map[string]any), v, path)
			if len(errs) == 0 {
				return nil
			}
			all = append(all, errs...)
		}
		return []string{fmt.Sprintf("%s: matches no anyOf alternative (%s)", path, strings.Join(all, "; "))}
	}
	var errs []string
	if t, ok := s["type"]; ok {
		var types []string
		switch tt := t.(type) {
		case string:
			types = []string{tt}
		case []any:
			for _, x := range tt {
				types = append(types, x.(string))
			}
		}
		if !slices.ContainsFunc(types, func(ty string) bool { return isType(v, ty) }) {
			return []string{fmt.Sprintf("%s: %v is not %v", path, v, types)}
		}
	}
	if enum, ok := s["enum"].([]any); ok && !slices.ContainsFunc(enum, func(e any) bool { return fmt.Sprint(e) == fmt.Sprint(v) }) {
		errs = append(errs, fmt.Sprintf("%s: %v not in enum %v", path, v, enum))
	}
	if n, ok := v.(float64); ok {
		if min, ok := s["minimum"].(float64); ok && n < min {
			errs = append(errs, fmt.Sprintf("%s: %v < minimum %v", path, n, min))
		}
		if max, ok := s["maximum"].(float64); ok && n > max {
			errs = append(errs, fmt.Sprintf("%s: %v > maximum %v", path, n, max))
		}
	}
	switch val := v.(type) {
	case map[string]any:
		for _, r := range asStrings(s["required"]) {
			if _, ok := val[r]; !ok {
				errs = append(errs, path+": missing required "+r)
			}
		}
		props, _ := s["properties"].(map[string]any)
		for k, x := range val {
			if ps, ok := props[k].(map[string]any); ok {
				errs = append(errs, d.validate(ps, x, path+"."+k)...)
			} else if ap, ok := s["additionalProperties"].(map[string]any); ok {
				errs = append(errs, d.validate(ap, x, path+"."+k)...)
			}
		}
	case []any:
		if min, ok := s["minItems"].(float64); ok && float64(len(val)) < min {
			errs = append(errs, fmt.Sprintf("%s: fewer than %v items", path, min))
		}
		if max, ok := s["maxItems"].(float64); ok && float64(len(val)) > max {
			errs = append(errs, fmt.Sprintf("%s: more than %v items", path, max))
		}
		if is, ok := s["items"].(map[string]any); ok {
			for i, x := range val {
				errs = append(errs, d.validate(is, x, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return errs
}

func isType(v any, ty string) bool {
	switch ty {
	case "null":
		return v == nil
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		n, ok := v.(float64)
		return ok && n == math.Trunc(n)
	}
	return false
}

func asStrings(v any) []string {
	var out []string
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			out = append(out, x.(string))
		}
	}
	return out
}

// checkResult fails t when a method's result does not match the released
// herdr-core schema.
func checkResult(t *testing.T, d *openRPC, method string, res map[string]any) {
	t.Helper()
	s := d.result(method)
	if s == nil {
		t.Fatalf("schema has no method %s", method)
	}
	if errs := d.validate(s, res, method); len(errs) > 0 {
		t.Errorf("%s result does not match herdr-core %s:\n  %s", method, "released schema", strings.Join(errs, "\n  "))
	}
}

func TestValidatorCatchesDrift(t *testing.T) {
	d := loadSchema(t)
	bad := map[string]any{"address": "x", "labels": map[string]any{}, "argv": []any{}, "cwd": "/", "state": "live",
		"created_ms": float64(1), "screen": "exact", "cols": float64(80)} // rows missing
	if errs := d.validate(d.schema("Terminal"), bad, "t"); len(errs) == 0 {
		t.Fatal("validator accepted a Terminal without rows")
	}
	bad["rows"] = "24"
	if errs := d.validate(d.schema("Terminal"), bad, "t"); len(errs) == 0 {
		t.Fatal("validator accepted a string for a uint16")
	}
}

// Every method agentd advertises exists in the released herdr-core API (the
// API only grows within a generation, so these names stay valid).
func TestAdvertisedMethodsExistUpstream(t *testing.T) {
	sock := startDaemon(t)
	c := newHCClient(t, sock)
	d := loadSchema(t)
	res := c.helloFramed()
	checkResult(t, d, "hello", res)
	for _, m := range res["methods"].([]any) {
		if d.result(m.(string)) == nil {
			t.Errorf("advertised method %s is not in herdr-core's released API", m)
		}
	}
}
