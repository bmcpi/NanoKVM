package redfish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

// TestEmbeddedOpenAPI_YAMLParses confirms the YAML compiled into the
// binary is valid (catches typos / indentation regressions at test time
// instead of at first request).
func TestEmbeddedOpenAPI_YAMLParses(t *testing.T) {
	var doc map[string]any
	if err := yaml.Unmarshal(openAPIYAML, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	if doc["openapi"] == nil {
		t.Error("openapi.yaml missing 'openapi' top-level key")
	}
	if doc["paths"] == nil {
		t.Error("openapi.yaml missing 'paths'")
	}
}

// TestGetOpenAPIYAML_ServesEmbeddedBytes verifies the YAML handler
// returns the spec verbatim with the right content type.
func TestGetOpenAPIYAML_ServesEmbeddedBytes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	svc := NewService()
	r.GET("/openapi.yaml", svc.GetOpenAPIYAML)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/yaml") {
		t.Errorf("Content-Type = %q; want application/yaml*", w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), "openapi:") {
		t.Errorf("response body missing 'openapi:' line")
	}
}

// TestGetOpenAPIJSON_RoundTrip verifies the YAML→JSON conversion
// produces parseable JSON whose structure matches the source.
func TestGetOpenAPIJSON_RoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	svc := NewService()
	r.GET("/openapi.json", svc.GetOpenAPIJSON)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Errorf("Content-Type = %q; want application/json*", w.Header().Get("Content-Type"))
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if got["openapi"] == nil {
		t.Error("JSON output missing 'openapi' top-level key")
	}
	if paths, ok := got["paths"].(map[string]any); !ok || paths["/redfish/v1/Systems/1/Bios"] == nil {
		t.Error("JSON output missing Bios path entry")
	}
}

func TestGetSwaggerUI_ServesHTML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	svc := NewService()
	r.GET("/docs", svc.GetSwaggerUI)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type = %q; want text/html*", w.Header().Get("Content-Type"))
	}
	body := w.Body.String()
	if !strings.Contains(body, "swagger-ui") || !strings.Contains(body, "/redfish/v1/openapi.yaml") {
		t.Errorf("Swagger UI HTML missing expected references:\n%s", body)
	}
}
