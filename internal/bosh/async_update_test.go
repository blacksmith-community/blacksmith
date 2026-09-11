package bosh

import (
	"net/http"
	"net/http/httptest"
	"testing"

	boshdirector "github.com/cloudfoundry/bosh-cli/v7/director"
)

func noFollowClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func newTestAdapter(endpoint string) *DirectorAdapter {
	return &DirectorAdapter{
		log:            &noOpLogger{},
		rawEndpoint:    endpoint,
		rawClient:      noFollowClient(),
		authAdjustment: boshdirector.NewAuthRequestAdjustment(nil, "", ""), // no-op auth for tests
	}
}

func TestParseTaskIDFromLocation(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"/tasks/1234", 1234, false},
		{"/tasks/1234/", 1234, false},
		{"https://director:25555/tasks/42", 42, false},
		{"/tasks/7?foo=bar", 7, false},
		{"", 0, true},
		{"/deployments/foo", 0, true},
	}

	for _, c := range cases {
		got, err := parseTaskIDFromLocation(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseTaskIDFromLocation(%q): expected error, got %d", c.in, got)
			}

			continue
		}

		if err != nil || got != c.want {
			t.Errorf("parseTaskIDFromLocation(%q) = %d, %v; want %d", c.in, got, err, c.want)
		}
	}
}

// The BOSH Director returns 303 See Other for a deployment-update task; make sure we accept
// any 3xx-with-Location (this is the case the first live test caught).
func TestUpdateDeploymentAsync_AcceptsRedirectStatuses(t *testing.T) {
	for _, status := range []int{http.StatusSeeOther, http.StatusFound, http.StatusMovedPermanently} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/deployments" {
				w.WriteHeader(http.StatusBadRequest)

				return
			}

			w.Header().Set("Location", "/tasks/99")
			w.WriteHeader(status)
		}))

		task, err := newTestAdapter(srv.URL).UpdateDeploymentAsync("dep", "name: dep")
		srv.Close()

		if err != nil {
			t.Fatalf("status %d: unexpected error: %v", status, err)
		}

		if task == nil || task.ID != 99 {
			t.Fatalf("status %d: got task %+v, want ID 99", status, task)
		}
	}
}

func TestUpdateDeploymentAsync_ErrorsWithoutLocation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // 200, no Location header
	}))
	defer srv.Close()

	if _, err := newTestAdapter(srv.URL).UpdateDeploymentAsync("dep", "name: dep"); err == nil {
		t.Fatal("expected error for 200 without Location, got nil")
	}
}
