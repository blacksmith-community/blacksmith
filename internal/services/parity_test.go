package services_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"blacksmith/internal/parity"
	"blacksmith/internal/services"
	"blacksmith/pkg/utils"
)

// catalogView is a services.Service with each plan's manifest and
// credentials back in the interface-keyed form that yaml.v2 and spruce
// produce. The dump of an empty map names its type, so the view keeps the
// golden recording what the merge produced rather than the key type the
// catalog stores. A key that DeinterfaceMap turned into a string still
// shows, because the dump labels a key that is not a string with its type.
// The fields and their order match services.Service, so the dump's paths
// are the ones the service itself gives.
type catalogView struct {
	ID          string
	Name        string
	Type        string
	Description string
	Bindable    bool
	Tags        []string
	Limit       int
	Plans       []planView
}

// planView is a services.Plan as catalogView records it.
type planView struct {
	ID              string
	Name            string
	Description     string
	Limit           int
	Type            string
	Manifest        map[interface{}]interface{}
	Credentials     map[interface{}]interface{}
	InitScriptPath  string
	ManifestPath    string
	CredentialsPath string
	Service         *services.Service
}

func viewCatalog(service services.Service) catalogView {
	view := catalogView{
		ID:          service.ID,
		Name:        service.Name,
		Type:        service.Type,
		Description: service.Description,
		Bindable:    service.Bindable,
		Tags:        service.Tags,
		Limit:       service.Limit,
	}

	if service.Plans != nil {
		view.Plans = make([]planView, 0, len(service.Plans))
	}

	for _, plan := range service.Plans {
		view.Plans = append(view.Plans, planView{
			ID:              plan.ID,
			Name:            plan.Name,
			Description:     plan.Description,
			Limit:           plan.Limit,
			Type:            plan.Type,
			Manifest:        utils.InterfaceMap(plan.Manifest),
			Credentials:     utils.InterfaceMap(plan.Credentials),
			InitScriptPath:  plan.InitScriptPath,
			ManifestPath:    plan.ManifestPath,
			CredentialsPath: plan.CredentialsPath,
			Service:         plan.Service,
		})
	}

	return view
}

// TestCatalogViewMirrorsTheCatalogTypes fails when a field is added to,
// removed from, or reordered in services.Service or services.Plan, so the
// view cannot drop a field from the catalog golden unnoticed.
func TestCatalogViewMirrorsTheCatalogTypes(t *testing.T) {
	t.Parallel()

	mirrors := func(original, view reflect.Type, converted ...string) {
		if original.NumField() != view.NumField() {
			t.Fatalf("%s has %d fields and its view has %d", original, original.NumField(), view.NumField())
		}

		for index := range original.NumField() {
			field, mirror := original.Field(index), view.Field(index)
			if field.Name != mirror.Name {
				t.Errorf("%s field %d is %s, and its view has %s", original, index, field.Name, mirror.Name)
			}

			isConverted := false

			for _, name := range converted {
				if field.Name == name {
					isConverted = true
				}
			}

			if !isConverted && field.Name != "Plans" && field.Type != mirror.Type {
				t.Errorf("%s.%s is %s, and its view has %s", original, field.Name, field.Type, mirror.Type)
			}
		}
	}

	mirrors(reflect.TypeOf(services.Service{}), reflect.TypeOf(catalogView{}))
	mirrors(reflect.TypeOf(services.Plan{}), reflect.TypeOf(planView{}), "Manifest", "Credentials")
}

// TestCatalogParity records the service every rendered forge tree loads as,
// with each plan's merged manifest and credentials.
//
//nolint:paralleltest // merging plan files uses spruce, which keeps global state
func TestCatalogParity(t *testing.T) {
	for _, fixture := range parity.Fixtures(t) {
		t.Run(fixture.Name(), func(t *testing.T) {
			service, err := services.ReadService(fixture.ServicesDir())
			if err != nil {
				t.Fatalf("read %s: %s", fixture.Name(), err)
			}

			golden := filepath.Join("testdata", "parity", fixture.Forge, fixture.Variant+".dump")
			parity.Golden(t, golden, parity.Relative(parity.Dump(viewCatalog(service))))
		})
	}
}

// TestCatalogParityReadsEveryFixtureTree proves the fixtures hold the plans
// each forge ships, so a missing plan cannot pass as an empty golden.
//
//nolint:paralleltest // merging plan files uses spruce, which keeps global state
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
