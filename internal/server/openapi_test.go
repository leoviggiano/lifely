package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The reason the panel uses fuego at all (house rule v2.27): the OpenAPI spec
// is GENERATED from the handlers' types. Measured before this test existed,
// /swagger/openapi.json answered 404 -- registering routes is not enough, the
// spec route is mounted by Run(), which this daemon never calls. A migration
// whose only benefit is unreachable is a dependency with no payment.
func TestOpenAPISpecIsServedAndDescribesTheRoutes(t *testing.T) {
	doc := struct {
		OpenAPI    string                     `json:"openapi"`
		Paths      map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}{}

	rec := getRaw(t, New(7777, "manual", fixture(t)), "/swagger/openapi.json")
	if rec.Code != 200 {
		t.Fatalf("the generated spec is not served: status %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"/api/pendencies", "/api/pendencies/{id...}", "/api/sources", "/api/projects"} {
		if _, ok := doc.Paths[want]; !ok {
			t.Errorf("%s is missing from the spec", want)
		}
	}
	// Typed responses are the point: a handler returning map[string]any would
	// be documented as "an object", which documents nothing.
	for _, want := range []string{"listResponse", "sourcesResponse", "projectsResponse", "pendencyJSON"} {
		if _, ok := doc.Components.Schemas[want]; !ok {
			t.Errorf("%s is not in the spec's schemas", want)
		}
	}
}

// The external swagger UI stays OFF: it loads its assets from a CDN, and the
// panel is loopback-only by design (ject D19, carried by v2.27).
func TestSwaggerUIIsNotServed(t *testing.T) {
	for _, path := range []string{"/swagger/index.html", "/swagger/"} {
		if rec := getRaw(t, New(7777, "manual", fixture(t)), path); rec.Code != 404 {
			t.Errorf("%s = %d, want 404: the external UI must stay off", path, rec.Code)
		}
	}
}

// getRaw is the sibling of get(): the spec is not a map[string]any and a 404
// has no JSON body at all, so neither can go through the decoding helper.
func getRaw(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "127.0.0.1:7777"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
