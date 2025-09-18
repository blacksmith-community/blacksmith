package compression_test

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/compression"
)

// createTestHTMLHandler creates a handler that returns HTML content.
func createTestHTMLHandler() http.HandlerFunc {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html")

		content := `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<h1>This is a test page that should be compressed</h1>
<p>This content is long enough to exceed the minimum compression size threshold.</p>
</body>
</html>`
		_, _ = fmt.Fprint(writer, content)
	})
}

// performRequest performs an HTTP request and returns the response.
func performRequest(handler http.Handler, acceptEncoding string) *http.Response {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	return w.Result()
}

// verifyGzipContent verifies that content can be decompressed and contains expected text.
func verifyGzipContent(t *testing.T, body []byte, expectedContent string) {
	t.Helper()

	reader, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Failed to create gzip reader: %v", err)
	}

	defer func() { _ = reader.Close() }()

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("Failed to decompress response: %v", err)
	}

	if !strings.Contains(string(decompressed), expectedContent) {
		t.Error("Decompressed content doesn't match expected content")
	}
}

func testCompressionEnabled(t *testing.T, handler http.Handler) {
	t.Helper()

	config := compression.Config{
		Enabled:      true,
		Types:        []string{"gzip"},
		Level:        -1,
		MinSize:      100,
		ContentTypes: []string{"text/html"},
	}
	compressedHandler := compression.NewMiddleware(handler, config)

	resp := performRequest(compressedHandler, "gzip, deflate")

	defer func() { _ = resp.Body.Close() }()

	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("Expected Content-Encoding: gzip, got: %s", resp.Header.Get("Content-Encoding"))
	}

	if resp.Header.Get("Vary") != "Accept-Encoding" {
		t.Errorf("Expected Vary: Accept-Encoding, got: %s", resp.Header.Get("Vary"))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	verifyGzipContent(t, body, "This is a test page")
}

func testCompressionDisabled(t *testing.T, handler http.Handler) {
	t.Helper()

	config := compression.Config{Enabled: false}
	uncompressedHandler := compression.NewMiddleware(handler, config)

	resp := performRequest(uncompressedHandler, "gzip, deflate")

	defer func() { _ = resp.Body.Close() }()

	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Expected no Content-Encoding header, got: %s", resp.Header.Get("Content-Encoding"))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	if !strings.Contains(string(body), "This is a test page") {
		t.Error("Uncompressed content doesn't match expected content")
	}
}

func testNoAcceptEncoding(t *testing.T, handler http.Handler) {
	t.Helper()

	config := compression.Config{
		Enabled:      true,
		Types:        []string{"gzip"},
		ContentTypes: []string{"text/html"},
	}
	compressedHandler := compression.NewMiddleware(handler, config)

	resp := performRequest(compressedHandler, "")

	defer func() { _ = resp.Body.Close() }()

	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Expected no Content-Encoding header, got: %s", resp.Header.Get("Content-Encoding"))
	}
}

func testSmallContent(t *testing.T) {
	t.Helper()

	smallHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "Small")
	})
	config := compression.Config{
		Enabled:      true,
		Types:        []string{"gzip"},
		MinSize:      1024,
		ContentTypes: []string{"text/html"},
	}
	compressedHandler := compression.NewMiddleware(smallHandler, config)

	resp := performRequest(compressedHandler, "gzip")

	defer func() { _ = resp.Body.Close() }()

	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Expected no compression for small content, got: %s", resp.Header.Get("Content-Encoding"))
	}
}

func testUnsupportedContentType(t *testing.T) {
	t.Helper()

	imageHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = fmt.Fprint(w, "This is supposed to be image data that shouldn't be compressed")
	})
	config := compression.Config{
		Enabled:      true,
		Types:        []string{"gzip"},
		ContentTypes: []string{"text/html", "application/json"},
	}
	compressedHandler := compression.NewMiddleware(imageHandler, config)
	req := httptest.NewRequest(http.MethodGet, "/image.png", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	w := httptest.NewRecorder()
	compressedHandler.ServeHTTP(w, req)

	resp := w.Result()

	defer func() { _ = resp.Body.Close() }()

	if resp.Header.Get("Content-Encoding") != "" {
		t.Errorf("Expected no compression for unsupported content type, got: %s", resp.Header.Get("Content-Encoding"))
	}
}

func TestMiddleware(t *testing.T) {
	t.Parallel()

	handler := createTestHTMLHandler()

	t.Run("CompressionEnabled", func(t *testing.T) {
		t.Parallel()
		testCompressionEnabled(t, handler)
	})

	t.Run("CompressionDisabled", func(t *testing.T) {
		t.Parallel()
		testCompressionDisabled(t, handler)
	})

	t.Run("NoAcceptEncoding", func(t *testing.T) {
		t.Parallel()
		testNoAcceptEncoding(t, handler)
	})

	t.Run("SmallContent", func(t *testing.T) {
		t.Parallel()
		testSmallContent(t)
	})

	t.Run("UnsupportedContentType", func(t *testing.T) {
		t.Parallel()
		testUnsupportedContentType(t)
	})
}

// getCompressionTestCases returns test cases for compression types.
func getCompressionTestCases() []struct {
	name             string
	configTypes      []string
	acceptEncoding   string
	expectedEncoding string
} {
	return []struct {
		name             string
		configTypes      []string
		acceptEncoding   string
		expectedEncoding string
	}{
		{
			name:             "GzipSupported",
			configTypes:      []string{"gzip"},
			acceptEncoding:   "gzip, deflate",
			expectedEncoding: "gzip",
		},
		{
			name:             "DeflateSupported",
			configTypes:      []string{"deflate"},
			acceptEncoding:   "gzip, deflate",
			expectedEncoding: "deflate",
		},
		{
			name:             "BrotliSupported",
			configTypes:      []string{"br"},
			acceptEncoding:   "br, gzip, deflate",
			expectedEncoding: "br",
		},
		{
			name:             "PreferenceOrder",
			configTypes:      []string{"br", "gzip", "deflate"},
			acceptEncoding:   "gzip, deflate, br",
			expectedEncoding: "br",
		},
		{
			name:             "NoMatchingEncoding",
			configTypes:      []string{"gzip"},
			acceptEncoding:   "compress, identity",
			expectedEncoding: "",
		},
	}
}

func TestCompressionTypes(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"message": "This is a JSON response that should be compressed with the requested compression type"}`)
	})

	for _, testCase := range getCompressionTestCases() {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			config := compression.Config{
				Enabled:      true,
				Types:        testCase.configTypes,
				MinSize:      10,
				ContentTypes: []string{"application/json"},
			}
			compressedHandler := compression.NewMiddleware(handler, config)
			req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			req.Header.Set("Accept-Encoding", testCase.acceptEncoding)

			w := httptest.NewRecorder()
			compressedHandler.ServeHTTP(w, req)

			resp := w.Result()

			defer func() { _ = resp.Body.Close() }()

			actualEncoding := resp.Header.Get("Content-Encoding")
			if actualEncoding != testCase.expectedEncoding {
				t.Errorf("Expected Content-Encoding: %s, got: %s", testCase.expectedEncoding, actualEncoding)
			}
		})
	}
}

func TestWebSocketBypassesCompression(t *testing.T) {
	t.Parallel()
	// Handler sets a header and writes a body; middleware should not compress
	// when WebSocket upgrade headers are present, even if client advertises encoding.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Inner-Handler", "true")
		// Simulate what a normal handler might do pre-upgrade
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	})

	config := compression.Config{
		Enabled:      true,
		Types:        []string{"gzip", "deflate", "br"},
		MinSize:      1, // force compression normally
		ContentTypes: []string{"application/json"},
	}

	middleware := compression.NewMiddleware(handler, config)

	req := httptest.NewRequest(http.MethodGet, "/b/blacksmith/ssh/stream", nil)
	// Typical websocket handshake headers
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Accept-Encoding", "gzip, br")

	recorder := httptest.NewRecorder()
	middleware.ServeHTTP(recorder, req)

	resp := recorder.Result()

	defer func() { _ = resp.Body.Close() }()
	// Ensure middleware did not add compression
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		t.Fatalf("expected no compression for websocket upgrade, got: %s", enc)
	}
	// Ensure inner handler ran
	if resp.Header.Get("X-Inner-Handler") != "true" {
		t.Fatalf("inner handler header missing; middleware may have intercepted incorrectly")
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Fatalf("unexpected body: %s", string(body))
	}
}
