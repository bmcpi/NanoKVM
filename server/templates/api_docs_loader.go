package templates

// api_docs_loader.go parses the embedded OpenAPI 3.x spec
// (server/service/redfish/openapi.yaml) into a flat data model that the
// APIDocsPage template can render. We don't use a full OpenAPI library
// here — the spec is finite and project-owned, so manual extraction
// keeps the dep surface minimal.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// APIDocsModel is the page-ready view of the OpenAPI spec.
type APIDocsModel struct {
	Title       string
	Version     string
	Summary     string
	Description string

	// Tags are the navigation groups (sidebar entries), in the order
	// declared in the spec's top-level `tags` array.
	Tags []APIDocsTag

	// ByTag maps a tag name → its operations (in source order). Tags
	// with no operations are still listed in Tags but absent from ByTag.
	ByTag map[string][]APIDocsOperation
}

// APIDocsTag is one navigation group.
type APIDocsTag struct {
	Name        string
	Description string
	Anchor      string // HTML id used by the in-page nav link
	Count       int    // number of operations under this tag
}

// APIDocsOperation is a single Method+Path entry with everything the
// template needs to render its card.
type APIDocsOperation struct {
	Method      string // GET / POST / PATCH / etc. — upper-case
	Path        string
	Summary     string
	Description string
	Tag         string
	Anchor      string // HTML id for direct deep-linking
	Parameters  []APIDocsParam
	RequestBody *APIDocsBody // nil when the operation has no body
	Responses   []APIDocsResponse
	PublicAuth  bool // true when the operation declares `security: []`
}

// APIDocsParam is one path/query/header parameter.
type APIDocsParam struct {
	Name        string
	In          string // "path" | "query" | "header" | "cookie"
	Required    bool
	Description string
	Type        string // primitive type from the schema, when present
}

// APIDocsBody describes a request body (always one content type per
// operation — we use the first if multiple are declared).
type APIDocsBody struct {
	ContentType string
	Required    bool
	Example     string // pretty-printed JSON when an `example` was set
}

// APIDocsResponse is one (status → description) pair, optionally with a
// pretty-printed JSON example for the right-column "Response samples" panel.
type APIDocsResponse struct {
	Status      string // "200", "default", etc.
	Description string
	ContentType string // first content type with an example, when present
	Example     string // pretty-printed JSON example, empty when absent
}

// LoadAPIDocs parses an OpenAPI YAML document into APIDocsModel.
func LoadAPIDocs(yamlBytes []byte) (APIDocsModel, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(yamlBytes, &raw); err != nil {
		return APIDocsModel{}, fmt.Errorf("parse openapi: %w", err)
	}
	doc, _ := normaliseYAMLForJSON(raw).(map[string]any)

	m := APIDocsModel{ByTag: map[string][]APIDocsOperation{}}

	if info, ok := doc["info"].(map[string]any); ok {
		m.Title, _ = info["title"].(string)
		m.Version, _ = info["version"].(string)
		m.Summary, _ = info["summary"].(string)
		m.Description, _ = info["description"].(string)
	}

	if tags, ok := doc["tags"].([]any); ok {
		for _, t := range tags {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			name, _ := tm["name"].(string)
			desc, _ := tm["description"].(string)
			if name == "" {
				continue
			}
			m.Tags = append(m.Tags, APIDocsTag{
				Name:        name,
				Description: desc,
				Anchor:      "tag-" + slugify(name),
			})
		}
	}

	paths, _ := doc["paths"].(map[string]any)
	if paths == nil {
		return m, nil
	}

	// Iterate paths in alphabetical order for deterministic output.
	pathKeys := make([]string, 0, len(paths))
	for k := range paths {
		pathKeys = append(pathKeys, k)
	}
	sort.Strings(pathKeys)

	// Method order matches Swagger UI conventions (read-then-write).
	methodOrder := []string{"get", "post", "patch", "put", "delete", "head", "options"}

	for _, path := range pathKeys {
		pi, ok := paths[path].(map[string]any)
		if !ok {
			continue
		}
		for _, method := range methodOrder {
			opMap, ok := pi[method].(map[string]any)
			if !ok {
				continue
			}
			op := newAPIOp(method, path, pi, opMap)
			m.ByTag[op.Tag] = append(m.ByTag[op.Tag], op)
		}
	}

	// Ensure every tag with operations is represented in Tags, and that
	// each Tag.Count reflects the actual operation count. If a tag was
	// referenced by an operation but not declared in the spec's tags
	// array, append it at the end so it still gets a nav entry.
	declared := map[string]int{}
	for i, t := range m.Tags {
		declared[t.Name] = i
	}
	for tagName := range m.ByTag {
		if _, ok := declared[tagName]; !ok && tagName != "" {
			m.Tags = append(m.Tags, APIDocsTag{
				Name:   tagName,
				Anchor: "tag-" + slugify(tagName),
			})
		}
	}
	for i := range m.Tags {
		m.Tags[i].Count = len(m.ByTag[m.Tags[i].Name])
	}

	return m, nil
}

func newAPIOp(method, path string, pi, opMap map[string]any) APIDocsOperation {
	op := APIDocsOperation{
		Method: strings.ToUpper(method),
		Path:   path,
		Anchor: "op-" + slugify(method+"-"+path),
	}
	op.Summary, _ = opMap["summary"].(string)
	op.Description, _ = opMap["description"].(string)

	if tagList, ok := opMap["tags"].([]any); ok && len(tagList) > 0 {
		if t, ok := tagList[0].(string); ok {
			op.Tag = t
		}
	}
	if op.Tag == "" {
		op.Tag = "Other"
	}

	// `security: []` (empty array, not absent) is the OpenAPI convention
	// for "explicitly public — overrides the doc's top-level security".
	if sec, ok := opMap["security"].([]any); ok && len(sec) == 0 {
		op.PublicAuth = true
	}

	op.Parameters = extractAPIParams(pi, opMap)
	op.RequestBody = extractAPIBody(opMap)
	op.Responses = extractAPIResponses(opMap)
	return op
}

// extractAPIParams pulls parameters from both the path-level
// `parameters` (applies to every method on the path) and the per-op
// `parameters`. $ref entries are skipped (we don't have a $ref resolver
// here and our spec uses very few of them at the param level).
func extractAPIParams(pi, op map[string]any) []APIDocsParam {
	var out []APIDocsParam
	for _, src := range []any{pi["parameters"], op["parameters"]} {
		list, ok := src.([]any)
		if !ok {
			continue
		}
		for _, p := range list {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			if _, hasRef := pm["$ref"]; hasRef {
				continue
			}
			ap := APIDocsParam{}
			ap.Name, _ = pm["name"].(string)
			ap.In, _ = pm["in"].(string)
			ap.Required, _ = pm["required"].(bool)
			ap.Description, _ = pm["description"].(string)
			if sch, ok := pm["schema"].(map[string]any); ok {
				if t, ok := sch["type"].(string); ok {
					ap.Type = t
				}
			}
			if ap.Name != "" {
				out = append(out, ap)
			}
		}
	}
	return out
}

func extractAPIBody(op map[string]any) *APIDocsBody {
	body, ok := op["requestBody"].(map[string]any)
	if !ok {
		return nil
	}
	out := &APIDocsBody{}
	out.Required, _ = body["required"].(bool)
	if content, ok := body["content"].(map[string]any); ok {
		// Take the first content-type entry (alphabetical for
		// determinism). Our spec uses one content type per body.
		keys := make([]string, 0, len(content))
		for k := range content {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			out.ContentType = keys[0]
			if vm, ok := content[keys[0]].(map[string]any); ok {
				if ex, ok := vm["example"]; ok {
					if pretty, err := json.MarshalIndent(ex, "", "  "); err == nil {
						out.Example = string(pretty)
					}
				}
			}
		}
	}
	return out
}

func extractAPIResponses(op map[string]any) []APIDocsResponse {
	responses, ok := op["responses"].(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(responses))
	for k := range responses {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]APIDocsResponse, 0, len(keys))
	for _, k := range keys {
		r, ok := responses[k].(map[string]any)
		if !ok {
			continue
		}
		ar := APIDocsResponse{Status: k}
		ar.Description, _ = r["description"].(string)
		// $ref response refs — flatten to the ref target name as a
		// description hint so we don't drop them silently.
		if ar.Description == "" {
			if ref, ok := r["$ref"].(string); ok {
				ar.Description = "(" + lastPathSegment(ref) + ")"
			}
		}
		if content, ok := r["content"].(map[string]any); ok {
			cts := make([]string, 0, len(content))
			for ck := range content {
				cts = append(cts, ck)
			}
			sort.Strings(cts)
			for _, ct := range cts {
				vm, ok := content[ct].(map[string]any)
				if !ok {
					continue
				}
				if ex, ok := vm["example"]; ok {
					if pretty, err := json.MarshalIndent(ex, "", "  "); err == nil {
						ar.ContentType = ct
						ar.Example = string(pretty)
						break
					}
				}
			}
		}
		out = append(out, ar)
	}
	return out
}

// FirstResponseSample returns the first response that carries an example
// payload (preferring 2xx). Empty status means no sample is available.
func (op APIDocsOperation) FirstResponseSample() APIDocsResponse {
	for _, r := range op.Responses {
		if r.Example != "" && strings.HasPrefix(r.Status, "2") {
			return r
		}
	}
	for _, r := range op.Responses {
		if r.Example != "" {
			return r
		}
	}
	return APIDocsResponse{}
}

// normaliseYAMLForJSON converts the map[any]any subtrees gopkg.in/yaml.v3
// can produce into map[string]any so the rest of this file (and our
// json.Marshal calls) can rely on the standard shape.
func normaliseYAMLForJSON(in any) any {
	switch v := in.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, vv := range v {
			out[k] = normaliseYAMLForJSON(vv)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(v))
		for k, vv := range v {
			if ks, ok := k.(string); ok {
				out[ks] = normaliseYAMLForJSON(vv)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, vv := range v {
			out[i] = normaliseYAMLForJSON(vv)
		}
		return out
	default:
		return v
	}
}

// slugify turns "Server Overview" / "/Systems/1/Bios/Settings" into
// HTML-id-friendly strings. Lower-case, replace punctuation with hyphens,
// collapse runs, trim edges.
func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer(
		"/", "-",
		" ", "-",
		".", "-",
		"{", "",
		"}", "",
		":", "",
		"#", "",
	).Replace(s)
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return strings.Trim(s, "-")
}

func lastPathSegment(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// shortMethod returns a fixed-width abbreviation for sidebar method
// tags. Keeps long operation summaries from wrapping next to wider verbs
// like DELETE / OPTIONS.
func shortMethod(method string) string {
	switch method {
	case "DELETE":
		return "DEL"
	case "OPTIONS":
		return "OPT"
	case "PATCH":
		return "PAT"
	default:
		return method
	}
}

// hasParamsIn reports whether params contains at least one entry whose
// In field equals loc ("path", "query", "header", "cookie").
func hasParamsIn(params []APIDocsParam, loc string) bool {
	for _, p := range params {
		if p.In == loc {
			return true
		}
	}
	return false
}

// filterParams returns the subset of params whose In field equals loc.
// Preserves source order so docs match the spec's parameter ordering.
func filterParams(params []APIDocsParam, loc string) []APIDocsParam {
	out := make([]APIDocsParam, 0, len(params))
	for _, p := range params {
		if p.In == loc {
			out = append(out, p)
		}
	}
	return out
}

// MethodColorClass returns Tailwind utility classes for an HTTP method
// badge. Exposed for the template — keeping the colour map next to the
// model file rather than inside templ so adding a method is a one-liner.
func MethodColorClass(method string) string {
	switch method {
	case "GET":
		return "bg-blue-500/15 text-blue-500 border border-blue-500/30"
	case "POST":
		return "bg-green-500/15 text-green-500 border border-green-500/30"
	case "PATCH":
		return "bg-yellow-500/15 text-yellow-500 border border-yellow-500/30"
	case "PUT":
		return "bg-orange-500/15 text-orange-500 border border-orange-500/30"
	case "DELETE":
		return "bg-destructive/15 text-destructive border border-destructive/30"
	default:
		return "bg-muted text-foreground border border-border"
	}
}

// StatusColorClass returns Tailwind classes for response-status badges.
func StatusColorClass(status string) string {
	if strings.HasPrefix(status, "2") {
		return "bg-green-500/15 text-green-500"
	}
	if strings.HasPrefix(status, "3") {
		return "bg-blue-500/15 text-blue-500"
	}
	if strings.HasPrefix(status, "4") {
		return "bg-yellow-500/15 text-yellow-500"
	}
	if strings.HasPrefix(status, "5") {
		return "bg-destructive/15 text-destructive"
	}
	return "bg-muted text-foreground"
}
