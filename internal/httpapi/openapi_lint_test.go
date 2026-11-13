package httpapi

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestOpenAPILint is the spec lint run by CI (plain go test, no extra tools):
// the document parses as OpenAPI 3.1, every $ref resolves, every operation has
// a unique operationId and a valid x-agentd-scope (pairing carries
// x-agentd-pairing instead), path parameters are declared and documented
// error responses use the Error envelope.
func TestOpenAPILint(t *testing.T) {
	doc, m := loadSpec(t)
	if v := doc.GetSpecInfo().Version; !strings.HasPrefix(v, "3.1") {
		t.Errorf("OpenAPI version %q, want 3.1.x", v)
	}

	// Every $ref points at an existing components entry.
	refRE := regexp.MustCompile(`\$ref:\s*["']?([^"'\s}]+)`)
	have := map[string]bool{}
	add := func(kind string, keys []string) {
		for _, k := range keys {
			have["#/components/"+kind+"/"+k] = true
		}
	}
	var ks []string
	for p := m.Components.Schemas.First(); p != nil; p = p.Next() {
		ks = append(ks, p.Key())
	}
	add("schemas", ks)
	ks = nil
	for p := m.Components.Responses.First(); p != nil; p = p.Next() {
		ks = append(ks, p.Key())
	}
	add("responses", ks)
	ks = nil
	for p := m.Components.Parameters.First(); p != nil; p = p.Next() {
		ks = append(ks, p.Key())
	}
	add("parameters", ks)
	ks = nil
	for p := m.Components.Headers.First(); p != nil; p = p.Next() {
		ks = append(ks, p.Key())
	}
	add("headers", ks)
	used := map[string]bool{}
	for _, mm := range refRE.FindAllSubmatch(specBytes, -1) {
		ref := string(mm[1])
		used[ref] = true
		if !have[ref] {
			t.Errorf("$ref %s does not resolve", ref)
		}
	}
	for ref := range have {
		if !used[ref] && !strings.HasPrefix(ref, "#/components/schemas/") {
			t.Errorf("components entry %s is never referenced", ref)
		}
	}

	ids := map[string]string{}
	scopes := map[string]bool{"none": true, "read": true, "write": true, "admin": true}
	pathParamRE := regexp.MustCompile(`\{(\w+)\}`)
	for pi := m.Paths.PathItems.First(); pi != nil; pi = pi.Next() {
		path, item := pi.Key(), pi.Value()
		declared := map[string]bool{}
		for _, p := range item.Parameters {
			if p.In == "path" {
				declared[p.Name] = true
			}
		}
		for oi := item.GetOperations().First(); oi != nil; oi = oi.Next() {
			where := strings.ToUpper(oi.Key()) + " " + path
			op := oi.Value()
			if op.OperationId == "" {
				t.Errorf("%s: no operationId", where)
			} else if prev, dup := ids[op.OperationId]; dup {
				t.Errorf("%s: operationId %s also used by %s", where, op.OperationId, prev)
			}
			ids[op.OperationId] = where

			scope, hasScope := op.Extensions.Get("x-agentd-scope")
			pairing, hasPairing := op.Extensions.Get("x-agentd-pairing")
			switch {
			case hasPairing && pairing.Value == "true":
				if hasScope {
					t.Errorf("%s: x-agentd-pairing together with x-agentd-scope", where)
				}
			case !hasScope:
				t.Errorf("%s: no x-agentd-scope", where)
			case !scopes[scope.Value]:
				t.Errorf("%s: x-agentd-scope %q", where, scope.Value)
			}
			if hasScope && scope.Value == "none" && (op.Security == nil || len(op.Security) != 0) {
				t.Errorf("%s: scope none needs `security: []`", where)
			}
			if hasScope && scope.Value != "none" && op.Security != nil && len(op.Security) == 0 {
				t.Errorf("%s: scope %s but security: []", where, scope.Value)
			}

			for _, p := range op.Parameters {
				if p.In == "path" {
					declared[p.Name] = true
				}
			}
			for _, mm := range pathParamRE.FindAllStringSubmatch(path, -1) {
				if !declared[mm[1]] {
					t.Errorf("%s: path parameter {%s} not declared", where, mm[1])
				}
			}

			if op.Responses == nil || op.Responses.Codes.Len() == 0 {
				t.Errorf("%s: no responses", where)
				continue
			}
			var success bool
			for ri := op.Responses.Codes.First(); ri != nil; ri = ri.Next() {
				code, resp := ri.Key(), ri.Value()
				n, err := strconv.Atoi(code)
				if err != nil {
					t.Errorf("%s: response code %q is not numeric", where, code)
					continue
				}
				if n < 300 {
					success = true
				}
				if n < 400 || resp.Content == nil {
					continue
				}
				js, ok := resp.Content.Get("application/json")
				if !ok {
					t.Errorf("%s: %s error response has no application/json content", where, code)
					continue
				}
				if !js.Schema.IsReference() || js.Schema.GetReference() != "#/components/schemas/Error" {
					t.Errorf("%s: %s error response uses %q, want the Error envelope", where, code, js.Schema.GetReference())
				}
			}
			if !success {
				t.Errorf("%s: no 2xx response", where)
			}
		}
	}

	// Event schemas of the SSE streams exist (resolved by sseSchemas).
	for _, p := range []string{"/v1/events", "/v1/sessions/{name}/view/stream"} {
		for ev, name := range sseSchemas(t, p) {
			if name == "" || !have["#/components/schemas/"+name] {
				t.Errorf("%s: x-sse-events %q -> unknown schema %q", p, ev, name)
			}
		}
	}
}
