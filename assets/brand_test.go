package assets_test

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	hubweb "github.com/mtl802/swarmmind/internal/hub/web"
	nodeweb "github.com/mtl802/swarmmind/internal/node/web"
)

// Exercise the actual embedded handlers: a source-only logo would fail once
// shipped as a binary, and independent copies could silently drift across ends.
func TestConsoleLogoAssets(t *testing.T) {
	var canonical []byte
	for name, handler := range map[string]http.Handler{"hub": hubweb.Handler(), "node": nodeweb.Handler()} {
		t.Run(name, func(t *testing.T) {
			for _, path := range []string{"/logo.svg", "/favicon.svg"} {
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "image/svg+xml" {
					t.Fatalf("%s: status/type = %d %q", path, w.Code, w.Header().Get("Content-Type"))
				}
				if w.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("embedded branding must not retain stale assets across upgrades")
				}
				if canonical == nil {
					canonical = append([]byte(nil), w.Body.Bytes()...)
				} else if !bytes.Equal(canonical, w.Body.Bytes()) {
					t.Fatal("console/favicons differ from the shared logo")
				}
				dec := xml.NewDecoder(bytes.NewReader(w.Body.Bytes()))
				for {
					_, err := dec.Token()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatalf("invalid embedded SVG: %v", err)
					}
				}
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/missing-brand.svg", nil))
			if w.Code != http.StatusNotFound {
				t.Fatal("unknown brand asset must stay unavailable")
			}
		})
	}
}
