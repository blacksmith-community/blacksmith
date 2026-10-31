package cf

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"blacksmith/internal/apierr"
	"blacksmith/pkg/http/response"
	cfservices "blacksmith/pkg/services/cf"
)

// testConnectionBodyLimit bounds the request body. The form sends a URL, a
// username, and a password, which fit many times over.
const testConnectionBodyLimit = 16 << 10

// checkCodes names the code a failed check answers with.
var checkCodes = map[string]string{
	cfservices.CheckReachable:    apierr.CodeCFUnreachable,
	cfservices.CheckTLS:          apierr.CodeCFTLSFailed,
	cfservices.CheckCloudFoundry: apierr.CodeCFNotCloudFoundry,
	cfservices.CheckToken:        apierr.CodeCFAuthFailed,
}

// TestConnection handles POST /b/cf/test-connection. It checks the API URL,
// username, and password the registration form holds, and answers 200 with
// every check, whether or not one failed. The credentials and the token are
// never logged, stored, or answered.
func (h *Handler) TestConnection(writer http.ResponseWriter, req *http.Request) {
	logger := h.logger.Named("cf-test-connection")

	var input cfservices.RegistrationTest

	err := json.NewDecoder(http.MaxBytesReader(writer, req.Body, testConnectionBodyLimit)).Decode(&input)
	if err != nil {
		apierr.Write(writer, apierr.New(http.StatusBadRequest, apierr.CodeInvalidRequest,
			"The request is not a JSON object with an api_url, a username, and a password."), http.StatusBadRequest)

		return
	}

	err = cfservices.ValidateRegistrationTest(&input)
	if err != nil {
		apierr.Write(writer, apierr.Wrap(http.StatusBadRequest, apierr.CodeInvalidRequest, err), http.StatusBadRequest)

		return
	}

	host := ""
	if parsed, parseErr := url.Parse(strings.TrimSpace(input.APIURL)); parseErr == nil {
		host = parsed.Host
	}

	checks := h.connectionTester.Run(req.Context(), &input)

	answer := map[string]interface{}{"success": true, "checks": checks}

	for _, check := range checks {
		if !check.Passed && !check.Skipped {
			answer["success"] = false
			answer["error"] = check.Message
			answer["code"] = checkCodes[check.Name]

			logger.Info("CF connection test to %s failed the %s check", host, check.Name)
			response.JSON(writer, http.StatusOK, answer)

			return
		}
	}

	answer["message"] = "Connected to Cloud Foundry, and the credentials got a token."

	logger.Info("CF connection test to %s passed", host)
	response.JSON(writer, http.StatusOK, answer)
}
