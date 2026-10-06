package testutil

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

// VaultFaultProxy sits in front of a Vault server, forwards every request, and
// can fail chosen reads with a Vault-style error. It records the writes that
// pass through, so a spec can show that a failed read led to no write.
type VaultFaultProxy struct {
	Addr string

	server *httptest.Server
	mu     sync.Mutex
	rules  []*faultRule
	writes []string
}

type faultRule struct {
	pathContains string
	status       int
	message      string
	remaining    int
}

// NewVaultFaultProxy starts a proxy to the Vault server at target, which is a
// base URL such as VaultDevServer.Addr.
func NewVaultFaultProxy(target string) (*VaultFaultProxy, error) {
	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("failed to parse vault address %q: %w", target, err)
	}

	proxy := &VaultFaultProxy{}
	reverse := httputil.NewSingleHostReverseProxy(targetURL)

	proxy.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if proxy.injectFault(writer, request) {
			return
		}

		reverse.ServeHTTP(writer, request)
	}))
	proxy.Addr = proxy.server.URL

	return proxy, nil
}

// FailReads makes the next times reads whose path contains pathContains answer
// with status and a Vault error body carrying message. A negative times fails
// every matching read. Status codes below 500 keep the Vault API client from
// retrying, so the failure is seen once per call.
func (p *VaultFaultProxy) FailReads(pathContains string, status int, message string, times int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.rules = append(p.rules, &faultRule{pathContains: pathContains, status: status, message: message, remaining: times})
}

// ClearFaults removes every read failure set by FailReads.
func (p *VaultFaultProxy) ClearFaults() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.rules = nil
}

// Writes returns the writes and deletes that reached the proxy, each as the
// method followed by the path.
func (p *VaultFaultProxy) Writes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.writes...)
}

// WritesTo returns the recorded writes whose path contains pathContains.
func (p *VaultFaultProxy) WritesTo(pathContains string) []string {
	var matched []string

	for _, write := range p.Writes() {
		if strings.Contains(write, pathContains) {
			matched = append(matched, write)
		}
	}

	return matched
}

// Close stops the proxy.
func (p *VaultFaultProxy) Close() {
	p.server.Close()
}

func (p *VaultFaultProxy) injectFault(writer http.ResponseWriter, request *http.Request) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if request.Method != http.MethodGet {
		p.writes = append(p.writes, request.Method+" "+request.URL.Path)

		return false
	}

	for _, rule := range p.rules {
		if rule.remaining == 0 || !strings.Contains(request.URL.Path, rule.pathContains) {
			continue
		}

		if rule.remaining > 0 {
			rule.remaining--
		}

		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(rule.status)

		_ = json.NewEncoder(writer).Encode(map[string][]string{"errors": {rule.message}})

		return true
	}

	return false
}
