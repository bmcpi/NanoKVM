// Package assets provides embedded static assets for the NanoKVM BMC web UI.
package assets

import "embed"

// CSS generation runs in three ordered steps:
//
//  1. Resolve the templui module path from the Go build cache and write a
//     transient @source directive into css/sources.generated.css so Tailwind
//     can scan templui's templ files for class names (no `go mod vendor`).
//  2. Run Tailwind to compile input.css → output.css.
//  3. Delete the transient sources.generated.css.
//
//go:generate sh -c "go list -m -f '@source \"{{.Dir}}/**/*.templ\";' github.com/templui/templui > css/sources.generated.css"
//go:generate go tool tailwindcss --cwd ../../ -i server/assets/css/input.css -o server/assets/css/output.css --minify
//go:generate rm -f css/sources.generated.css

// CSS contains embedded CSS files (Tailwind output, xterm).
//
//go:embed css/output.css css/xterm.min.css
var CSS embed.FS

// JS contains embedded JavaScript files (CryptoJS, xterm + addons).
//
//go:embed js/*
var JS embed.FS

// Img contains embedded image files (favicon, logos).
//
//go:embed img/*
var Img embed.FS
