package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"blacksmith/pkg/services/common"
)

// SecurityMiddleware provides security enhancements for service testing
type SecurityMiddleware struct {
	rateLimiter *common.RateLimiter
	auditLogger *common.AuditLogger
	enabled     bool
}

// NewSecurityMiddleware creates a new security middleware
func NewSecurityMiddleware(logger func(string, ...interface{})) *SecurityMiddleware {
	return &SecurityMiddleware{
		rateLimiter: common.NewRateLimiter(100, time.Minute), // 100 requests per minute
		auditLogger: common.NewAuditLogger(logger),
		enabled:     true,
	}
}

// ValidateRequest performs security validation on incoming requests
func (sm *SecurityMiddleware) ValidateRequest(instanceID, operation string, params map[string]interface{}) error {
	if !sm.enabled {
		return nil
	}

	// Rate limiting
	rateLimitKey := fmt.Sprintf("%s:%s", instanceID, operation)
	if !sm.rateLimiter.AllowRequest(rateLimitKey) {
		remaining := sm.rateLimiter.GetRemainingRequests(rateLimitKey)
		return common.NewServiceError("security", "E007",
			fmt.Sprintf("rate limit exceeded, %d requests remaining", remaining), true)
	}

	// Input validation
	if err := common.ValidateInputs(params); err != nil {
		return common.NewServiceError("security", "E008",
			fmt.Sprintf("input validation failed: %v", err), false)
	}

	return nil
}

// LogOperation logs an operation for audit purposes
func (sm *SecurityMiddleware) LogOperation(user, instanceID, serviceType, operation string, params interface{}, result interface{}, err error) {
	if sm.enabled && sm.auditLogger != nil {
		sm.auditLogger.LogOperation(user, instanceID, serviceType, operation, params, result, err)
	}
}

// HandleSecurityError formats security errors for HTTP responses
func (sm *SecurityMiddleware) HandleSecurityError(w http.ResponseWriter, err error) bool {
	if serviceErr, ok := err.(*common.ServiceError); ok && serviceErr.ServiceType == "security" {
		statusCode := 429 // Too Many Requests
		if serviceErr.Code == "E008" {
			statusCode = 400 // Bad Request for validation errors
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)

		response := map[string]interface{}{
			"success": false,
			"error":   serviceErr.Message,
			"code":    serviceErr.Code,
		}

		if jsonData, jsonErr := json.Marshal(response); jsonErr == nil {
			if _, writeErr := w.Write(jsonData); writeErr != nil {
				// Log write error but continue processing
			}
		} else {
			fmt.Fprintf(w, `{"success": false, "error": "internal error"}`)
		}

		return true
	}

	return false
}

// GetRateLimitHeaders returns rate limit headers for responses
func (sm *SecurityMiddleware) GetRateLimitHeaders(instanceID, operation string) map[string]string {
	if !sm.enabled {
		return nil
	}

	rateLimitKey := fmt.Sprintf("%s:%s", instanceID, operation)
	remaining := sm.rateLimiter.GetRemainingRequests(rateLimitKey)

	return map[string]string{
		"X-RateLimit-Limit":     fmt.Sprintf("%d", sm.rateLimiter.Limit),
		"X-RateLimit-Remaining": fmt.Sprintf("%d", remaining),
		"X-RateLimit-Window":    sm.rateLimiter.Window.String(),
	}
}
