package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"blacksmith/internal/config"
)

// TestUIHandlerServesWASMAsApplicationWasm checks that the terminal's
// WebAssembly core is served as application/wasm. Browsers only compile a
// module while it downloads when the response carries that type.
func TestUIHandlerServesWASMAsApplicationWasm(t *testing.T) {
	t.Run("any wasm file in the web root", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "vendor", "wterm", "9.9.9")

		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(dir, "wterm.wasm"), []byte("\x00asm\x01\x00\x00\x00"), 0o600); err != nil {
			t.Fatal(err)
		}

		assertWASMResponse(t, getUIHandler(&config.Config{WebRoot: root}), "/vendor/wterm/9.9.9/wterm.wasm")
	})

	t.Run("the vendored terminal core", func(t *testing.T) {
		assertWASMResponse(t, getUIHandler(&config.Config{WebRoot: "ui"}), "/vendor/wterm/0.5.10/wterm.wasm")
	})
}

func assertWASMResponse(t *testing.T, handler http.Handler, path string) {
	t.Helper()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("%s: status %d, want %d", path, rec.Code, http.StatusOK)
	}

	if got := rec.Header().Get("Content-Type"); got != "application/wasm" {
		t.Errorf("%s: Content-Type %q, want %q", path, got, "application/wasm")
	}
}

// TestUIHandlerServesWebManifestAsManifestJSON checks the web app manifest
// is served as application/manifest+json. Go has no built-in type for the
// .webmanifest extension, so without one the file can go out as plain text
// and a browser may refuse to read it.
func TestUIHandlerServesWebManifestAsManifestJSON(t *testing.T) {
	t.Parallel()

	handler := getUIHandler(&config.Config{WebRoot: "ui"})

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, "/manifest.webmanifest", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, want %d", method, rec.Code, http.StatusOK)
		}

		if got := rec.Header().Get("Content-Type"); got != "application/manifest+json" {
			t.Errorf("%s: Content-Type %q, want %q", method, got, "application/manifest+json")
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/missing.webmanifest", nil))

	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") == "application/manifest+json" {
		t.Errorf("a missing manifest: status %d, Content-Type %q, want a plain 404", rec.Code, rec.Header().Get("Content-Type"))
	}
}
