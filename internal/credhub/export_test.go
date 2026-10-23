package credhub

import (
	"context"
	"net/http"
	"time"

	"blacksmith/pkg/logger"
)

// ExplainFailure exposes explainFailure to package credhub_test.
func ExplainFailure(operation string, err error) (string, string) {
	return explainFailure(operation, err)
}

// SetCleanerBackoff replaces the waits between retry attempts so tests do
// not sleep for seconds.
func SetCleanerBackoff(cleaner *Cleaner, delays ...time.Duration) {
	cleaner.backoff = delays
}

// SetCleanerRunTimeout replaces RunTimeout for one cleaner, so a test of the
// run deadline does not wait two minutes.
func SetCleanerRunTimeout(cleaner *Cleaner, timeout time.Duration) {
	cleaner.runTimeout = timeout
}

// ProbeWithin runs the startup probe under timeout instead of ProbeTimeout,
// so a test of the deadline does not wait thirty seconds.
func ProbeWithin(ctx context.Context, client *Client, directorName string, log logger.Logger, timeout time.Duration) error {
	return probeWithin(ctx, client, directorName, log, timeout)
}

// SetRegenerateBufferHook makes Regenerate hand its answer buffer to hook
// after it has been cleared.
func SetRegenerateBufferHook(client *Client, hook func([]byte)) {
	client.bufferSeen = hook
}

// RegenerateRequestForTest builds the request Regenerate sends.
func RegenerateRequestForTest(ctx context.Context, client *Client, certificateID string) (*http.Request, error) {
	request, _, err := client.regenerateRequest(ctx, certificateID, "token")

	return request, err
}

// SetKeyPairBufferHook makes CertificateKeyPair hand its answer buffer to
// hook after it has been cleared.
func SetKeyPairBufferHook(client *Client, hook func([]byte)) {
	client.bufferSeen = hook
}

// DecodeKeyPair exposes decodeKeyPair to package credhub_test.
func DecodeKeyPair(body []byte) ([]byte, []byte, error) {
	return decodeKeyPair(body)
}

// SetCertificateVersionBufferHook makes CertificateVersion hand its answer
// buffer to hook after it has been cleared.
func SetCertificateVersionBufferHook(client *Client, hook func([]byte)) {
	client.bufferSeen = hook
}
