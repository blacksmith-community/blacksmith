package cf

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	foundationUser     = "reg-admin-q4"
	foundationPassword = "pw-Hk2-registration"
	foundationToken    = "tok-reg-5c1e"
)

// v3OnlyFoundation is a Cloud Foundry like cf-deployment v56, where every
// /v2 endpoint is disabled. Its root advertises the UAA under /uaa and the
// login server under /login, so a test can tell which one was used.
type v3OnlyFoundation struct {
	*httptest.Server

	root   string
	v2Hits atomic.Int32
	// rootRedirect, when set, is where the root answers 302.
	rootRedirect string
	// tokenAnswer, when set, answers the token requests in place of the
	// password check.
	tokenAnswer http.HandlerFunc
	// brokersRedirect, when set, is where every /v3/service_brokers
	// request is answered 307, keeping its path.
	brokersRedirect string
	// brokersAnswer, when set, answers every /v3/service_brokers request.
	brokersAnswer http.HandlerFunc
	brokerHits    atomic.Int32
	uaaHits       atomic.Int32
	loginHits     atomic.Int32
}

func newV3OnlyFoundation(t *testing.T) *v3OnlyFoundation {
	t.Helper()

	foundation := &v3OnlyFoundation{}
	foundation.Server = httptest.NewTLSServer(http.HandlerFunc(foundation.serve))
	t.Cleanup(foundation.Close)

	foundation.root = fmt.Sprintf(`{"links":{"self":{"href":%q},"cloud_controller_v3":{"href":%q},"uaa":{"href":%q},"login":{"href":%q}}}`,
		foundation.URL, foundation.URL+"/v3", foundation.URL+"/uaa", foundation.URL+"/login")

	return foundation
}

func (f *v3OnlyFoundation) serve(writer http.ResponseWriter, req *http.Request) {
	switch {
	case req.URL.Path == "/v2" || strings.HasPrefix(req.URL.Path, "/v2/"):
		f.v2Hits.Add(1)
		writer.WriteHeader(http.StatusNotFound)
		_, _ = writer.Write([]byte(`{"errors":[{"title":"CF-V2Disabled","detail":"V2 endpoints are disabled"}]}`))
	case req.URL.Path == "/" && req.Method == http.MethodGet && f.rootRedirect != "":
		http.Redirect(writer, req, f.rootRedirect, http.StatusFound)
	case req.URL.Path == "/" && req.Method == http.MethodGet:
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(f.root))
	case strings.HasPrefix(req.URL.Path, "/v3/service_brokers") && f.brokersAnswer != nil:
		f.brokerHits.Add(1)
		f.brokersAnswer(writer, req)
	case strings.HasPrefix(req.URL.Path, "/v3/service_brokers") && f.brokersRedirect != "":
		f.brokerHits.Add(1)
		http.Redirect(writer, req, f.brokersRedirect+req.URL.Path, http.StatusTemporaryRedirect)
	case req.URL.Path == "/v3/info" && req.Method == http.MethodGet:
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"name":"lab-cf","version":56,"description":"cf-deployment v56","build":"abc"}`))
	case req.URL.Path == "/uaa/oauth/token":
		f.uaaHits.Add(1)
		f.token(writer, req)
	case req.URL.Path == "/login/oauth/token":
		f.loginHits.Add(1)
		f.token(writer, req)
	default:
		http.NotFound(writer, req)
	}
}

func (f *v3OnlyFoundation) token(writer http.ResponseWriter, req *http.Request) {
	if f.tokenAnswer != nil {
		f.tokenAnswer(writer, req)

		return
	}

	clientID, clientSecret, basicOK := req.BasicAuth()

	parseErr := req.ParseForm()
	if parseErr != nil || req.Method != http.MethodPost || !basicOK || clientID != "cf" || clientSecret != "" ||
		req.PostForm.Get("grant_type") != "password" ||
		req.PostForm.Get("username") != foundationUser || req.PostForm.Get("password") != foundationPassword {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"unauthorized","error_description":"Bad credentials"}`))

		return
	}

	writer.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(writer, `{"access_token":%q,"token_type":"bearer","expires_in":600}`, foundationToken)
}

func (f *v3OnlyFoundation) client() *CFAuthClient {
	client := NewCFAuthClient(f.URL+"/", foundationUser, foundationPassword)
	client.httpClient = f.Client()

	return client
}

func TestRegistrationAuthenticatesWithoutV2(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)
	client := foundation.client()

	err := client.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate() against a foundation without /v2 = %v, want nil", err)
	}

	token, err := client.GetAuthToken()
	if err != nil || token != "bearer "+foundationToken {
		t.Fatalf("GetAuthToken() = %q, %v, want the UAA token", token, err)
	}

	if hits := foundation.v2Hits.Load(); hits != 0 {
		t.Errorf("registration asked /v2 %d times, want 0", hits)
	}

	if foundation.uaaHits.Load() != 1 || foundation.loginHits.Load() != 0 {
		t.Errorf("token requests went to uaa %d and login %d times, want the uaa link once",
			foundation.uaaHits.Load(), foundation.loginHits.Load())
	}
}

func TestRegistrationTestConnectionWithoutV2(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)

	info, err := foundation.client().TestConnection()
	if err != nil {
		t.Fatalf("TestConnection() against a foundation without /v2 = %v, want nil", err)
	}

	if info.Name != "lab-cf" || info.Version != "56" || info.Description != "cf-deployment v56" || info.APIURL != foundation.URL {
		t.Errorf("TestConnection() = %+v, want the /v3/info name, version, and description", *info)
	}

	if hits := foundation.v2Hits.Load(); hits != 0 {
		t.Errorf("registration asked /v2 %d times, want 0", hits)
	}
}

func TestRegistrationClientTestConnectionWithoutV2(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)

	client := NewClient(foundation.URL, foundationUser, foundationPassword, "https://broker.example.com", "broker", "secret")
	client.authClient.httpClient = foundation.Client()

	result, err := client.TestConnection()
	if err != nil || !result.Success {
		t.Fatalf("Client.TestConnection() = %+v, %v, want success", result, err)
	}

	if hits := foundation.v2Hits.Load(); hits != 0 {
		t.Errorf("registration asked /v2 %d times, want 0", hits)
	}
}

func TestRegistrationFallsBackToTheLoginLink(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)
	foundation.root = fmt.Sprintf(`{"links":{"cloud_controller_v3":{"href":%q},"login":{"href":%q}}}`,
		foundation.URL+"/v3", foundation.URL+"/login")

	err := foundation.client().Authenticate()
	if err != nil {
		t.Fatalf("Authenticate() with only a login link = %v, want nil", err)
	}

	if foundation.loginHits.Load() != 1 || foundation.uaaHits.Load() != 0 {
		t.Errorf("token requests went to uaa %d and login %d times, want the login link once",
			foundation.uaaHits.Load(), foundation.loginHits.Load())
	}
}

func TestRegistrationSendsNoCredentialsToAnAPIThatIsNotCloudFoundry(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)
	foundation.root = fmt.Sprintf(`{"links":{"uaa":{"href":%q}}}`, foundation.URL+"/uaa")

	err := foundation.client().Authenticate()
	if !errors.Is(err, errNotCloudFoundry) {
		t.Fatalf("Authenticate() against a root without cloud_controller_v3 = %v, want errNotCloudFoundry", err)
	}

	if foundation.uaaHits.Load() != 0 {
		t.Errorf("registration sent the credentials %d times, want 0", foundation.uaaHits.Load())
	}

	if strings.Contains(err.Error(), foundationPassword) || strings.Contains(err.Error(), foundationUser) {
		t.Error("the error repeats the credentials")
	}
}

func TestRegistrationReportsRefusedCredentials(t *testing.T) {
	t.Parallel()

	foundation := newV3OnlyFoundation(t)
	client := NewCFAuthClient(foundation.URL, foundationUser, "wrong-password")
	client.httpClient = foundation.Client()

	err := client.Authenticate()
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("Authenticate() with a wrong password = %v, want ErrAuthenticationFailed", err)
	}
}

// The connection test and registration must pick the same endpoint from
// the same root, so a test that passes means registration can sign in.
func TestRegistrationAndTheConnectionTestUseTheSameEndpoint(t *testing.T) {
	t.Parallel()

	for _, root := range []string{"both", "login only"} {
		t.Run(root, func(t *testing.T) {
			t.Parallel()

			foundation := newV3OnlyFoundation(t)
			if root == "login only" {
				foundation.root = fmt.Sprintf(`{"links":{"cloud_controller_v3":{"href":%q},"login":{"href":%q}}}`,
					foundation.URL+"/v3", foundation.URL+"/login")
			}

			pool := foundation.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
			tester := &ConnectionTester{RootCAs: pool}

			checks := tester.Run(t.Context(), &RegistrationTest{APIURL: foundation.URL, Username: foundationUser, Password: foundationPassword})
			for _, check := range checks {
				if !check.Passed {
					t.Fatalf("connection test check %s failed: %s", check.Name, check.Message)
				}
			}

			testerUAA, testerLogin := foundation.uaaHits.Load(), foundation.loginHits.Load()

			err := foundation.client().Authenticate()
			if err != nil {
				t.Fatalf("Authenticate() = %v, want nil", err)
			}

			if foundation.uaaHits.Load()-testerUAA != testerUAA || foundation.loginHits.Load()-testerLogin != testerLogin {
				t.Errorf("the connection test asked uaa %d and login %d times, but registration asked uaa %d and login %d times",
					testerUAA, testerLogin, foundation.uaaHits.Load()-testerUAA, foundation.loginHits.Load()-testerLogin)
			}

			if foundation.v2Hits.Load() != 0 {
				t.Errorf("/v2 was asked %d times, want 0", foundation.v2Hits.Load())
			}
		})
	}
}

func TestAuthEndpointFromRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		root string
		want string
		err  error
	}{
		{
			name: "uaa first",
			root: `{"links":{"cloud_controller_v3":{"href":"https://api.example.com/v3"},"uaa":{"href":"https://uaa.example.com/"},"login":{"href":"https://login.example.com"}}}`,
			want: "https://uaa.example.com",
		},
		{
			name: "login when there is no uaa link",
			root: `{"links":{"cloud_controller_v3":{"href":"https://api.example.com/v3"},"login":{"href":"https://login.example.com"}}}`,
			want: "https://login.example.com",
		},
		{
			name: "no authentication link",
			root: `{"links":{"cloud_controller_v3":{"href":"https://api.example.com/v3"}}}`,
			err:  errNoAuthEndpoint,
		},
		{
			name: "no cloud_controller_v3 link",
			root: `{"links":{"uaa":{"href":"https://uaa.example.com"}}}`,
			err:  errNotCloudFoundry,
		},
		{
			name: "not JSON",
			root: `<html>hello</html>`,
			err:  errNotCloudFoundry,
		},
		{
			name: "query and fragment on the link",
			root: `{"links":{"cloud_controller_v3":{"href":"https://api.example.com/v3"},"uaa":{"href":"https://uaa.example.com/?x=1#top"}}}`,
			want: "https://uaa.example.com",
		},
		{
			name: "query on a link with a path",
			root: `{"links":{"cloud_controller_v3":{"href":"https://api.example.com/v3"},"uaa":{"href":"https://example.com/uaa/?x=1"}}}`,
			want: "https://example.com/uaa",
		},
		{
			name: "credentials in the link",
			root: `{"links":{"cloud_controller_v3":{"href":"https://api.example.com/v3"},"uaa":{"href":"https://u:p@uaa.example.com"}}}`,
			err:  ErrAPIURLMalformed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := authEndpointFromRoot([]byte(test.root))
			if test.err != nil {
				if !errors.Is(err, test.err) {
					t.Fatalf("authEndpointFromRoot() error = %v, want %v", err, test.err)
				}

				return
			}

			if err != nil || got.String() != test.want {
				t.Fatalf("authEndpointFromRoot() = %v, %v, want %s", got, err, test.want)
			}
		})
	}
}
