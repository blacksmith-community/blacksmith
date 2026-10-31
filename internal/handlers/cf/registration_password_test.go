package cf

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"blacksmith/internal/apierr"
	"blacksmith/internal/config"
	"blacksmith/internal/interfaces"
	"blacksmith/pkg/logger"
	cfservices "blacksmith/pkg/services/cf"
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

func (v *memoryVault) UpdateCFRegistrationStatus(_ context.Context, registrationID, status, _ string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.registrations[registrationID]["status"] = status

	return nil
}

// stored is a copy of the stored record, safe to read while a
// registration runs.
func (v *memoryVault) stored(registrationID string) map[string]interface{} {
	v.mu.Lock()
	defer v.mu.Unlock()

	record := map[string]interface{}{}
	for key, value := range v.registrations[registrationID] {
		record[key] = value
	}

	return record
}

// storedPassword reports a password the record holds under any spelling of
// the key.
func storedPassword(record map[string]interface{}) (interface{}, bool) {
	for key, value := range record {
		if strings.EqualFold(key, "password") {
			return value, true
		}
	}

	return nil, false
}

// brokerConfig is a broker with CF registration on and no default name.
type brokerConfig struct {
	interfaces.Config
}

func (brokerConfig) GetBrokerConfig() config.BrokerConfig { return config.BrokerConfig{} }

// passwordOperations records the password a registration ran with.
type passwordOperations struct {
	passwords chan string
}

func (ops *passwordOperations) PerformRegistration(req *cfservices.RegistrationRequest, progressChan chan<- cfservices.RegistrationProgress) error {
	close(progressChan)
	ops.passwords <- req.Password

	return nil
}

// registerHandler is a handler over vault whose registrations report the
// password they ran with to ops.
func registerHandler(vault *memoryVault) (*Handler, *passwordOperations) {
	ops := &passwordOperations{passwords: make(chan string, 1)}
	handler := NewHandler(logger.Get().Named("cf-registration-password-test"), brokerConfig{}, nil, vault)
	handler.newCFOperations = func() cfOperations { return ops }
	handler.persistProgress = nil

	return handler, ops
}

func (ops *passwordOperations) password(t *testing.T) string {
	t.Helper()

	select {
	case password := <-ops.passwords:
		return password
	case <-time.After(5 * time.Second):
		t.Fatal("the registration never ran")

		return ""
	}
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

// Register takes the password in its own body, so neither creating nor
// editing a registration stores one.
func TestNewRegistrationsStoreNoPassword(t *testing.T) {
	t.Parallel()

	vault := &memoryVault{registrations: map[string]map[string]interface{}{}}
	handler := NewHandler(logger.Get().Named("cf-registration-password-test"), nil, nil, vault)

	serveRegistration(t, handler, http.MethodPost, "/b/cf/registrations",
		`{"id":"reg-1","name":"lab-cf","password":"`+registrationPassword+`","Password":"`+registrationPassword+`"}`)

	if password, found := storedPassword(vault.stored("reg-1")); found {
		t.Fatalf("creating the registration stored the password %v, want none", password)
	}

	serveRegistration(t, handler, http.MethodPut, "/b/cf/registrations/reg-1",
		`{"id":"reg-1","name":"lab-cf","password":"`+registrationPassword+`"}`)

	if password, found := storedPassword(vault.stored("reg-1")); found {
		t.Fatalf("editing the registration stored the password %v, want none", password)
	}
}

// A record saved before the broker stopped storing passwords keeps its
// password through an edit, because nothing migrates it.
// legacyRecord is a registration saved by a broker that still stored the
// CF password.
func legacyRecord() map[string]interface{} {
	return map[string]interface{}{
		"id": "reg-1", "name": "lab-cf", "status": "pending",
		"api_url": "https://api.cf.example.com", "username": "admin", "password": "legacy-pw-5m2x",
	}
}

func TestEditingALegacyRegistrationKeepsItsPassword(t *testing.T) {
	t.Parallel()

	vault := &memoryVault{registrations: map[string]map[string]interface{}{"reg-1": legacyRecord()}}
	handler := NewHandler(logger.Get().Named("cf-registration-password-test"), nil, nil, vault)

	serveRegistration(t, handler, http.MethodPut, "/b/cf/registrations/reg-1",
		`{"id":"reg-1","name":"lab-cf-renamed","api_url":"https://api.cf.example.com","username":"admin","password":"`+registrationPassword+`"}`)

	record := vault.stored("reg-1")
	if record["password"] != "legacy-pw-5m2x" || record["name"] != "lab-cf-renamed" {
		t.Fatalf("the edited legacy record = %v, want the new name and the old password", record)
	}
}

// A legacy password belongs to the foundation and user it was saved for, so
// an edit that points the record elsewhere drops it, and register then needs
// the password in its body.
func TestEditingALegacyRegistrationsFoundationDropsItsPassword(t *testing.T) {
	t.Parallel()

	for name, edit := range map[string]string{
		"another API URL":  `{"id":"reg-1","name":"lab-cf","status":"pending","api_url":"https://api.other.example.com","username":"admin"}`,
		"another username": `{"id":"reg-1","name":"lab-cf","status":"pending","api_url":"https://api.cf.example.com","username":"someone-else"}`,
		"no API URL":       `{"id":"reg-1","name":"lab-cf","status":"pending","username":"admin"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			vault := &memoryVault{registrations: map[string]map[string]interface{}{"reg-1": legacyRecord()}}
			handler, ops := registerHandler(vault)

			serveRegistration(t, handler, http.MethodPut, "/b/cf/registrations/reg-1", edit)

			if password, kept := storedPassword(vault.stored("reg-1")); kept {
				t.Fatalf("the edited record kept the password %v, want none", password)
			}

			recorder := startRegistration(handler, `{}`)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("register with no body password answered %d %s, want 400", recorder.Code, recorder.Body.String())
			}

			select {
			case password := <-ops.passwords:
				t.Fatalf("the registration ran with %q, want it not to run", password)
			default:
			}
		})
	}
}

func startRegistration(handler *Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/b/cf/registrations/reg-1/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)

	return recorder
}

func TestRegisterUsesThePasswordInItsBody(t *testing.T) {
	t.Parallel()

	for name, record := range map[string]map[string]interface{}{
		"new record":    {"id": "reg-1", "name": "lab-cf", "status": "pending"},
		"legacy record": {"id": "reg-1", "name": "lab-cf", "status": "pending", "password": "legacy-pw-5m2x"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			handler, ops := registerHandler(&memoryVault{registrations: map[string]map[string]interface{}{"reg-1": record}})

			recorder := startRegistration(handler, `{"password":"`+registrationPassword+`"}`)
			if recorder.Code != http.StatusOK {
				t.Fatalf("register answered %d: %s", recorder.Code, recorder.Body.String())
			}

			if got := ops.password(t); got != registrationPassword {
				t.Fatalf("the registration ran with %q, want the password in the request body", got)
			}
		})
	}
}

func TestRegisterFallsBackToALegacyStoredPassword(t *testing.T) {
	t.Parallel()

	handler, ops := registerHandler(&memoryVault{registrations: map[string]map[string]interface{}{
		"reg-1": {"id": "reg-1", "name": "lab-cf", "status": "pending", "password": "legacy-pw-5m2x"},
	}})

	recorder := startRegistration(handler, `{}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("register answered %d: %s", recorder.Code, recorder.Body.String())
	}

	if got := ops.password(t); got != "legacy-pw-5m2x" {
		t.Fatalf("the registration ran with %q, want the legacy stored password", got)
	}
}

func TestRegisterWithoutAPasswordIsABadRequest(t *testing.T) {
	t.Parallel()

	for _, body := range []string{`{}`, `{"password":""}`, `{"password":42}`, `not json`} {
		handler, ops := registerHandler(&memoryVault{registrations: map[string]map[string]interface{}{
			"reg-1": {"id": "reg-1", "name": "lab-cf", "status": "pending"},
		}})

		recorder := startRegistration(handler, body)

		var answer struct {
			Success *bool  `json:"success"`
			Error   string `json:"error"`
			Code    string `json:"code"`
		}

		err := json.Unmarshal(recorder.Body.Bytes(), &answer)
		if recorder.Code != http.StatusBadRequest || err != nil || answer.Code != apierr.CodeInvalidRequest ||
			answer.Success == nil || *answer.Success || !strings.Contains(answer.Error, "password") {
			t.Fatalf("register with the body %s answered %d %s, want 400 invalid_request naming the password",
				body, recorder.Code, recorder.Body.String())
		}

		select {
		case password := <-ops.passwords:
			t.Fatalf("the registration ran with %q, want it not to run", password)
		default:
		}
	}
}
