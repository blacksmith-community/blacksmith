package services_test

import (
	"path/filepath"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/internal/services"
)

// TestCatalogParity records the service every rendered forge tree loads as,
// with each plan's merged manifest and credentials.
//
//nolint:paralleltest // kept serial for now, though it shares no process-wide state
func TestCatalogParity(t *testing.T) {
	for _, fixture := range parity.Fixtures(t) {
		t.Run(fixture.Name(), func(t *testing.T) {
			service, err := services.ReadService(fixture.ServicesDir())
			if err != nil {
				t.Fatalf("read %s: %s", fixture.Name(), err)
			}

			golden := filepath.Join("testdata", "parity", fixture.Forge, fixture.Variant+".dump")
			parity.Golden(t, golden, parity.Relative(parity.Dump(service)))
		})
	}
}

// TestCatalogParityReadsEveryFixtureTree proves the fixtures hold the plans
// each forge ships, so a missing plan cannot pass as an empty golden.
//
//nolint:paralleltest // kept serial for now, though it shares no process-wide state
func TestCatalogParityReadsEveryFixtureTree(t *testing.T) {
	want := map[string]int{"kubernetes": 1, "mariadb": 1, "postgresql": 2, "rabbitmq": 1, "redis": 3, "valkey": 2}

	for _, fixture := range parity.Fixtures(t) {
		service, err := services.ReadService(fixture.ServicesDir())
		if err != nil {
			t.Fatalf("read %s: %s", fixture.Name(), err)
		}

		if len(service.Plans) != want[fixture.Forge] {
			t.Errorf("%s loads %d plans, want %d", fixture.Name(), len(service.Plans), want[fixture.Forge])
		}
	}
}
