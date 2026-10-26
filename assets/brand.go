// Package assets shares the approved SwarmMind logo between both consoles.
package assets

import (
	_ "embed"
	"net/http"
)

//go:embed brand/logo-mark.svg
var logo []byte

// ServeLogo serves the same self-contained SVG for both the UI mark and favicon.
// These public static assets contain no configuration, identity or credentials.
func ServeLogo(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(logo)
}
