package certinventory_test

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"blacksmith/internal/certinventory"
)

func exportCert(cn string) certinventory.Certificate {
	notAfter := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	days := -2

	return certinventory.Certificate{
		ID: "id1", Key: "/bosh/dep/cert", Variable: "cert", Kind: certinventory.KindLeaf,
		CommonName: cn, Issuer: "Test CA",
		Owners: []certinventory.Owner{{
			Kind: certinventory.OwnerInstance, InstanceID: "inst-1", InstanceName: "redis-1",
			ServiceName: "redis", PlanName: "small",
		}},
		AffectedInstances: 1, EffectiveNotAfter: &notAfter, DaysLeft: &days,
		Expiry: certinventory.ExpiryExpired, Health: certinventory.HealthError, Attention: true,
		Findings: []certinventory.Finding{{Code: "expired"}, {Code: "short_chain"}},
		Coverage: certinventory.Coverage{ExpectedListeners: 3, ObservedListeners: 2},
	}
}

func readCSV(t *testing.T, certs []certinventory.Certificate) [][]string {
	t.Helper()

	var buf bytes.Buffer
	if err := certinventory.WriteCSV(&buf, certs); err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}

	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("output is not CSV: %v", err)
	}

	return rows
}

func TestWriteCSVHeader(t *testing.T) {
	t.Parallel()

	rows := readCSV(t, []certinventory.Certificate{exportCert("plain")})
	want := "id,key,variable,kind,common_name,issuer,owner_kind,owner,service,plan,instance_id,affected_instances,effective_not_after,days_left,expiry,health,attention,findings,coverage"

	if got := strings.Join(rows[0], ","); got != want {
		t.Errorf("header %q, want %q", got, want)
	}

	row := rows[1]
	if row[18] != "2/3" || row[17] != "expired;short_chain" || row[12] != "2026-10-21T12:00:00Z" {
		t.Errorf("coverage, findings, or date wrong in %v", row)
	}
}

func TestWriteCSVNeutralisesFormulas(t *testing.T) {
	t.Parallel()

	for _, cn := range []string{`=HYPERLINK("x")`, "+1", "-1", "@SUM(A1)", "\tx", "\rx"} {
		rows := readCSV(t, []certinventory.Certificate{exportCert(cn)})
		if got := rows[1][4]; got != "'"+cn {
			t.Errorf("common name %q written as %q, want a leading apostrophe", cn, got)
		}
	}

	rows := readCSV(t, []certinventory.Certificate{exportCert("fine")})
	if rows[1][4] != "fine" {
		t.Errorf("a harmless name was changed to %q", rows[1][4])
	}
}

func TestWriteCSVLeavesNumbers(t *testing.T) {
	t.Parallel()

	rows := readCSV(t, []certinventory.Certificate{exportCert("x")})
	if rows[1][13] != "-2" {
		t.Errorf("days_left written as %q, want -2", rows[1][13])
	}

	if rows[1][11] != "1" || rows[1][16] != "true" {
		t.Errorf("affected_instances or attention changed: %v", rows[1])
	}
}

func TestWriteJSON(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	resp := certinventory.InventoryResponse{Certificates: []certinventory.Certificate{exportCert("x")}}
	if err := certinventory.WriteJSON(&buf, resp); err != nil {
		t.Fatal(err)
	}

	var back certinventory.InventoryResponse
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil || len(back.Certificates) != 1 {
		t.Errorf("round trip failed: %v, %d certificates", err, len(back.Certificates))
	}
}
