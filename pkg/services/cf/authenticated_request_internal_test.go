package cf

import (
	"errors"
	"strings"
	"testing"
)

const brokerPassword = "broker-pw-7d1f"

// brokerClient is a registration client for the foundation whose broker
// password is brokerPassword.
func brokerClient(foundation *v3OnlyFoundation) *Client {
	client := NewClient(foundation.URL, foundationUser, foundationPassword, "https://broker.example.com", "broker", brokerPassword)
	client.authClient.httpClient = foundation.Client()

	return client
}

// expectRedirectRefused fails the test unless err is the refusal of an
// off-origin redirect, the receiver saw nothing, and err repeats no
// credential.
func expectRedirectRefused(t *testing.T, foundation *v3OnlyFoundation, receiver *plainReceiver, err error) {
	t.Helper()

	if !errors.Is(err, ErrAPIRedirectedOffHost) {
		t.Fatalf("CreateServiceBroker() = %v, want ErrAPIRedirectedOffHost", err)
	}

	if hits := foundation.brokerHits.Load(); hits != 1 {
		t.Fatalf("the foundation saw %d broker requests, want 1", hits)
	}

	if hits := receiver.hits.Load(); hits != 0 {
		t.Fatalf("the redirect target got %d requests carrying the token and the broker password, want 0", hits)
	}

	expectNoCredentials(t, err)

	if strings.Contains(err.Error(), brokerPassword) {
		t.Fatalf("the error repeats the broker password: %v", err)
	}
}

// A 307 keeps the method and the body, and Go keeps the Authorization
// header when only the port changes, so following it to plain http on the
// same host name would send the token and the broker password in
// plaintext.
func TestAuthenticatedRequestsDoNotFollowARedirectToPlainHTTP(t *testing.T) {
	foundation := newV3OnlyFoundation(t)
	receiver := newPlainReceiver(t)
	foundation.brokersRedirect = receiver.URL

	_, err := brokerClient(foundation).CreateServiceBroker("blacksmith")

	expectRedirectRefused(t, foundation, receiver, err)
}

// Go drops the Authorization header on a redirect to another host name,
// but it still resends a 307's body, which carries the broker password.
func TestAuthenticatedRequestsDoNotFollowARedirectToAnotherHost(t *testing.T) {
	foundation := newV3OnlyFoundation(t)
	receiver := newPlainReceiver(t)
	foundation.brokersRedirect = strings.Replace(receiver.URL, "127.0.0.1", "localhost", 1)

	_, err := brokerClient(foundation).CreateServiceBroker("blacksmith")

	expectRedirectRefused(t, foundation, receiver, err)
}
