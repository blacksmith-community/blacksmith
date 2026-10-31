package cf

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
)

const registrationPassword = "cf-admin-pw-93kq"

// memoryVault keeps registrations in memory, the way Vault keeps the
// posted record whole.
type memoryVault struct {
	interfaces.Vault

	mu            sync.Mutex
	registrations map[string]map[string]interface{}
}

func (v *memoryVault) SaveCFRegistration(_ context.Context, registration map[string]interface{}) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	id, _ := registration["id"].(string)
	v.registrations[id] = registration

	return nil
}

func (v *memoryVault) GetCFRegistration(_ context.Context, registrationID string, out interface{}) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	registration, ok := v.registrations[registrationID]
	if !ok {
		return false, nil
	}

	raw, err := json.Marshal(registration)
	if err != nil {
		return false, err
	}

	return true, json.Unmarshal(raw, out)
}

func (v *memoryVault) ListCFRegistrations(context.Context) ([]map[string]interface{}, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	registrations := make([]map[string]interface{}, 0, len(v.registrations))
	for _, registration := range v.registrations {
		registrations = append(registrations, registration)
	}

	return registrations, nil
}

func serveRegistration(t *testing.T, handler *Handler, method, path, body string) string {
	t.Helper()

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("%s %s answered %d: %s", method, path, recorder.Code, recorder.Body.String())
	}

	return recorder.Body.String()
}

// The CF admin password goes into a registration once, and no answer
// about the registration ever gives it back, under any spelling of the
// key.
func TestRegistrationAnswersNeverContainThePassword(t *testing.T) {
	t.Parallel()

	vault := &memoryVault{registrations: map[string]map[string]interface{}{}}
	handler := NewHandler(logger.Get().Named("cf-registration-password-test"), nil, nil, vault)

	created := `{"id":"reg-1","name":"lab-cf","api_url":"https://api.cf.example.com","username":"admin","password":"` + registrationPassword + `","broker_name":"blacksmith"}`
	updated := `{"id":"reg-1","name":"lab-cf","api_url":"https://api.cf.example.com","username":"admin","Password":"` + registrationPassword + `","PASSWORD":"` + registrationPassword + `"}`

	answers := map[string]string{
		"create": serveRegistration(t, handler, http.MethodPost, "/b/cf/registrations", created),
		"list":   serveRegistration(t, handler, http.MethodGet, "/b/cf/registrations", ""),
		"get":    serveRegistration(t, handler, http.MethodGet, "/b/cf/registrations/reg-1", ""),
		"update": serveRegistration(t, handler, http.MethodPut, "/b/cf/registrations/reg-1", updated),
	}
	answers["list after update"] = serveRegistration(t, handler, http.MethodGet, "/b/cf/registrations", "")
	answers["get after update"] = serveRegistration(t, handler, http.MethodGet, "/b/cf/registrations/reg-1", "")

	for name, answer := range answers {
		if strings.Contains(answer, registrationPassword) {
			t.Errorf("the %s answer contains the password: %s", name, answer)
		}

		if strings.Contains(strings.ToLower(answer), `"password"`) {
			t.Errorf("the %s answer has a password field: %s", name, answer)
		}

		if !strings.Contains(answer, "lab-cf") {
			t.Errorf("the %s answer lost the registration: %s", name, answer)
		}
	}
}

// Register reads the stored password when the request brings none, so
// leaving it out of the answers must not drop it from the record.
func TestRegistrationStillStoresThePassword(t *testing.T) {
	t.Parallel()

	vault := &memoryVault{registrations: map[string]map[string]interface{}{}}
	handler := NewHandler(logger.Get().Named("cf-registration-password-test"), nil, nil, vault)

	serveRegistration(t, handler, http.MethodPost, "/b/cf/registrations",
		`{"id":"reg-1","name":"lab-cf","password":"`+registrationPassword+`"}`)

	if got := vault.registrations["reg-1"]["password"]; got != registrationPassword {
		t.Fatalf("the stored registration's password = %v, want the posted one", got)
	}
}
