// Package web embeds the static assets compiled into generated pages.
package web

import "embed"

// Static holds the CSS and JavaScript assets inlined into rendered pages.
//
//go:embed static
var Static embed.FS
