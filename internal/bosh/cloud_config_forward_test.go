package bosh_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"blacksmith/internal/bosh"
)

var errListCloudConfigs = errors.New("the list failed")

// listingDirector answers ListCloudConfigs and nothing else.
type listingDirector struct {
	bosh.Director

	entries []bosh.CloudConfigEntry
	err     error
}

func (l *listingDirector) ListCloudConfigs(_ context.Context) ([]bosh.CloudConfigEntry, error) {
	return l.entries, l.err
}

// The pooled and batch directors forward the cloud config list unchanged, and
// forward a failure too.
func TestCloudConfigListForwardsThroughThePooledAndBatchDirectors(t *testing.T) {
	t.Parallel()

	entries := []bosh.CloudConfigEntry{{Name: planDefaultName, Content: "azs: []\n"}, {Name: "extra", Content: "vm_types: []\n"}}

	wrap := map[string]func(bosh.Director) bosh.Director{
		"pooled": func(inner bosh.Director) bosh.Director {
			return bosh.NewPooledDirector(inner, 1, time.Second, nil)
		},
		"batch": func(inner bosh.Director) bosh.Director {
			return bosh.NewBatchDirector(inner, 1, time.Second, nil)
		},
	}

	for name, build := range wrap {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := build(&listingDirector{entries: entries}).ListCloudConfigs(t.Context())
			if err != nil {
				t.Fatalf("ListCloudConfigs failed: %v", err)
			}

			if !reflect.DeepEqual(got, entries) {
				t.Errorf("expected %v, got %v", entries, got)
			}

			_, err = build(&listingDirector{err: errListCloudConfigs}).ListCloudConfigs(t.Context())
			if !errors.Is(err, errListCloudConfigs) {
				t.Errorf("expected the failure to pass through, got %v", err)
			}
		})
	}
}
