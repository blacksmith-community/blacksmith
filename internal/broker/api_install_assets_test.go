package broker_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blacksmith/internal/broker"
)

// publicInstallAssets is every path a browser may fetch without the broker
// credential. The manifest, the favicon, and the brand icons are what a
// browser or an installed app asks for before it has a password to send.
var publicInstallAssets = []string{
	"/manifest.webmanifest",
	"/favicon.ico",
	"/assets/brand/icon-16.png",
	"/assets/brand/icon-32.png",
	"/assets/brand/icon-48.png",
	"/assets/brand/icon-180.png",
	"/assets/brand/icon-192.png",
	"/assets/brand/icon-512.png",
}

// guardedPaths must all still need the credential. They cover the document,
// the other UI assets, the internal and broker APIs, near misses on every
// allowlisted name, path traversal in plain and percent-encoded forms,
// encoded slashes, and doubled or trailing slashes.
var guardedPaths = []string{
	// The document and the rest of the UI.
	"/",
	"/index.html",
	"/blacksmith.js",
	"/blacksmith.js?v=20261112-oat-422",
	"/vendor/oat/oat.min.css",
	"/assets/",
	"/assets/brand",
	"/assets/brand/",
	"/assets/brand/README.md",
	"/assets/brand/forge-master.png",
	"/secret.txt",

	// The internal API and the broker API.
	"/b/",
	"/b/upgrade/tasks",
	"/b/upgrade/commands/availability",
	"/b/status",
	"/v2/",
	"/v2/catalog",
	"/v2/service_instances/x",

	// Near misses on the allowlisted names.
	"/manifest.json",
	"/manifest.webmanifest.bak",
	"/manifest.webmanifest/",
	"/manifest.webmanifestx",
	"/Manifest.webmanifest",
	"/MANIFEST.WEBMANIFEST",
	"/favicon.ico/",
	"/favicon.ico.map",
	"/favicon.icox",
	"/Favicon.ico",
	"/favicon.ico;x",
	"/assets/brand/icon-192.png/",
	"/assets/brand/icon-192.png.map",
	"/assets/brand/icon-192.PNG",
	"/assets/brand/Icon-192.png",
	"/assets/brand/icon-.png",
	"/assets/brand/icon-x.png",
	"/assets/brand/icon-192.svg",
	"/assets/brand/icon-192-.png",
	"/assets/brand/icon--192.png",
	"/assets/brand/icon-192.png;x",
	"/assets/brand/sub/icon-192.png",
	"/assets/brand/icon-192.png/icon-192.png",
	"/assets/Brand/icon-192.png",
	"/x/assets/brand/icon-192.png",
	"/x/favicon.ico",
	"/b/manifest.webmanifest",
	"/v2/favicon.ico",

	// Doubled slashes and dot segments.
	"//manifest.webmanifest",
	"//favicon.ico",
	"/./favicon.ico",
	"/assets//brand/icon-192.png",
	"/assets/brand//icon-192.png",
	"/assets/brand/./icon-192.png",
	"/assets/./brand/icon-192.png",

	// Traversal out of an allowlisted path.
	"/assets/brand/../../index.html",
	"/assets/brand/../../secret.txt",
	"/assets/brand/../../b/upgrade/tasks",
	"/assets/brand/../../v2/catalog",
	"/assets/brand/icon-192.png/../../../secret.txt",
	"/favicon.ico/../secret.txt",
	"/favicon.ico/../b/upgrade/tasks",
	"/manifest.webmanifest/../v2/catalog",
	"/manifest.webmanifest/../index.html",

	// Traversal into an allowlisted path.
	"/b/../favicon.ico",
	"/v2/../manifest.webmanifest",
	"/secret/../assets/brand/icon-192.png",

	// Percent-encoded dots, slashes, and letters.
	"/assets/brand/..%2f..%2fsecret.txt",
	"/assets/brand/..%2F..%2Fb%2Fupgrade%2Ftasks",
	"/assets/brand/%2e%2e/%2e%2e/secret.txt",
	"/assets/brand/%2E%2E/%2E%2E/index.html",
	"/assets/brand/icon-192.png%2f..%2f..%2f..%2fsecret.txt",
	"/assets%2fbrand%2ficon-192.png",
	"/assets/brand%2ficon-192.png",
	"/favicon%2eico",
	"/favicon.ic%6f",
	"/%66avicon.ico",
	"/manifest%2Ewebmanifest",
	"/%6d%61%6e%69%66%65%73%74.webmanifest",
	"/assets/brand/icon%2d192.png",
	"/assets/brand/icon-192%2epng",
	"/assets/brand/icon-%31%39%32.png",
	"/favicon.ico%2f..%2fsecret.txt",
	"/favicon.ico%00",
	"/assets/brand/icon-192.png%00",
	"/assets/brand/icon-192.png%00.txt",
	"/%2e/favicon.ico",
	"/%2f/favicon.ico",
	"/favicon.ico%3f",
	"/favicon.ico%23",
	"/assets/brand/%69con-192.png",
	"/assets/brand/icon-192.png%20",
	"/b%2f..%2ffavicon.ico",

	// Backslashes, which some servers read as separators.
	"/assets/brand/..%5c..%5csecret.txt",
	"/assets/brand/icon-192.png%5c..%5csecret.txt",
	"/assets\\brand\\icon-192.png",
}

// installAssetAPI builds an API whose handlers record whether they ran and
// which path the web root was asked for.
func installAssetAPI(t *testing.T, webRoot http.Handler) (*broker.API, *[]string) {
	t.Helper()

	var reached []string

	record := func(name string, next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = append(reached, name+" "+r.URL.Path)

			next.ServeHTTP(w, r)
		})
	}

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	return &broker.API{
		Username: "admin",
		Password: "pw",
		Internal: record("internal", ok),
		Primary:  record("primary", ok),
		WebRoot:  record("webroot", webRoot),
	}, &reached
}

func serve(t *testing.T, api *broker.API, method, target string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, nil)
	for name, values := range header {
		req.Header[name] = values
	}

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)

	return rec
}

// TestInstallAssetsAreServedWithoutCredentials checks that a browser or an
// installed app can fetch the manifest, the favicon, and each brand icon
// with no credential, by GET and by HEAD, and that the web root is asked
// for exactly the path requested.
func TestInstallAssetsAreServedWithoutCredentials(t *testing.T) {
	t.Parallel()

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, target := range append(append([]string{}, publicInstallAssets...), "/manifest.webmanifest?v=1", "/assets/brand/icon-192.png?v=1") {
			t.Run(method+" "+target, func(t *testing.T) {
				t.Parallel()

				api, reached := installAssetAPI(t, ok)
				rec := serve(t, api, method, target, nil)

				if rec.Code != http.StatusOK {
					t.Fatalf("status %d, want %d", rec.Code, http.StatusOK)
				}

				path, _, _ := strings.Cut(target, "?")
				if len(*reached) != 1 || (*reached)[0] != "webroot "+path {
					t.Fatalf("handlers reached %q, want only the web root for %s", *reached, path)
				}
			})
		}
	}
}

// TestInstallAssetsIgnoreCredentials checks that an allowlisted file is
// served whatever credential arrives with it. The manifest link sends the
// browser's stored credential, which may be stale.
func TestInstallAssetsIgnoreCredentials(t *testing.T) {
	t.Parallel()

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for _, user := range [][2]string{{"admin", "pw"}, {"admin", "wrong"}} {
		api, reached := installAssetAPI(t, ok)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/manifest.webmanifest", nil)
		req.SetBasicAuth(user[0], user[1])

		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK || len(*reached) != 1 {
			t.Fatalf("password %q: status %d, reached %q", user[1], rec.Code, *reached)
		}
	}
}

// TestEveryOtherPathStillNeedsCredentials sends each guarded path with no
// credential and checks it is refused with the basic-auth challenge before
// any handler runs. It then checks the allowlisted names are refused for
// every method but GET and HEAD.
func TestEveryOtherPathStillNeedsCredentials(t *testing.T) {
	t.Parallel()

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	assertRefused := func(t *testing.T, method, target string) {
		t.Helper()

		api, reached := installAssetAPI(t, ok)
		rec := serve(t, api, method, target, nil)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %q: status %d, want %d", method, target, rec.Code, http.StatusUnauthorized)
		}

		if got := rec.Header().Get("WWW-Authenticate"); got != "basic realm=Blacksmith" {
			t.Errorf("%s %q: WWW-Authenticate %q", method, target, got)
		}

		if len(*reached) != 0 {
			t.Errorf("%s %q: an unauthenticated request reached %q", method, target, *reached)
		}
	}

	for _, target := range guardedPaths {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			assertRefused(t, method, target)
		}
	}

	for _, target := range publicInstallAssets {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions, "PROPFIND"} {
			assertRefused(t, method, target)
		}
	}
}

// TestGuardedPathsStillWorkWithCredentials checks the allowlist changed
// nothing for a request that carries the right credential, and that a wrong
// one is still refused on a guarded path.
func TestGuardedPathsStillWorkWithCredentials(t *testing.T) {
	t.Parallel()

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	for target, want := range map[string]string{
		"/":                 "webroot /",
		"/blacksmith.js":    "webroot /blacksmith.js",
		"/b/upgrade/tasks":  "internal /b/upgrade/tasks",
		"/v2/catalog":       "primary /v2/catalog",
		"/assets/brand/":    "webroot /assets/brand/",
		"/assets/brand/x.y": "webroot /assets/brand/x.y",
	} {
		api, reached := installAssetAPI(t, ok)

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		req.SetBasicAuth("admin", "pw")

		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK || len(*reached) != 1 || (*reached)[0] != want {
			t.Errorf("%s: status %d, reached %q, want %q", target, rec.Code, *reached, want)
		}

		api, reached = installAssetAPI(t, ok)

		req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
		req.SetBasicAuth("admin", "wrong")

		rec = httptest.NewRecorder()
		api.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden || len(*reached) != 0 {
			t.Errorf("%s with a wrong password: status %d, reached %q", target, rec.Code, *reached)
		}
	}
}

// TestInstallAssetsNeverLeakOtherFiles puts a real file server behind the
// API, with a secret file beside the web root and another inside it, and
// checks that no unauthenticated request in the guarded list returns
// either one, and that each allowlisted request returns its own file.
func TestInstallAssetsNeverLeakOtherFiles(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	root := filepath.Join(base, "ui")
	files := map[string]string{
		"../secret.txt":                 "outside-secret",
		"secret.txt":                    "inside-secret",
		"index.html":                    "inside-secret-page",
		"manifest.webmanifest":          "public-manifest",
		"favicon.ico":                   "public-favicon",
		"assets/brand/README.md":        "inside-secret-readme",
		"assets/brand/forge-master.png": "inside-secret-master",
		"assets/brand/icon-16.png":      "public-icon-16",
		"assets/brand/icon-32.png":      "public-icon-32",
		"assets/brand/icon-48.png":      "public-icon-48",
		"assets/brand/icon-180.png":     "public-icon-180",
		"assets/brand/icon-192.png":     "public-icon-192",
		"assets/brand/icon-512.png":     "public-icon-512",
		"assets/brand/sub/icon-192.png": "inside-secret-nested",
	}

	for name, body := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	api, _ := installAssetAPI(t, http.FileServer(http.Dir(root)))

	for _, target := range guardedPaths {
		rec := serve(t, api, http.MethodGet, target, nil)
		if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "public-") {
			t.Errorf("GET %q without a credential returned %d with a file body", target, rec.Code)
		}
	}

	for _, target := range publicInstallAssets {
		rec := serve(t, api, http.MethodGet, target, nil)
		want := files[strings.TrimPrefix(target, "/")]

		if rec.Code != http.StatusOK || rec.Body.String() != want {
			t.Errorf("GET %s: status %d, body %q, want %q", target, rec.Code, rec.Body.String(), want)
		}
	}
}
