package middleware

import (
	"net/http"
	"time"

	"blacksmith/internal/interfaces"
	pkgmiddleware "blacksmith/pkg/http/middleware"
)

// LoggingMiddleware creates a middleware that logs HTTP requests.
func LoggingMiddleware(logger interfaces.Logger) pkgmiddleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			start := time.Now()

			// Wrap the response writer to capture status code
			wrappedWriter := &responseWriter{
				ResponseWriter: w,
				statusCode:     http.StatusOK, // Default to 200
			}

			requestLogger := logger.Named("http-request")
			requestLogger.Debug("Starting request: %s %s", request.Method, request.URL.Path)

			// Call the next handler
			next.ServeHTTP(wrappedWriter, request)

			duration := time.Since(start)
			requestLogger.Info("Completed request: %s %s - Status: %d - Duration: %v",
				request.Method, request.URL.Path, wrappedWriter.statusCode, duration)
		})
	}
}

// responseWriter wraps http.ResponseWriter to capture the status code.
type responseWriter struct {
	http.ResponseWriter

	statusCode int
}

// WriteHeader captures the status code.
func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}
