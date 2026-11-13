package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	"github.com/pb33f/libopenapi-validator/config"
	"github.com/pb33f/libopenapi-validator/schema_validation"
	"github.com/pb33f/libopenapi/datamodel/high/base"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
)

// Contract-test plumbing: the OpenAPI 3.1 document is loaded once with
// libopenapi (the validator of the pinned dependency libopenapi-validator).

var specPath = filepath.Join("..", "..", "docs", "api", "openapi.yaml")

var (
	specOnce  sync.Once
	specBytes []byte
	specDoc   libopenapi.Document
	specModel *v3.Document
	specErr   error
)

func loadSpec(t testing.TB) (libopenapi.Document, *v3.Document) {
	t.Helper()
	specOnce.Do(func() {
		specBytes, specErr = os.ReadFile(specPath)
		if specErr != nil {
			return
		}
		specDoc, specErr = libopenapi.NewDocument(specBytes)
		if specErr != nil {
			return
		}
		m, err := specDoc.BuildV3Model()
		if m == nil || err != nil {
			specErr = err
			if err == nil {
				specErr = errors.New("no v3 model")
			}
			return
		}
		specModel = &m.Model
	})
	if specErr != nil {
		t.Fatalf("openapi.yaml: %v", specErr)
	}
	return specDoc, specModel
}

// newSpecValidator returns a validator for requests and responses. Security
// is checked by the gateway tests (scope denial), not here.
func newSpecValidator(t testing.TB) validator.Validator {
	t.Helper()
	doc, _ := loadSpec(t)
	v, errs := validator.NewValidator(doc, config.WithoutSecurityValidation())
	if len(errs) > 0 {
		t.Fatalf("validator: %v", errs)
	}
	return v
}

// specSchema returns the resolved schema components/schemas/<name>.
func specSchema(t testing.TB, name string) *base.Schema {
	t.Helper()
	_, m := loadSpec(t)
	p, ok := m.Components.Schemas.Get(name)
	if !ok {
		t.Fatalf("schema %s not in openapi.yaml", name)
	}
	s := p.Schema()
	if s == nil {
		t.Fatalf("schema %s does not resolve", name)
	}
	return s
}

// validateAgainst checks a JSON payload against a components/schemas entry.
func validateAgainst(t testing.TB, schemaName, payload string) []string {
	t.Helper()
	ok, errs := schema_validation.NewSchemaValidator().ValidateSchemaStringWithVersion(specSchema(t, schemaName), payload, 3.1)
	if ok {
		return nil
	}
	var out []string
	for _, e := range errs {
		msg := e.Message + ": " + e.Reason
		for _, se := range e.SchemaValidationErrors {
			msg += " [" + se.FieldPath + ": " + se.Reason + "]"
		}
		out = append(out, msg)
	}
	return out
}

// sseSchemas returns the x-sse-events mapping (event name -> schema name) of
// an operation's text/event-stream response.
func sseSchemas(t testing.TB, path string) map[string]string {
	t.Helper()
	_, m := loadSpec(t)
	pi, ok := m.Paths.PathItems.Get(path)
	if !ok || pi.Get == nil {
		t.Fatalf("no GET %s in the spec", path)
	}
	resp, ok := pi.Get.Responses.Codes.Get("200")
	if !ok {
		t.Fatalf("GET %s has no 200", path)
	}
	mt, ok := resp.Content.Get("text/event-stream")
	if !ok {
		t.Fatalf("GET %s 200 has no text/event-stream", path)
	}
	out := map[string]string{}
	node, ok := mt.Extensions.Get("x-sse-events")
	if !ok {
		t.Fatalf("GET %s: no x-sse-events", path)
	}
	var raw map[string]map[string]string
	if err := node.Decode(&raw); err != nil {
		t.Fatalf("x-sse-events: %v", err)
	}
	for ev, ref := range raw {
		out[ev] = strings.TrimPrefix(ref["$ref"], "#/components/schemas/")
	}
	return out
}

// contractEnv wraps a restEnv, validating every exchange against the spec and
// recording which (operationId, status) pairs were exercised.
type contractEnv struct {
	*restEnv
	v       validator.Validator
	mu      sync.Mutex
	covered map[string]bool // "operationId status"
}

// call performs the request through the real handler, validates request and
// response against the spec (skipRequest: the request is deliberately
// invalid) and checks the status. It returns the recorder.
func (c *contractEnv) call(t *testing.T, op string, want int, method, path, token, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	return c.callOpt(t, op, want, false, method, path, token, body, hdr...)
}

func (c *contractEnv) callBad(t *testing.T, op string, want int, method, path, token, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	return c.callOpt(t, op, want, true, method, path, token, body, hdr...)
}

func (c *contractEnv) callOpt(t *testing.T, op string, want int, skipRequest bool, method, path, token, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	rec := c.send(t, method, path, token, body, hdr...)
	if rec.Code != want {
		t.Fatalf("%s %s %s = %d, want %d: %s", op, method, path, rec.Code, want, rec.Body)
	}
	mk := func() *http.Request {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		return r
	}
	if !skipRequest {
		if ok, errs := c.v.ValidateHttpRequest(mk()); !ok {
			t.Errorf("%s %s %s: request violates the spec: %s", op, method, path, fmtValidation(errs))
		}
	}
	res := rec.Result()
	if ok, errs := c.v.ValidateHttpResponse(mk(), res); !ok {
		t.Errorf("%s %s %s -> %d: response violates the spec: %s\nbody: %s", op, method, path, want, fmtValidation(errs), rec.Body)
	}
	checkCommonHeaders(t, op, res)
	c.mu.Lock()
	c.covered[op+" "+strconv.Itoa(want)] = true
	c.mu.Unlock()
	return rec
}

// checkCommonHeaders asserts the headers the spec promises on every response.
func checkCommonHeaders(t *testing.T, op string, res *http.Response) {
	t.Helper()
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("%s: X-Content-Type-Options = %q", op, got)
	}
	if got := res.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("%s: Referrer-Policy = %q", op, got)
	}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") && res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("%s: JSON response with Cache-Control %q", op, res.Header.Get("Cache-Control"))
	}
}

func fmtValidation[T interface{ Error() string }](errs []T) string {
	var parts []string
	for _, e := range errs {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}

var remoteSeq atomic.Int32

// send is restEnv.req with a fresh remote address for unauthenticated calls,
// so the per-address failed-authentication limit (429) does not kick in.
func (c *contractEnv) send(t *testing.T, method, path, token, body string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2." + strconv.Itoa(int(remoteSeq.Add(1)%250)+1) + ":4000"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, r)
	return rec
}
