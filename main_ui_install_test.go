package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"blacksmith/internal/broker"
	"blacksmith/internal/config"
)

// installAPI puts the real UI behind the broker's auth, the way the broker
// serves it.
func installAPI() *broker.API {
	refuse := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

	return &broker.API{
		Username: "admin",
		Password: "pw",
		Internal: refuse,
		Primary:  refuse,
		WebRoot:  getUIHandler(&config.Config{WebRoot: "ui"}),
	}
}

// TestInstallAssetsReferencedByThePageNeedNoCredentials reads every icon
// the manifest lists and every icon, favicon, and manifest link in the
// page's head, and checks each one is served from the real UI without the
// broker credential. A browser or an installed app fetches these before
// it has a password to send.
func TestInstallAssetsReferencedByThePageNeedNoCredentials(t *testing.T) {
	t.Parallel()

	page, err := os.ReadFile(filepath.Join("ui", "index.html"))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join("ui", "manifest.webmanifest"))
	if err != nil {
		t.Fatal(err)
	}

	var manifest struct {
		Icons []struct {
			Src string `json:"src"`
		} `json:"icons"`
	}

	err = json.Unmarshal(raw, &manifest)
	if err != nil {
		t.Fatal(err)
	}

	targets := map[string]bool{}
	for _, icon := range manifest.Icons {
		targets[icon.Src] = true
	}

	links := regexp.MustCompile(`<link\s[^>]*rel="(?:manifest|icon|apple-touch-icon)"[^>]*href="([^"]+)"`).FindAllStringSubmatch(string(page), -1)
	for _, link := range links {
		targets[link[1]] = true
	}

	if len(links) < 3 || len(manifest.Icons) == 0 {
		t.Fatalf("found %d head links and %d manifest icons, want the manifest, favicon, and touch icon links and some icons", len(links), len(manifest.Icons))
	}

	api := installAPI()

	for target := range targets {
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))

		if rec.Code != http.StatusOK {
			t.Errorf("%s without a credential: status %d, want %d", target, rec.Code, http.StatusOK)
		}
	}
}

// TestRealUIStillNeedsCredentials checks the document, a script, and the
// other files in the brand directory are refused without the credential
// from the real UI.
func TestRealUIStillNeedsCredentials(t *testing.T) {
	t.Parallel()

	api := installAPI()

	for _, target := range []string{"/", "/index.html", "/blacksmith.js", "/assets/brand/", "/assets/brand/README.md", "/assets/brand/forge-master.png", "/vendor/oat/oat.min.js"} {
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil))

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a credential: status %d, want %d", target, rec.Code, http.StatusUnauthorized)
		}
	}
}
