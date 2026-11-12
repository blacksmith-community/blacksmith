package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/config"
)

const (
	wantKeyedCacheControl  = "private, max-age=31536000, immutable"
	wantRevalidatedControl = "no-cache"
)

// TestUIHandlerCacheControl checks the two cache policies. A request that
// carries a ?v= key names one exact build of an asset, so the browser may
// keep it for a year without asking again. Everything else, the document
// included, is revalidated on every load, so a deploy is picked up without
// a hard reload.
func TestUIHandlerCacheControl(t *testing.T) {
	t.Parallel()

	handler := getUIHandler(&config.Config{WebRoot: writeCacheWebRoot(t)})

	cases := []struct {
		name   string
		method string
		path   string
		status int
		want   string
	}{
		{"the document", http.MethodGet, "/", http.StatusOK, wantRevalidatedControl},
		{"the document by name", http.MethodGet, "/index.html", http.StatusMovedPermanently, wantRevalidatedControl},
		{"the document with a key", http.MethodGet, "/?v=1", http.StatusOK, wantRevalidatedControl},
		{"an unkeyed script", http.MethodGet, "/blacksmith.js", http.StatusOK, wantRevalidatedControl},
		{"a keyed script", http.MethodGet, "/blacksmith.js?v=20261112-oat-1", http.StatusOK, wantKeyedCacheControl},
		{"a keyed script by HEAD", http.MethodHead, "/blacksmith.js?v=20261112-oat-1", http.StatusOK, wantKeyedCacheControl},
		{"an empty key", http.MethodGet, "/blacksmith.js?v=", http.StatusOK, wantRevalidatedControl},
		{"a keyed vendor stylesheet", http.MethodGet, "/vendor/oat/oat.css?v=oat-1.0.0", http.StatusOK, wantKeyedCacheControl},
		{"an unkeyed vendor stylesheet", http.MethodGet, "/vendor/oat/oat.css", http.StatusOK, wantRevalidatedControl},
		{"an unkeyed image", http.MethodGet, "/assets/brand/x.png", http.StatusOK, wantRevalidatedControl},
		{"the manifest", http.MethodGet, "/manifest.webmanifest", http.StatusOK, wantRevalidatedControl},
		{"a keyed missing file", http.MethodGet, "/missing.js?v=1", http.StatusNotFound, wantRevalidatedControl},
		{"a keyed directory redirect", http.MethodGet, "/vendor/oat?v=1", http.StatusMovedPermanently, wantRevalidatedControl},
		{"a keyed directory listing", http.MethodGet, "/vendor/oat/?v=1", http.StatusOK, wantRevalidatedControl},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), testCase.method, testCase.path, nil))

			if rec.Code != testCase.status {
				t.Fatalf("%s %s: status %d, want %d", testCase.method, testCase.path, rec.Code, testCase.status)
			}

			if got := rec.Header().Get("Cache-Control"); got != testCase.want {
				t.Errorf("%s %s: Cache-Control %q, want %q", testCase.method, testCase.path, got, testCase.want)
			}
		})
	}
}

// TestUIHandlerKeyedNotModifiedStaysImmutable checks a keyed revalidation
// answered with 304 keeps the long-lived policy, so the browser goes on
// trusting its copy.
func TestUIHandlerKeyedNotModifiedStaysImmutable(t *testing.T) {
	t.Parallel()

	handler := getUIHandler(&config.Config{WebRoot: writeCacheWebRoot(t)})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/blacksmith.js?v=1", nil)
	req.Header.Set("If-Modified-Since", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf("status %d, want %d", rec.Code, http.StatusNotModified)
	}

	if got := rec.Header().Get("Cache-Control"); got != wantKeyedCacheControl {
		t.Errorf("Cache-Control %q, want %q", got, wantKeyedCacheControl)
	}
}

// writeCacheWebRoot writes a small web root with a document, a script, a
// vendor stylesheet, an image, and a manifest, and returns its path.
func writeCacheWebRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"index.html":           "<html></html>",
		"blacksmith.js":        "void 0",
		"vendor/oat/oat.css":   "a{}",
		"assets/brand/x.png":   "png",
		"manifest.webmanifest": "{}",
	}

	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))

		err := os.MkdirAll(filepath.Dir(full), 0o750)
		if err != nil {
			t.Fatal(err)
		}

		err = os.WriteFile(full, []byte(body), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}

	return root
}

// TestUIHandlerKeyedPageAssetsAreImmutable serves every asset the real page
// asks for with a ?v= key, and checks each one is found and cached for good.
// It also checks the Oat files carry a key, since an upgrade must replace
// them in every browser that has the old copy.
func TestUIHandlerKeyedPageAssetsAreImmutable(t *testing.T) {
	t.Parallel()

	page, err := os.ReadFile(filepath.Join("ui", "index.html"))
	if err != nil {
		t.Fatal(err)
	}

	handler := getUIHandler(&config.Config{WebRoot: "ui"})
	keyed := regexp.MustCompile(`(?:href|src)="(/[^"?]+\?v=[^"]+)"`).FindAllStringSubmatch(string(page), -1)

	if len(keyed) == 0 {
		t.Fatal("the page references no keyed assets")
	}

	seen := map[string]bool{}

	for _, match := range keyed {
		target := match[1]
		file, _, _ := strings.Cut(target, "?")
		seen[file] = true

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want %d", target, rec.Code, http.StatusOK)

			continue
		}

		if got := rec.Header().Get("Cache-Control"); got != wantKeyedCacheControl {
			t.Errorf("%s: Cache-Control %q, want %q", target, got, wantKeyedCacheControl)
		}
	}

	for _, oat := range []string{"/vendor/oat/oat.min.css", "/vendor/oat/oat.min.js"} {
		if !seen[oat] {
			t.Errorf("%s is not loaded with a ?v= key", oat)
		}
	}
}
