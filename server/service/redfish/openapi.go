package redfish

// openapi.go serves the OpenAPI 3.1 specification for the Redfish surface
// this BMC implements, plus a small Swagger UI page that consumes it.
//
// The spec is authored in YAML (openapi.yaml, embedded below) — that's
// the canonical form a human edits. JSON is produced on demand from the
// same source so clients that prefer JSON aren't left out.
//
// Endpoints (wired in server/router/redfish.go):
//   GET /redfish/v1/openapi.yaml — the spec, served as application/yaml
//   GET /redfish/v1/openapi.json — same spec, converted to JSON
//   GET /redfish/v1/docs         — Swagger UI loading openapi.yaml
//
// All three are public (no auth) so a tool can discover the surface
// before authenticating.

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

//go:embed openapi.yaml
var openAPIYAML []byte

// jsonOnce / cachedJSON memoise the YAML→JSON conversion so we only do
// it once per process. The spec is static for the lifetime of a binary.
var (
	jsonOnce   sync.Once
	cachedJSON []byte
	cachedErr  error
)

// GetOpenAPIYAML serves the OpenAPI spec verbatim.
func (s *Service) GetOpenAPIYAML(c *gin.Context) {
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", openAPIYAML)
}

// GetOpenAPIJSON serves the OpenAPI spec as JSON. Parses YAML once
// (sync.Once) and serves the cached bytes on every subsequent call.
func (s *Service) GetOpenAPIJSON(c *gin.Context) {
	jsonOnce.Do(func() {
		var any map[string]any
		if err := yaml.Unmarshal(openAPIYAML, &any); err != nil {
			cachedErr = err
			return
		}
		// json.Marshal can't represent map[any]any (which gopkg.in/yaml.v3
		// produces for nested maps). We unmarshalled into map[string]any
		// at the top level, but nested maps may still be map[any]any
		// depending on the YAML doc — normalise before marshalling.
		normalised := normaliseYAMLMaps(any)
		out, err := json.Marshal(normalised)
		if err != nil {
			cachedErr = err
			return
		}
		cachedJSON = out
	})
	if cachedErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": cachedErr.Error()})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", cachedJSON)
}

// GetSwaggerUI serves a self-contained HTML page that loads Swagger UI
// from a CDN and points it at /redfish/v1/openapi.yaml.
//
// Uses a pinned Swagger UI version so the rendered docs don't drift if
// upstream pushes a breaking change.
func (s *Service) GetSwaggerUI(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(swaggerUIHTML))
}

// normaliseYAMLMaps walks a decoded YAML value and converts every
// map[any]any to map[string]any so encoding/json can handle it.
func normaliseYAMLMaps(in any) any {
	switch v := in.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, vv := range v {
			out[k] = normaliseYAMLMaps(vv)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(v))
		for k, vv := range v {
			if ks, ok := k.(string); ok {
				out[ks] = normaliseYAMLMaps(vv)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, vv := range v {
			out[i] = normaliseYAMLMaps(vv)
		}
		return out
	default:
		return v
	}
}

// swaggerUIHTML is the minimal Swagger UI host page. Loads CSS + bundle
// from the jsdelivr CDN at a pinned version, then renders the spec from
// the same-origin YAML endpoint.
const swaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <title>NanoKVM Redfish API</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui.css" />
  <style>body { margin: 0; }</style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({
        url: "/redfish/v1/openapi.yaml",
        dom_id: "#swagger-ui",
        deepLinking: true,
        presets: [SwaggerUIBundle.presets.apis],
        layout: "BaseLayout",
      });
    };
  </script>
</body>
</html>
`
