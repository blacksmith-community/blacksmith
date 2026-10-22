package certificates

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/certinventory"
	"blacksmith/internal/upgrade"
	"blacksmith/pkg/http/response"
	"blacksmith/pkg/logger"
)

const (
	inventoryPrefix    = "/b/certificates/"
	maxInventoryBody   = 64 << 10
	scanStateRunning   = "running"
	inventoryDisabled  = "Certificate inventory is off on this broker (certificates.inventory.enabled)."
	scanUnavailableMsg = "The certificate scanner is not running."
	noPreviewerMsg     = "Rotation previews are not available on this broker."
	badPreviewBodyMsg  = "The preview body must be JSON with a mode and a list of selections."
)

// InventoryService is everything the inventory API reads from and asks of
// the certificate inventory.
type InventoryService interface {
	Response(ctx context.Context, f certinventory.Filter) certinventory.InventoryResponse
	Item(id string) (certinventory.ItemDetail, bool)
	RequestScan(ids []string) (string, bool)
	ScanInstance(ctx context.Context, id string) (certinventory.InstanceScan, error)
	Settings() certinventory.Settings
	SaveSettings(ctx context.Context, s certinventory.Settings) error
	Enabled() bool
}

// RotationPreviewer builds rotation previews. The upgrade manager is one.
type RotationPreviewer interface {
	PreviewRotation(ctx context.Context, req upgrade.PreviewRequest) (*upgrade.Preview, error)
}

// InventoryHandler serves the certificate inventory API and the rotation
// preview under /b/certificates/.
type InventoryHandler struct {
	svc       InventoryService
	previewer RotationPreviewer
	log       logger.Logger
}

// NewInventoryHandler returns a handler for the inventory API. A nil service
// answers every request as if the inventory were switched off, and a nil
// previewer answers a preview request with an internal error.
func NewInventoryHandler(svc InventoryService, previewer RotationPreviewer, log logger.Logger) *InventoryHandler {
	if log == nil {
		log = logger.NewNoOpLogger()
	}

	return &InventoryHandler{svc: svc, previewer: previewer, log: log}
}

// ServeHTTP resolves the route first, so a path that names no route answers
// 404 even when the inventory is off. Only a known route answers
// inventory_disabled.
func (h *InventoryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, inventoryPrefix), "/")
	segs := strings.Split(path, "/")

	var route func()

	switch {
	case r.Method == http.MethodGet && path == "inventory":
		route = func() { h.inventory(w, r) }
	case r.Method == http.MethodPost && path == "scan":
		route = func() { h.scan(w, r) }
	case r.Method == http.MethodPost && path == "rotation/preview":
		route = func() { h.preview(w, r) }
	case r.Method == http.MethodGet && len(segs) == 2 && segs[0] == "instances" && segs[1] != "":
		route = func() { h.instance(w, r, segs[1]) }
	case r.Method == http.MethodGet && len(segs) == 2 && segs[0] == "items" && segs[1] != "":
		route = func() { h.item(w, segs[1]) }
	case r.Method == http.MethodGet && path == "settings":
		route = func() { response.JSON(w, http.StatusOK, h.svc.Settings()) }
	case r.Method == http.MethodPut && path == "settings":
		route = func() { h.saveSettings(w, r) }
	default:
		apierr.Write(w, apierr.New(http.StatusNotFound, apierr.CodeNotFound, "No certificate endpoint answers "+r.Method+" "+r.URL.Path+"."), http.StatusNotFound)

		return
	}

	if h.svc == nil || !h.svc.Enabled() {
		apierr.Write(w, apierr.New(http.StatusForbidden, apierr.CodeInventoryDisabled, inventoryDisabled), http.StatusForbidden)

		return
	}

	route()
}

func (h *InventoryHandler) inventory(w http.ResponseWriter, r *http.Request) {
	filter, err := certinventory.ParseFilter(r.URL.Query())
	if err != nil {
		apierr.Write(w, err, http.StatusBadRequest)

		return
	}

	response.JSON(w, http.StatusOK, h.svc.Response(r.Context(), filter))
}

func (h *InventoryHandler) item(w http.ResponseWriter, id string) {
	detail, ok := h.svc.Item(id)
	if !ok {
		apierr.Write(w, apierr.New(http.StatusNotFound, apierr.CodeInstanceNotFound, "No certificate has the ID "+id+"."), http.StatusNotFound)

		return
	}

	response.JSON(w, http.StatusOK, detail)
}

func (h *InventoryHandler) scan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InstanceIDs []string `json:"instance_ids"`
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxInventoryBody))
	if err != nil {
		apierr.Write(w, apierr.New(http.StatusBadRequest, apierr.CodeInvalidFilter, "The request body could not be read."), http.StatusBadRequest)

		return
	}

	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			apierr.Write(w, apierr.New(http.StatusBadRequest, apierr.CodeInvalidFilter, "The scan request body must be JSON with an optional instance_ids list."), http.StatusBadRequest)

			return
		}
	}

	// The second value says a scan was already running, and the caller
	// gets that scan's ID. Only an empty ID means no scan can start.
	id, _ := h.svc.RequestScan(req.InstanceIDs)
	if id == "" {
		apierr.Write(w, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, scanUnavailableMsg), http.StatusInternalServerError)

		return
	}

	response.JSON(w, http.StatusAccepted, map[string]string{"scan_id": id, "state": scanStateRunning})
}

// preview answers a rotation preview. Every refusal comes from the manager
// as an API error with its spec 9.4 status and code.
func (h *InventoryHandler) preview(w http.ResponseWriter, r *http.Request) {
	if h.previewer == nil {
		apierr.Write(w, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, noPreviewerMsg), http.StatusInternalServerError)

		return
	}

	var req upgrade.PreviewRequest

	err := json.NewDecoder(io.LimitReader(r.Body, maxInventoryBody)).Decode(&req)
	if err != nil {
		apierr.Write(w, apierr.New(http.StatusBadRequest, apierr.CodeNoSelections, badPreviewBodyMsg), http.StatusBadRequest)

		return
	}

	preview, err := h.previewer.PreviewRotation(r.Context(), req)
	if err != nil {
		var apiErr *apierr.Error
		if !errors.As(err, &apiErr) {
			h.log.Errorf("certificate rotation preview failed: %v", err)
		}

		apierr.Write(w, err, http.StatusInternalServerError)

		return
	}

	response.JSON(w, http.StatusOK, preview)
}

type instanceBody struct {
	Instance           instanceInfo                   `json:"instance"`
	Scan               instanceScanInfo               `json:"scan"`
	Certificates       []certinventory.Certificate    `json:"certificates"`
	RotatableVariables []string                       `json:"rotatable_variables"`
	Summary            certinventory.Summary          `json:"summary"`
	Capabilities       certinventory.CapabilitiesView `json:"capabilities"`
}

type instanceInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Deployment  string `json:"deployment,omitempty"`
	ServiceName string `json:"service_name,omitempty"`
	PlanName    string `json:"plan_name,omitempty"`
}

type instanceScanInfo struct {
	ScannedAt     time.Time `json:"scanned_at"`
	LastSuccessAt time.Time `json:"last_success_at"`
	Stale         bool      `json:"stale"`
	Error         string    `json:"error,omitempty"`
}

func (h *InventoryHandler) instance(w http.ResponseWriter, r *http.Request, id string) {
	scan, err := h.svc.ScanInstance(r.Context(), id)
	if err != nil {
		if errors.Is(err, certinventory.ErrUnknownInstance) {
			apierr.Write(w, err, http.StatusNotFound)

			return
		}

		h.log.Errorf("certificate scan of instance %s failed: %v", id, err)
		apierr.Write(w, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "The instance could not be scanned."), http.StatusInternalServerError)

		return
	}

	inv := h.svc.Response(r.Context(), certinventory.Filter{Instance: []string{id}})

	body := instanceBody{
		Instance: instanceInfo{
			ID: scan.Ref.ID, Name: scan.Ref.Name, Deployment: scan.Ref.Deployment,
			ServiceName: scan.Ref.ServiceName, PlanName: scan.Ref.PlanName,
		},
		Scan: instanceScanInfo{
			ScannedAt: scan.ScannedAt, LastSuccessAt: scan.LastSuccessAt, Stale: scan.Stale, Error: scan.Err,
		},
		Certificates:       inv.Certificates,
		RotatableVariables: []string{},
		Summary:            inv.Filtered,
		Capabilities:       inv.Capabilities,
	}

	if body.Certificates == nil {
		body.Certificates = []certinventory.Certificate{}
	}

	for i := range inv.Certificates {
		if c := &inv.Certificates[i]; c.Rotation.Allowed {
			body.RotatableVariables = append(body.RotatableVariables, c.Variable)
		}
	}

	response.JSON(w, http.StatusOK, body)
}

func (h *InventoryHandler) saveSettings(w http.ResponseWriter, r *http.Request) {
	settings := certinventory.DefaultSettings()

	dec := json.NewDecoder(io.LimitReader(r.Body, maxInventoryBody))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&settings); err != nil {
		apierr.Write(w, apierr.New(http.StatusBadRequest, apierr.CodeInvalidSettings, "The settings body must be JSON with known setting names."), http.StatusBadRequest)

		return
	}

	if err := settings.Validate(); err != nil {
		apierr.Write(w, apierr.New(http.StatusBadRequest, apierr.CodeInvalidSettings, err.Error()), http.StatusBadRequest)

		return
	}

	if err := h.svc.SaveSettings(r.Context(), settings); err != nil {
		var apiErr *apierr.Error
		if errors.As(err, &apiErr) {
			apierr.Write(w, err, http.StatusInternalServerError)

			return
		}

		h.log.Errorf("saving certificate settings failed: %v", err)
		apierr.Write(w, apierr.New(http.StatusInternalServerError, apierr.CodeInternal, "The settings could not be saved."), http.StatusInternalServerError)

		return
	}

	response.JSON(w, http.StatusOK, settings)
}
