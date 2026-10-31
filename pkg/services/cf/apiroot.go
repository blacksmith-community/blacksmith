package cf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var errNotCloudFoundry = errors.New("not a Cloud Foundry root document")

// authEndpointFromRoot finds the authentication endpoint in a Cloud Foundry
// API root document, the answer to GET /. Registration and the connection
// test both find it here, so a test that passes signs in where registration
// will. The v3 root's uaa link names the endpoint that /v2/info used to,
// and cf-deployment v56 disables /v2, so the root is the only place to ask.
// The login link stands in when a root has no uaa link.
func authEndpointFromRoot(body []byte) (*url.URL, error) {
	var root struct {
		Links struct {
			CloudControllerV3 struct {
				Href string `json:"href"`
			} `json:"cloud_controller_v3"`
			UAA struct {
				Href string `json:"href"`
			} `json:"uaa"`
			Login struct {
				Href string `json:"href"`
			} `json:"login"`
		} `json:"links"`
	}

	err := json.Unmarshal(body, &root)
	if err != nil || root.Links.CloudControllerV3.Href == "" {
		return nil, errNotCloudFoundry
	}

	advertised := root.Links.UAA.Href
	if advertised == "" {
		advertised = root.Links.Login.Href
	}

	return authEndpoint(advertised)
}

func authEndpoint(advertised string) (*url.URL, error) {
	if advertised == "" {
		return nil, errNoAuthEndpoint
	}

	parsed, err := url.Parse(advertised)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil {
		return nil, ErrAPIURLMalformed
	}

	// The token path goes after the endpoint's own path, so a query or
	// fragment on the link would swallow it.
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawPath = strings.TrimSuffix(parsed.RawPath, "/")

	return parsed, nil
}

// newPasswordGrant builds the UAA password-grant token request that
// registration and the connection test both send to the endpoint
// authEndpointFromRoot found.
func newPasswordGrant(ctx context.Context, authURL *url.URL, username, password string) (*http.Request, error) {
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("username", username)
	form.Set("password", password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL.String()+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building the token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth("cf", "")

	return req, nil
}

// readLimited reads body, failing with errResponseTooLarge past limit bytes.
func readLimited(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading the response: %w", err)
	}

	if int64(len(data)) > limit {
		return nil, errResponseTooLarge
	}

	return data, nil
}
