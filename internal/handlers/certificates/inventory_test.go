package certificates_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/certinventory"
	"blacksmith/internal/handlers/certificates"
	"blacksmith/pkg/logger"
)

type fakeInventory struct {
	enabled     bool
	filter      certinventory.Filter
	scanIDs     []string
	scanID      string
	scanRunning bool
	scanErr     error
	saved       *certinventory.Settings
	saveErr     error
	instanceRef string
}

func (f *fakeInventory) Enabled() bool { return f.enabled }

func (f *fakeInventory) Response(_ context.Context, flt certinventory.Filter) certinventory.InventoryResponse {
	f.filter = flt

	return certinventory.InventoryResponse{
		Certificates: []certinventory.Certificate{
			{ID: "a", Variable: "crt", Rotation: certinventory.Rotatability{Allowed: true}},
			{ID: "b", Variable: "ca"},
		},
	}
}

func (f *fakeInventory) Item(id string) (certinventory.ItemDetail, bool) {
	if id != "known" {
		return certinventory.ItemDetail{}, false
	}

	return certinventory.ItemDetail{Certificate: certinventory.Certificate{ID: "known"}, PEM: "pem-text"}, true
}

func (f *fakeInventory) RequestScan(ids []string) (string, bool) {
	f.scanIDs = ids

	return f.scanID, f.scanRunning
}

func (f *fakeInventory) ScanInstance(_ context.Context, id string) (certinventory.InstanceScan, error) {
	f.instanceRef = id
	if f.scanErr != nil {
		return certinventory.InstanceScan{}, f.scanErr
	}

	return certinventory.InstanceScan{
		Ref:       certinventory.InstanceRef{ID: id, Name: "n"},
		ScannedAt: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC),
		Err:       "director timed out",
	}, nil
}

func (f *fakeInventory) Settings() certinventory.Settings { return certinventory.DefaultSettings() }

func (f *fakeInventory) SaveSettings(_ context.Context, s certinventory.Settings) error {
	if f.saveErr != nil {
		return f.saveErr
	}

	f.saved = &s

	return nil
}

func do(t *testing.T, svc *fakeInventory, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	h := certificates.NewInventoryHandler(svc, logger.NewNoOpLogger())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	h.ServeHTTP(rec, req)

	return rec
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
		Code    string `json:"code"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}

	if body.Success || body.Error == "" {
		t.Errorf("error body %q lacks success=false or a message", rec.Body.String())
	}

	return body.Code
}

func TestInventoryRoutes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		svc    *fakeInventory
		method string
		target string
		body   string
		status int
		code   string
	}{
		{"inventory", &fakeInventory{enabled: true}, "GET", "/b/certificates/inventory?expiry=critical", "", 200, ""},
		{"bad filter", &fakeInventory{enabled: true}, "GET", "/b/certificates/inventory?expiry=soon", "", 400, "invalid_filter"},
		{"unknown key", &fakeInventory{enabled: true}, "GET", "/b/certificates/inventory?colour=red", "", 400, "invalid_filter"},
		{"item", &fakeInventory{enabled: true}, "GET", "/b/certificates/items/known", "", 200, ""},
		{"unknown item", &fakeInventory{enabled: true}, "GET", "/b/certificates/items/nope", "", 404, "instance_not_found"},
		{"settings get", &fakeInventory{enabled: true}, "GET", "/b/certificates/settings", "", 200, ""},
		{"settings put", &fakeInventory{enabled: true}, "PUT", "/b/certificates/settings", `{"warning_days":20,"critical_days":5}`, 200, ""},
		{"settings invalid", &fakeInventory{enabled: true}, "PUT", "/b/certificates/settings", `{"warning_days":7,"critical_days":7}`, 400, "invalid_settings"},
		{"settings unknown field", &fakeInventory{enabled: true}, "PUT", "/b/certificates/settings", `{"bogus":1}`, 400, "invalid_settings"},
		{"settings not json", &fakeInventory{enabled: true}, "PUT", "/b/certificates/settings", `nope`, 400, "invalid_settings"},
		{"settings store down", &fakeInventory{enabled: true, saveErr: errors.New("vault down")}, "PUT", "/b/certificates/settings", `{}`, 500, "internal_error"},
		{"scan", &fakeInventory{enabled: true, scanID: "s1"}, "POST", "/b/certificates/scan", "", 202, ""},
		{"scan already running", &fakeInventory{enabled: true, scanID: "s1", scanRunning: true}, "POST", "/b/certificates/scan", "", 202, ""},
		{"scan stopped", &fakeInventory{enabled: true}, "POST", "/b/certificates/scan", "", 500, "internal_error"},
		{"scan bad body", &fakeInventory{enabled: true, scanID: "s1"}, "POST", "/b/certificates/scan", `{`, 400, "invalid_filter"},
		{"instance", &fakeInventory{enabled: true}, "GET", "/b/certificates/instances/i1", "", 200, ""},
		{"instance unknown", &fakeInventory{enabled: true, scanErr: certinventory.ErrUnknownInstance}, "GET", "/b/certificates/instances/zz", "", 404, "instance_not_found"},
		{"disabled", &fakeInventory{}, "GET", "/b/certificates/inventory", "", 403, "inventory_disabled"},
		{"disabled scan", &fakeInventory{}, "POST", "/b/certificates/scan", "", 403, "inventory_disabled"},
		{"unknown path", &fakeInventory{enabled: true}, "GET", "/b/certificates/rotation/preview", "", 404, "not_found"},
		{"wrong method", &fakeInventory{enabled: true}, "DELETE", "/b/certificates/inventory", "", 404, "not_found"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := do(t, tc.svc, tc.method, tc.target, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d, body %s", rec.Code, tc.status, rec.Body.String())
			}

			if tc.code != "" {
				if got := errCode(t, rec); got != tc.code {
					t.Errorf("code %q, want %q", got, tc.code)
				}
			}
		})
	}
}

func TestInventoryPassesFilter(t *testing.T) {
	t.Parallel()

	svc := &fakeInventory{enabled: true}
	do(t, svc, "GET", "/b/certificates/inventory?expiry=critical&expiry=warning&attention=true", "")

	if len(svc.filter.Expiry) != 2 || !svc.filter.Attention {
		t.Errorf("filter %+v", svc.filter)
	}
}

func TestInventoryScanBody(t *testing.T) {
	t.Parallel()

	for _, running := range []bool{false, true} {
		checkScanBody(t, running)
	}
}

func checkScanBody(t *testing.T, running bool) {
	t.Helper()

	svc := &fakeInventory{enabled: true, scanID: "s1", scanRunning: running}
	rec := do(t, svc, "POST", "/b/certificates/scan", `{"instance_ids":["i1","i2"]}`)

	if rec.Code != 202 {
		t.Fatalf("running=%v gave status %d, want 202", running, rec.Code)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	if body["scan_id"] != "s1" || body["state"] != "running" || len(svc.scanIDs) != 2 {
		t.Errorf("body %v, ids %v", body, svc.scanIDs)
	}
}

func TestInventoryInstanceReportsPartialScan(t *testing.T) {
	t.Parallel()

	rec := do(t, &fakeInventory{enabled: true}, "GET", "/b/certificates/instances/i1", "")

	var body struct {
		Instance struct {
			ID string `json:"id"`
		} `json:"instance"`
		Scan struct {
			Error string `json:"error"`
		} `json:"scan"`
		RotatableVariables []string `json:"rotatable_variables"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	if body.Instance.ID != "i1" || body.Scan.Error != "director timed out" {
		t.Errorf("body %s", rec.Body.String())
	}

	if len(body.RotatableVariables) != 1 || body.RotatableVariables[0] != "crt" {
		t.Errorf("rotatable %v", body.RotatableVariables)
	}
}

func TestInventorySettingsSavedOverDefaults(t *testing.T) {
	t.Parallel()

	svc := &fakeInventory{enabled: true}
	do(t, svc, "PUT", "/b/certificates/settings", `{"warning_days":45}`)

	want := certinventory.DefaultSettings()
	want.WarningDays = 45

	if svc.saved == nil || *svc.saved != want {
		t.Errorf("saved %+v, want %+v", svc.saved, want)
	}
}

func TestInventoryItemServesPEM(t *testing.T) {
	t.Parallel()

	rec := do(t, &fakeInventory{enabled: true}, "GET", "/b/certificates/items/known", "")
	if !strings.Contains(rec.Body.String(), `"pem":"pem-text"`) {
		t.Errorf("body %s", rec.Body.String())
	}
}
