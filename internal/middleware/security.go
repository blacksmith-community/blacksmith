package middleware

import (
	"net/http"
	"strings"

	pkgmiddleware "blacksmith/pkg/http/middleware"
	"blacksmith/pkg/services"
)

// SecurityMiddleware creates a middleware that handles security validation.
func SecurityMiddleware(securityManager *services.SecurityMiddleware) pkgmiddleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			// Skip security validation for certain endpoints (like health checks)
			if isPublicEndpoint(request.URL.Path) {
				next.ServeHTTP(writer, request)

				return
			}

			// Extract instance ID from path if available
			instanceID := extractInstanceIDFromPath(request.URL.Path)
			if instanceID == "" {
				// Some endpoints don't have instance IDs, allow them through
				next.ServeHTTP(writer, request)

				return
			}

			// Basic security validation
			params := map[string]interface{}{
				"method":      request.Method,
				"path":        request.URL.Path,
				"instance_id": instanceID,
			}

			err := securityManager.ValidateRequest(instanceID, "http_request", params)
			if err != nil {
				if securityManager.HandleSecurityError(writer, err) {
					return
				}
			}

			// Add rate limit headers
			if headers := securityManager.GetRateLimitHeaders(instanceID, "http_request"); headers != nil {
				for key, value := range headers {
					writer.Header().Set(key, value)
				}
			}

			next.ServeHTTP(writer, request)
		})
	}
}

// isPublicEndpoint determines if an endpoint should skip security validation.
func isPublicEndpoint(path string) bool {
	publicEndpoints := []string{
		"/b/instance",
		"/b/config/ssh/ui-terminal-status",
		"/b/internal/health",
	}

	for _, endpoint := range publicEndpoints {
		if path == endpoint {
			return true
		}
	}

	return false
}

// extractInstanceIDFromPath extracts instance ID from URL paths that contain it.
func extractInstanceIDFromPath(path string) string {
	// Common patterns: /b/{instanceID}/service/operation
	parts := strings.Split(path, "/")
	if len(parts) >= 3 && parts[1] == "b" {
		// Skip if it's a known non-instance path
		if parts[2] == "cf" || parts[2] == "certificates" || parts[2] == "instance" || parts[2] == "config" {
			return ""
		}

		return parts[2]
	}

	return ""
}
