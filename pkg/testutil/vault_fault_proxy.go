package testutil

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
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
	write        bool
	pathContains string
	status       int
	message      string
	remaining    int
	hold         bool
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

// FailWrites makes the next times writes and deletes whose path contains
// pathContains answer with status and a Vault error body carrying message. A
// negative times fails every matching write. The failed requests still count
// in Writes, and none of them reaches the Vault server.
func (p *VaultFaultProxy) FailWrites(pathContains string, status int, message string, times int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.rules = append(p.rules, &faultRule{write: true, pathContains: pathContains, status: status, message: message, remaining: times})
}

// HoldWrites makes the next times writes and deletes whose path contains
// pathContains hang until the caller gives up on them, and then answer nothing.
// A negative times holds every matching write. The held requests still count in
// Writes, and none of them reaches the Vault server. A spec uses it to let a
// call's deadline expire in the middle of a write.
func (p *VaultFaultProxy) HoldWrites(pathContains string, times int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.rules = append(p.rules, &faultRule{write: true, pathContains: pathContains, remaining: times, hold: true})
}

// HoldReads makes the next times reads whose path contains pathContains hang
// until the caller gives up on them, and then answer nothing. A negative times
// holds every matching read. None of the held requests reaches the Vault
// server. A spec uses it to let a call's deadline expire in the middle of a
// read.
func (p *VaultFaultProxy) HoldReads(pathContains string, times int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.rules = append(p.rules, &faultRule{pathContains: pathContains, remaining: times, hold: true})
}

// ClearFaults removes every failure and hold set by FailReads, FailWrites,
// HoldReads, and HoldWrites.
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
	rule := p.matchRule(request)
	if rule == nil {
		return false
	}

	if rule.hold {
		holdUntilAbandoned(request)

		return true
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(rule.status)

	_ = json.NewEncoder(writer).Encode(map[string][]string{"errors": {rule.message}})

	return true
}

// matchRule records the request when it is a write, and returns a copy of the
// first rule that applies to it, or nil.
func (p *VaultFaultProxy) matchRule(request *http.Request) *faultRule {
	p.mu.Lock()
	defer p.mu.Unlock()

	isWrite := request.Method != http.MethodGet
	if isWrite {
		p.writes = append(p.writes, request.Method+" "+request.URL.Path)
	}

	for _, rule := range p.rules {
		if rule.write != isWrite || rule.remaining == 0 || !strings.Contains(request.URL.Path, rule.pathContains) {
			continue
		}

		if rule.remaining > 0 {
			rule.remaining--
		}

		matched := *rule

		return &matched
	}

	return nil
}

// heldRequestLimit keeps a held request from outliving a spec whose caller
// never gave up.
const heldRequestLimit = 30 * time.Second

// holdUntilAbandoned blocks until the client closes the connection. The server
// only notices a closed connection once the request body has been read to its
// end, so the body is read first.
func holdUntilAbandoned(request *http.Request) {
	_, _ = io.Copy(io.Discard, request.Body)

	select {
	case <-request.Context().Done():
	case <-time.After(heldRequestLimit):
	}
}
