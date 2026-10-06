package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fivetwenty-io/boshapi/internal/redact"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// maxTokenAnswer is the most of UAA's answer that is read.
const maxTokenAnswer = 1 << 20

// maxErrorDescription is the most of a non-JSON error answer that an
// AuthError keeps.
const maxErrorDescription = 1024

// logTokenURL is the log field that names UAA's token endpoint.
const logTokenURL = "token_url"

// OAuth2Config configures an OAuth2TokenManager.
type OAuth2Config struct {
	// Director is the director URL, which errors name.
	Director string
	// TokenURL is UAA's token endpoint.
	TokenURL     string
	ClientID     string
	ClientSecret string
	// HTTPClient sends the token requests. It must not be nil.
	HTTPClient *http.Client
	// Logger receives debug and warning messages. It may be nil.
	Logger boshapi.Logger
}

// OAuth2TokenManager fetches tokens from UAA with the client_credentials
// grant. One token request runs at a time: a caller that needs a new token
// waits for a slot, and a caller whose context ends while it waits returns
// at once without sending anything. The caller that holds the slot fetches
// under its own context, so when that context ends, the next caller in line
// fetches under its own instead of taking the first caller's error.
type OAuth2TokenManager struct {
	config OAuth2Config
	token  atomic.Pointer[Token]
	// slot holds one value while a caller checks or fetches the token.
	slot chan struct{}
}

// NewOAuth2TokenManager creates a token manager that sends its token
// requests through config.HTTPClient.
func NewOAuth2TokenManager(config OAuth2Config) *OAuth2TokenManager {
	return &OAuth2TokenManager{
		config: config,
		slot:   make(chan struct{}, 1),
	}
}

// GetToken returns the cached token while it is valid, and otherwise fetches
// a new one.
func (m *OAuth2TokenManager) GetToken(ctx context.Context) (string, error) {
	token := m.token.Load()
	if token.Valid() {
		return token.AccessToken, nil
	}

	return m.RefreshToken(ctx, "")
}

// RefreshToken replaces stale, the token a request carried when the director
// answered 401, and returns the replacement. A token the director rejects
// may still be valid by its expiry, for example after UAA's signing key
// changed, so a cached token equal to stale is always replaced. When the
// cached token is valid and differs from stale, another caller has already
// replaced it, and it is returned without a request. An empty stale replaces
// only a token that is missing or about to expire.
func (m *OAuth2TokenManager) RefreshToken(ctx context.Context, stale string) (string, error) {
	err := m.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer m.release()

	cached := m.token.Load()
	if cached.Valid() && (stale == "" || cached.AccessToken != stale) {
		return cached.AccessToken, nil
	}

	token, err := m.fetch(ctx)
	if err != nil {
		return "", err
	}

	m.token.Store(token)

	return token.AccessToken, nil
}

// acquire takes the slot, or returns an AuthError wrapping the context error
// when ctx ends first. A caller whose context has already ended never takes
// the slot, so it never starts a token request.
func (m *OAuth2TokenManager) acquire(ctx context.Context) error {
	if ctx.Err() != nil {
		return m.contextEnded(ctx)
	}

	select {
	case m.slot <- struct{}{}:
	case <-ctx.Done():
		return m.contextEnded(ctx)
	}

	// Both cases may have been ready, and select picks one at random.
	if ctx.Err() != nil {
		m.release()

		return m.contextEnded(ctx)
	}

	return nil
}

func (m *OAuth2TokenManager) release() {
	<-m.slot
}

func (m *OAuth2TokenManager) contextEnded(ctx context.Context) *boshapi.AuthError {
	return m.authError(context.Cause(ctx))
}

func (m *OAuth2TokenManager) authError(err error) *boshapi.AuthError {
	return &boshapi.AuthError{
		Stage:    boshapi.AuthStageToken,
		Director: m.config.Director,
		TokenURL: m.config.TokenURL,
		Err:      err,
	}
}

// fetch asks UAA for a token with the client_credentials grant. It asks for
// no scope, so UAA grants the client's own authorities, as the bosh CLI
// does.
func (m *OAuth2TokenManager) fetch(ctx context.Context) (*Token, error) {
	form := url.Values{"grant_type": {"client_credentials"}}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.config.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, m.authError(fmt.Errorf("building the token request: %w", err))
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(m.config.ClientID, m.config.ClientSecret)

	m.debug("requesting a UAA token", map[string]interface{}{logTokenURL: redact.URL(m.config.TokenURL)})

	resp, err := m.config.HTTPClient.Do(req)
	if err != nil {
		return nil, m.authError(err)
	}

	defer m.closeBody(resp.Body)

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenAnswer))
	if err != nil {
		authErr := m.authError(fmt.Errorf("reading UAA's answer: %w", err))
		authErr.StatusCode = resp.StatusCode

		return nil, authErr
	}

	if resp.StatusCode != http.StatusOK {
		return nil, m.answerError(resp.StatusCode, body)
	}

	var token Token

	err = json.Unmarshal(body, &token)
	if err != nil || token.AccessToken == "" {
		authErr := m.authError(nil)
		authErr.StatusCode = resp.StatusCode
		authErr.Description = "the answer was not a token: it held no access_token"

		return nil, authErr
	}

	if token.ExpiresIn > 0 {
		token.ExpiresAt = time.Now().Add(time.Duration(token.ExpiresIn) * time.Second)
	}

	m.debug("received a UAA token", map[string]interface{}{logTokenURL: redact.URL(m.config.TokenURL), "expires_in": token.ExpiresIn})

	return &token, nil
}

// redacted replaces what the description must never show.
const redacted = "[REDACTED]"

// scrub hides the client credentials in text that UAA, or a proxy in front of
// it, sent back. A server that echoes the request headers repeats the whole
// Authorization value, which holds the base64 of ClientID:ClientSecret, so
// scrub hides that value, its base64 payload, and the raw secret. A secret
// that is empty is skipped, because it would match between every character.
func (m *OAuth2TokenManager) scrub(text string) string {
	payload := base64.StdEncoding.EncodeToString([]byte(m.config.ClientID + ":" + m.config.ClientSecret))
	needles := []string{"Basic " + payload, payload, m.config.ClientSecret}

	for _, needle := range needles {
		if needle != "" {
			text = strings.ReplaceAll(text, needle, redacted)
		}
	}

	return text
}

// answerError builds the AuthError for a token request UAA refused. UAA's
// JSON error shape is {"error": code, "error_description": text}. Any other
// body becomes the description, trimmed and cut to maxErrorDescription
// bytes. The description is scrubbed of the client credentials before it is
// cut, so a cut cannot leave half of one behind.
func (m *OAuth2TokenManager) answerError(status int, body []byte) *boshapi.AuthError {
	authErr := m.authError(nil)
	authErr.StatusCode = status

	var answer struct {
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}

	err := json.Unmarshal(body, &answer)
	if err == nil && answer.Error != "" {
		authErr.Code = answer.Error
		authErr.Description = m.scrub(answer.ErrorDescription)

		return authErr
	}

	text := m.scrub(strings.TrimSpace(string(body)))
	if len(text) > maxErrorDescription {
		text = strings.ToValidUTF8(text[:maxErrorDescription], "")
	}

	authErr.Description = text

	return authErr
}

func (m *OAuth2TokenManager) closeBody(body io.Closer) {
	err := body.Close()
	if err != nil && m.config.Logger != nil {
		m.config.Logger.Warn("closing UAA's token answer failed", map[string]interface{}{
			logTokenURL: redact.URL(m.config.TokenURL),
			"error":     err.Error(),
		})
	}
}

func (m *OAuth2TokenManager) debug(msg string, fields map[string]interface{}) {
	if m.config.Logger != nil {
		fields["director"] = m.config.Director
		m.config.Logger.Debug(msg, fields)
	}
}
