package router

// apidocs.go bridges the OpenAPI spec served by the redfish package and
// the templates.APIDocsPage renderer. The parsed model is cached for
// the process lifetime — the spec is embedded so it's static for the
// lifetime of the binary.

import (
	"sync"

	"github.com/BMCPi/NanoKVM/server/service/redfish"
	"github.com/BMCPi/NanoKVM/server/templates"
)

var (
	apiDocsOnce  sync.Once
	apiDocsModel templates.APIDocsModel
	apiDocsError error
)

// loadAPIDocsModel parses redfish.OpenAPIYAML() into a renderable model.
// Parses lazily on first call, then caches the result. Safe for
// concurrent use.
func loadAPIDocsModel() (templates.APIDocsModel, error) {
	apiDocsOnce.Do(func() {
		apiDocsModel, apiDocsError = templates.LoadAPIDocs(redfish.OpenAPIYAML())
	})
	return apiDocsModel, apiDocsError
}
