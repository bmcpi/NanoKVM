// Package assets provides embedded static assets for the NanoKVM BMC web UI.
package assets

import "embed"

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
