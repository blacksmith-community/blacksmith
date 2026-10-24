package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/config"
)

// TestUIHandlerHTMLIsNotCached checks that the HTML document is always
// revalidated, so a deploy is picked up without a hard reload, while the
// versioned assets keep their normal caching behavior.
func TestUIHandlerHTMLIsNotCached(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"index.html":    "<html></html>",
		"blacksmith.js": "console.log(1)",
	}

	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	handler := getUIHandler(&config.Config{WebRoot: root})

	cases := []struct {
		path    string
		noCache bool
	}{
		{"/", true},
		{"/index.html", true},
		{"/blacksmith.js", false},
	}

	for _, tc := range cases {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

		got := strings.Contains(rec.Header().Get("Cache-Control"), "no-cache")
		if got != tc.noCache {
			t.Errorf("%s: no-cache = %v, want %v (status %d, header %q)",
				tc.path, got, tc.noCache, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}
