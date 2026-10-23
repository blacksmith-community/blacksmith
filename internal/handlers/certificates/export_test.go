package certificates_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"blacksmith/internal/certinventory"
	"blacksmith/internal/handlers/certificates"
	"blacksmith/pkg/logger"
)

// filteringInventory answers the expiry filter the way the real service
// does, so an export and the table can be compared.
type filteringInventory struct{ fakeInventory }

func (f *filteringInventory) Response(_ context.Context, flt certinventory.Filter) certinventory.InventoryResponse {
	all := []certinventory.Certificate{
		{ID: "a", Expiry: certinventory.ExpiryCritical},
		{ID: "b", Expiry: certinventory.ExpiryOK},
		{ID: "c", Expiry: certinventory.ExpiryCritical},
	}

	var out []certinventory.Certificate

	for i := range all {
		if len(flt.Expiry) == 0 || all[i].Expiry == certinventory.ExpiryStatus(flt.Expiry[0]) {
			out = append(out, all[i])
		}
	}

	return certinventory.InventoryResponse{Certificates: out}
}

func serve(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()

	h := certificates.NewInventoryHandler(&filteringInventory{fakeInventory{enabled: true}}, nil, logger.NewNoOpLogger())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", target, nil))

	return rec
}

func TestExportMatchesFilter(t *testing.T) {
	t.Parallel()

	rec := serve(t, "/b/certificates/inventory?format=csv&expiry=critical")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}

	var exported []string
	for _, r := range rows[1:] {
		exported = append(exported, r[0])
	}

	// The table answers the same filter, and the export has its rows.
	table := serve(t, "/b/certificates/inventory?expiry=critical")

	var body certinventory.InventoryResponse
	if err := json.Unmarshal(table.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	var shown []string
	for i := range body.Certificates {
		shown = append(shown, body.Certificates[i].ID)
	}

	all := serve(t, "/b/certificates/inventory")

	var everything certinventory.InventoryResponse
	if err := json.Unmarshal(all.Body.Bytes(), &everything); err != nil {
		t.Fatal(err)
	}

	if len(shown) == 0 || len(shown) >= len(everything.Certificates) {
		t.Fatalf("the filter should keep some rows and drop some, kept %v of %d", shown, len(everything.Certificates))
	}

	if strings.Join(exported, ",") != strings.Join(shown, ",") {
		t.Errorf("export rows %v differ from the table rows %v", exported, shown)
	}

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content type %q", ct)
	}

	cd := rec.Header().Get("Content-Disposition")
	if !regexp.MustCompile(`^attachment; filename="certificates-\d{8}T\d{6}Z\.csv"$`).MatchString(cd) {
		t.Errorf("content disposition %q", cd)
	}

	js := serve(t, "/b/certificates/inventory?format=json")
	if js.Code != 200 || !strings.Contains(js.Header().Get("Content-Disposition"), ".json\"") {
		t.Errorf("json export: %d %q", js.Code, js.Header().Get("Content-Disposition"))
	}
}

func TestExportRejectsUnknownKey(t *testing.T) {
	t.Parallel()

	rec := serve(t, "/b/certificates/inventory?format=csv&colour=red")
	if rec.Code != 400 || errCode(t, rec) != "invalid_filter" {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}

	rec = serve(t, "/b/certificates/inventory?format=xml")
	if rec.Code != 400 || errCode(t, rec) != "invalid_filter" {
		t.Errorf("bad format: status %d body %s", rec.Code, rec.Body.String())
	}
}
