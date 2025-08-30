package main

import (
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"
)

// CompressionMiddleware wraps an HTTP handler to provide response compression
type CompressionMiddleware struct {
	handler http.Handler
	config  CompressionConfig
}

// NewCompressionMiddleware creates a new compression middleware with the given configuration
func NewCompressionMiddleware(handler http.Handler, config CompressionConfig) *CompressionMiddleware {
	// Set defaults if not configured
	if len(config.Types) == 0 {
		config.Types = []string{"gzip"}
	}
	if config.Level == 0 {
		config.Level = -1 // Default compression level
	}
	if config.MinSize == 0 {
		config.MinSize = 1024 // 1KB minimum
	}
	if len(config.ContentTypes) == 0 {
		config.ContentTypes = []string{
			"text/html",
			"text/css",
			"text/javascript",
			"text/plain",
			"text/xml",
			"application/json",
			"application/javascript",
			"application/xml",
			"application/rss+xml",
			"application/atom+xml",
			"image/svg+xml",
		}
	}

	return &CompressionMiddleware{
		handler: handler,
		config:  config,
	}
}

// ServeHTTP implements the http.Handler interface
func (cm *CompressionMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !cm.config.Enabled {
		cm.handler.ServeHTTP(w, r)
		return
	}

	// Check if client accepts compression
	acceptEncoding := r.Header.Get("Accept-Encoding")
	if acceptEncoding == "" {
		cm.handler.ServeHTTP(w, r)
		return
	}

	// Determine the best compression method to use
	compressionType := cm.getBestCompression(acceptEncoding)
	if compressionType == "" {
		cm.handler.ServeHTTP(w, r)
		return
	}

	// Create a response recorder to capture the response
	recorder := &responseRecorder{
		ResponseWriter:        w,
		compressionMiddleware: cm,
		compressionType:       compressionType,
	}

	// Serve the request
	cm.handler.ServeHTTP(recorder, r)

	// Important: Close the compressor to flush any remaining compressed data
	if err := recorder.Close(); err != nil {
		Logger.Error("Failed to close compression stream: %s", err)
	}
}

// getBestCompression returns the best compression method supported by both client and server
func (cm *CompressionMiddleware) getBestCompression(acceptEncoding string) string {
	acceptEncoding = strings.ToLower(acceptEncoding)

	// Check compression types in order of preference (more efficient first)
	for _, compressionType := range cm.config.Types {
		compressionType = strings.ToLower(compressionType)
		if strings.Contains(acceptEncoding, compressionType) {
			return compressionType
		}
	}
	return ""
}

// shouldCompress determines if a response should be compressed based on content type and size
func (cm *CompressionMiddleware) shouldCompress(contentType string, contentLength int) bool {
	// Check minimum size requirement
	// If we know the content length, check if it meets the minimum size
	if contentLength < cm.config.MinSize {
		return false
	}

	// Check if content type should be compressed
	contentType = strings.ToLower(strings.Split(contentType, ";")[0]) // Remove charset info
	for _, ct := range cm.config.ContentTypes {
		if strings.HasPrefix(contentType, strings.ToLower(ct)) {
			return true
		}
	}

	return false
}

// responseRecorder captures the response to determine if compression should be applied
type responseRecorder struct {
	http.ResponseWriter
	compressionMiddleware *CompressionMiddleware
	compressionType       string
	headerWritten         bool
	compressor            io.WriteCloser
	buffer                []byte // Buffer to accumulate response until we know if we should compress
	statusCode            int    // Store status code until we decide on compression
}

// Header returns the header map
func (rr *responseRecorder) Header() http.Header {
	return rr.ResponseWriter.Header()
}

// WriteHeader stores the status code but doesn't write headers yet
func (rr *responseRecorder) WriteHeader(statusCode int) {
	if rr.headerWritten {
		return
	}
	// Store the status code for later
	rr.statusCode = statusCode
}

// Write buffers data and makes compression decision based on actual content size
func (rr *responseRecorder) Write(data []byte) (int, error) {
	// If headers are already written, just write the data
	if rr.headerWritten {
		if rr.compressor != nil {
			return rr.compressor.Write(data)
		}
		return rr.ResponseWriter.Write(data)
	}

	// Buffer the data
	rr.buffer = append(rr.buffer, data...)

	// For now, just return the length as if it was written
	// The actual write will happen in Close() or when buffer gets large enough
	return len(data), nil
}

// Close flushes buffered data and closes the compressor if one is active
func (rr *responseRecorder) Close() error {
	// If headers haven't been written yet, we need to make the compression decision now
	if !rr.headerWritten {
		rr.finalizeHeaders()
	}

	// Write any buffered data
	if len(rr.buffer) > 0 {
		var err error
		if rr.compressor != nil {
			_, err = rr.compressor.Write(rr.buffer)
		} else {
			_, err = rr.ResponseWriter.Write(rr.buffer)
		}
		if err != nil {
			return err
		}
	}

	// Close the compressor if active
	if rr.compressor != nil {
		return rr.compressor.Close()
	}
	return nil
}

// finalizeHeaders makes the final compression decision based on buffered content
func (rr *responseRecorder) finalizeHeaders() {
	if rr.headerWritten {
		return
	}
	rr.headerWritten = true

	// Use default status code if not set
	if rr.statusCode == 0 {
		rr.statusCode = http.StatusOK
	}

	contentType := rr.Header().Get("Content-Type")
	contentLength := len(rr.buffer)

	// Check if we should compress based on actual content size
	if rr.compressionMiddleware.shouldCompress(contentType, contentLength) {
		// Remove Content-Length header as it will change after compression
		rr.Header().Del("Content-Length")

		// Set compression headers
		rr.Header().Set("Content-Encoding", rr.compressionType)
		rr.Header().Set("Vary", "Accept-Encoding")

		// Create the appropriate compressor
		var err error
		switch rr.compressionType {
		case "gzip":
			if rr.compressionMiddleware.config.Level == -1 {
				rr.compressor = gzip.NewWriter(rr.ResponseWriter)
			} else {
				rr.compressor, err = gzip.NewWriterLevel(rr.ResponseWriter, rr.compressionMiddleware.config.Level)
			}
		case "deflate":
			if rr.compressionMiddleware.config.Level == -1 {
				rr.compressor, err = flate.NewWriter(rr.ResponseWriter, flate.DefaultCompression)
			} else {
				rr.compressor, err = flate.NewWriter(rr.ResponseWriter, rr.compressionMiddleware.config.Level)
			}
		case "br", "brotli":
			if rr.compressionMiddleware.config.Level == -1 {
				rr.compressor = brotli.NewWriter(rr.ResponseWriter)
			} else {
				rr.compressor = brotli.NewWriterLevel(rr.ResponseWriter, rr.compressionMiddleware.config.Level)
			}
		}

		if err != nil {
			// If compression setup fails, fall back to uncompressed response
			Logger.Error("Failed to create compressor for %s: %s", rr.compressionType, err)
			rr.Header().Del("Content-Encoding")
			rr.Header().Del("Vary")
			rr.compressor = nil
		}
	}

	// Write the actual status code
	rr.ResponseWriter.WriteHeader(rr.statusCode)
}

// Flush implements http.Flusher interface
func (rr *responseRecorder) Flush() {
	// For streaming, we need to finalize headers and flush buffered content
	if !rr.headerWritten {
		rr.finalizeHeaders()
	}

	// Write any buffered data
	if len(rr.buffer) > 0 {
		if rr.compressor != nil {
			if _, err := rr.compressor.Write(rr.buffer); err != nil {
				Logger.Error("Failed to write compressed data during flush: %s", err)
			}
		} else {
			if _, err := rr.ResponseWriter.Write(rr.buffer); err != nil {
				Logger.Error("Failed to write uncompressed data during flush: %s", err)
			}
		}
		rr.buffer = nil // Clear the buffer after flushing
	}

	if rr.compressor != nil {
		// For streaming responses, we need to flush the compressor first
		if flusher, ok := rr.compressor.(http.Flusher); ok {
			flusher.Flush()
		}
	}
	if flusher, ok := rr.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
