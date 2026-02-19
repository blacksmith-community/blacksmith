// Package server provides an HTTP handler that implements the Open Service
// Broker API v2.17 server-side endpoints. Broker authors implement the
// osbapi.ServiceBroker interface and pass it to NewHandler to obtain an
// http.Handler ready for use with any Go HTTP server.
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	osbapi "github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// defaultMinAPIVersion is the lowest broker API version accepted when no
// override is supplied via WithMinAPIVersion.
const defaultMinAPIVersion = "2.13"

// Option configures the handler.
type Option func(*handler)

// WithBasicAuth adds HTTP basic auth middleware.
func WithBasicAuth(username, password string) Option {
	return func(h *handler) {
		h.username = username
		h.password = password
	}
}

// WithLogger sets the logger for the handler.
func WithLogger(logger osbapi.Logger) Option {
	return func(h *handler) {
		h.logger = logger
	}
}

// WithMinAPIVersion sets the minimum supported API version (default "2.13").
func WithMinAPIVersion(version string) Option {
	return func(h *handler) {
		h.minAPIVersion = version
	}
}

// handler holds broker state and configuration used by every endpoint.
type handler struct {
	broker        osbapi.ServiceBroker
	username      string
	password      string
	logger        osbapi.Logger
	minAPIVersion string
}

// NewHandler creates an http.Handler that serves OSB API v2.17 endpoints.
// It uses Go 1.22+ ServeMux with method+pattern routing.
func NewHandler(broker osbapi.ServiceBroker, opts ...Option) http.Handler {
	h := &handler{
		broker:        broker,
		minAPIVersion: defaultMinAPIVersion,
	}
	for _, opt := range opts {
		opt(h)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /v2/catalog", h.catalogHandler)
	mux.HandleFunc("PUT /v2/service_instances/{instance_id}", h.provisionHandler)
	mux.HandleFunc("GET /v2/service_instances/{instance_id}", h.fetchInstanceHandler)
	mux.HandleFunc("PATCH /v2/service_instances/{instance_id}", h.updateHandler)
	mux.HandleFunc("DELETE /v2/service_instances/{instance_id}", h.deprovisionHandler)
	mux.HandleFunc("GET /v2/service_instances/{instance_id}/last_operation", h.lastOperationHandler)

	mux.HandleFunc("PUT /v2/service_instances/{instance_id}/service_bindings/{binding_id}", h.bindHandler)
	mux.HandleFunc("GET /v2/service_instances/{instance_id}/service_bindings/{binding_id}", h.fetchBindingHandler)
	mux.HandleFunc("DELETE /v2/service_instances/{instance_id}/service_bindings/{binding_id}", h.unbindHandler)
	mux.HandleFunc("GET /v2/service_instances/{instance_id}/service_bindings/{binding_id}/last_operation", h.bindingLastOperationHandler)

	return h.applyMiddleware(mux)
}

// ---------------------------------------------------------------------------
// Middleware
// ---------------------------------------------------------------------------

// applyMiddleware wraps the mux with version checking and optional basic auth.
func (h *handler) applyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. API version check.
		version := r.Header.Get(osbapi.HeaderAPIVersion)
		if version == "" {
			writeError(w, http.StatusPreconditionFailed, &osbapi.OSBError{
				ErrorCode:   "MissingAPIVersion",
				Description: osbapi.HeaderAPIVersion + " header is required",
			})
			return
		}
		if versionLessThan(version, h.minAPIVersion) {
			writeError(w, http.StatusPreconditionFailed, &osbapi.OSBError{
				ErrorCode:   "IncompatibleAPIVersion",
				Description: "minimum supported version is " + h.minAPIVersion,
			})
			return
		}

		// 2. Basic auth check (if configured).
		if h.username != "" || h.password != "" {
			u, p, ok := r.BasicAuth()
			if !ok || u != h.username || p != h.password {
				w.Header().Set("WWW-Authenticate", `Basic realm="Service Broker"`)
				writeError(w, http.StatusUnauthorized, &osbapi.OSBError{
					Description: "unauthorized",
				})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// versionLessThan returns true when a < b using simple major.minor comparison.
func versionLessThan(a, b string) bool {
	aParts := strings.SplitN(a, ".", 2)
	bParts := strings.SplitN(b, ".", 2)

	aMajor, aMinor := 0, 0
	bMajor, bMinor := 0, 0

	if len(aParts) > 0 {
		aMajor = atoi(aParts[0])
	}
	if len(aParts) > 1 {
		aMinor = atoi(aParts[1])
	}
	if len(bParts) > 0 {
		bMajor = atoi(bParts[0])
	}
	if len(bParts) > 1 {
		bMinor = atoi(bParts[1])
	}

	if aMajor != bMajor {
		return aMajor < bMajor
	}
	return aMinor < bMinor
}

// atoi is a minimal string-to-int converter (no error handling needed for
// well-formed version strings).
func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Route handlers
// ---------------------------------------------------------------------------

func (h *handler) catalogHandler(w http.ResponseWriter, r *http.Request) {
	catalog, err := h.broker.GetCatalog(r.Context())
	if err != nil {
		h.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, catalog)
}

func (h *handler) provisionHandler(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")

	var req osbapi.ProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, &osbapi.OSBError{
			Description: "invalid request body: " + err.Error(),
		})
		return
	}

	async := r.URL.Query().Get("accepts_incomplete") == "true"

	resp, isAsync, err := h.broker.Provision(r.Context(), instanceID, req, async)
	if err != nil {
		h.handleError(w, err)
		return
	}

	status := http.StatusCreated
	if isAsync {
		status = http.StatusAccepted
	}
	writeJSON(w, status, resp)
}

func (h *handler) fetchInstanceHandler(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")

	req := osbapi.FetchInstanceRequest{
		ServiceID: r.URL.Query().Get("service_id"),
		PlanID:    r.URL.Query().Get("plan_id"),
	}

	resp, err := h.broker.GetInstance(r.Context(), instanceID, req)
	if err != nil {
		h.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) updateHandler(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")

	var req osbapi.UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, &osbapi.OSBError{
			Description: "invalid request body: " + err.Error(),
		})
		return
	}

	async := r.URL.Query().Get("accepts_incomplete") == "true"

	resp, isAsync, err := h.broker.Update(r.Context(), instanceID, req, async)
	if err != nil {
		h.handleError(w, err)
		return
	}

	status := http.StatusOK
	if isAsync {
		status = http.StatusAccepted
	}
	writeJSON(w, status, resp)
}

func (h *handler) deprovisionHandler(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")

	req := osbapi.DeprovisionRequest{
		ServiceID: r.URL.Query().Get("service_id"),
		PlanID:    r.URL.Query().Get("plan_id"),
	}

	async := r.URL.Query().Get("accepts_incomplete") == "true"

	resp, isAsync, err := h.broker.Deprovision(r.Context(), instanceID, req, async)
	if err != nil {
		h.handleError(w, err)
		return
	}

	status := http.StatusOK
	if isAsync {
		status = http.StatusAccepted
	}
	writeJSON(w, status, resp)
}

func (h *handler) lastOperationHandler(w http.ResponseWriter, r *http.Request) {
	instanceID := r.PathValue("instance_id")

	req := osbapi.LastOperationRequest{
		ServiceID: r.URL.Query().Get("service_id"),
		PlanID:    r.URL.Query().Get("plan_id"),
		Operation: r.URL.Query().Get("operation"),
	}

	resp, err := h.broker.LastOperation(r.Context(), instanceID, req)
	if err != nil {
		h.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------------

// handleError maps broker errors to appropriate HTTP responses.
func (h *handler) handleError(w http.ResponseWriter, err error) {
	// Check for typed OSBError first.
	var osbErr *osbapi.OSBError
	if errors.As(err, &osbErr) {
		status := osbErr.StatusCode
		if status == 0 {
			status = http.StatusInternalServerError
		}
		writeError(w, status, osbErr)
		return
	}

	// Map sentinel errors.
	switch {
	case errors.Is(err, osbapi.ErrAsyncRequired):
		writeError(w, http.StatusUnprocessableEntity, &osbapi.OSBError{
			ErrorCode:   osbapi.ErrorCodeAsyncRequired,
			Description: err.Error(),
		})

	case errors.Is(err, osbapi.ErrConcurrencyError):
		writeError(w, http.StatusUnprocessableEntity, &osbapi.OSBError{
			ErrorCode:   osbapi.ErrorCodeConcurrencyError,
			Description: err.Error(),
		})

	case errors.Is(err, osbapi.ErrInstanceNotFound), errors.Is(err, osbapi.ErrBindingNotFound):
		writeError(w, http.StatusNotFound, &osbapi.OSBError{
			Description: err.Error(),
		})

	case errors.Is(err, osbapi.ErrGoneError):
		writeError(w, http.StatusGone, &osbapi.OSBError{
			Description: err.Error(),
		})

	case errors.Is(err, osbapi.ErrRequiresApp):
		writeError(w, http.StatusUnprocessableEntity, &osbapi.OSBError{
			ErrorCode:   osbapi.ErrorCodeRequiresApp,
			Description: err.Error(),
		})

	case errors.Is(err, osbapi.ErrMaintenanceInfoConflict):
		writeError(w, http.StatusUnprocessableEntity, &osbapi.OSBError{
			ErrorCode:   osbapi.ErrorCodeMaintenanceInfoConflict,
			Description: err.Error(),
		})

	case errors.Is(err, osbapi.ErrInstanceAlreadyExists):
		writeError(w, http.StatusConflict, &osbapi.OSBError{
			Description: err.Error(),
		})

	case errors.Is(err, osbapi.ErrBadRequest):
		writeError(w, http.StatusBadRequest, &osbapi.OSBError{
			Description: err.Error(),
		})

	default:
		writeError(w, http.StatusInternalServerError, &osbapi.OSBError{
			Description: "internal server error",
		})
	}
}

// ---------------------------------------------------------------------------
// JSON helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, osbErr *osbapi.OSBError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(osbErr)
}
