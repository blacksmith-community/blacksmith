package cf

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"blacksmith/internal/apierr"
)

// postCFRoutes are every POST route under /b/cf.
var postCFRoutes = []string{
	"/b/cf/registrations",
	"/b/cf/test-connection",
	"/b/cf/registrations/reg-1/register",
	"/b/cf/endpoints/lab/connect",
}

// serveRecovering serves req and reports whether a route ran far enough to
// panic on the handler's nil config, manager, or vault.
func serveRecovering(handler *Handler, req *http.Request) (recorder *httptest.ResponseRecorder, ran bool) {
	recorder = httptest.NewRecorder()

	defer func() {
		if recover() != nil {
			ran = true
		}
	}()

	handler.ServeHTTP(recorder, req)

	return recorder, false
}

// A page on another site can post a form or a text/plain body with the
// operator's cached credentials, so every POST route refuses a body that
// is not declared as JSON before it does anything.
func TestCFPostRoutesRefuseABodyThatIsNotJSON(t *testing.T) {
	t.Parallel()

	contentTypes := []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", "application/jsonp", "not a media type"}

	for _, route := range postCFRoutes {
		for _, contentType := range contentTypes {
			t.Run(route+" "+contentType, func(t *testing.T) {
				t.Parallel()

				req := httptest.NewRequest(http.MethodPost, route, strings.NewReader(`{}`))
				if contentType != "" {
					req.Header.Set("Content-Type", contentType)
				}

				recorder, ran := serveRecovering(NewHandler(newCaptureLogger(), nil, nil, nil), req)
				if ran {
					t.Fatalf("POST %s with Content-Type %q ran the route", route, contentType)
				}

				var answer struct {
					Success bool   `json:"success"`
					Error   string `json:"error"`
					Code    string `json:"code"`
				}

				err := json.Unmarshal(recorder.Body.Bytes(), &answer)
				if err != nil || recorder.Code != http.StatusUnsupportedMediaType ||
					answer.Code != apierr.CodeUnsupportedMediaType || answer.Error == "" || answer.Success {
					t.Fatalf("POST %s with Content-Type %q = %d %s, want 415 %s", route, contentType,
						recorder.Code, recorder.Body.String(), apierr.CodeUnsupportedMediaType)
				}
			})
		}
	}
}

func TestCFPostRoutesAcceptJSON(t *testing.T) {
	t.Parallel()

	for _, contentType := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON"} {
		t.Run(contentType, func(t *testing.T) {
			t.Parallel()

			// An empty object fails the route's own field checks, which proves
			// the request got past the Content-Type check without any network.
			req := httptest.NewRequest(http.MethodPost, "/b/cf/test-connection", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", contentType)

			recorder, ran := serveRecovering(newConnHandler(newCaptureLogger(), nil), req)
			if ran || recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), apierr.CodeInvalidRequest) {
				t.Fatalf("POST with Content-Type %q = %d %s, want the route's 400 invalid_request", contentType, recorder.Code, recorder.Body.String())
			}
		})
	}
}
