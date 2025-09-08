package reconciler_test

import (
	"reflect"
	"sync"
	"testing"

	. "blacksmith/pkg/reconciler"
)

// memVault implements VaultInterface for tests using in-memory storage.
type memVault struct {
	mu       sync.Mutex
	store    map[string]map[string]interface{}
	getCalls []string
	putCalls []string
	// error injection for negative-path tests
	getErr error
	putErr error
}

func newMemVault() *memVault {
	return &memVault{store: make(map[string]map[string]interface{})}
}

func (m *memVault) Get(path string) (map[string]interface{}, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.getCalls = append(m.getCalls, path)
	if m.getErr != nil {
		return nil, m.getErr
	}

	data := m.store[path]
	if data == nil {
		return nil, nil
	}
	// return a shallow copy to prevent external mutation
	cp := make(map[string]interface{}, len(data))
	for k, v := range data {
		cp[k] = v
	}

	return cp, nil
}

func (m *memVault) Put(path string, secret map[string]interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.putCalls = append(m.putCalls, path)
	if m.putErr != nil {
		return m.putErr
	}

	cp := make(map[string]interface{}, len(secret))
	for k, v := range secret {
		cp[k] = v
	}

	m.store[path] = cp

	return nil
}

// Unused in these tests but required by the interface.
func (m *memVault) GetSecret(path string) (map[string]interface{}, error)      { return nil, nil }
func (m *memVault) SetSecret(path string, secret map[string]interface{}) error { return nil }
func (m *memVault) DeleteSecret(path string) error                             { return nil }
func (m *memVault) ListSecrets(path string) ([]string, error)                  { return []string{}, nil }

func TestIndexSynchronizer_GetVaultIndex_Empty(t *testing.T) {
	t.Parallel()

	v := newMemVault()
	s := NewIndexSynchronizer(v, NewMockLogger())

	idx, err := s.GetVaultIndex()
	if err != nil {
		t.Fatalf("getVaultIndex returned error: %v", err)
	}

	if len(idx) != 0 {
		t.Fatalf("expected empty index, got: %#v", idx)
	}
}

func TestIndexSynchronizer_GetAndSaveVaultIndex_RoundTrip(t *testing.T) {
	t.Parallel()

	v := newMemVault()
	// Seed existing index under canonical path "db"
	v.store["db"] = map[string]interface{}{"inst1": map[string]interface{}{"service_id": "redis"}}

	s := NewIndexSynchronizer(v, NewMockLogger())

	idx, err := s.GetVaultIndex()
	if err != nil {
		t.Fatalf("getVaultIndex error: %v", err)
	}

	if _, ok := idx["inst1"]; !ok {
		t.Fatalf("expected inst1 present, got: %#v", idx)
	}

	// Save new index and verify stored
	newIdx := map[string]interface{}{"inst2": map[string]interface{}{"plan_id": "cache-small"}}
	if err := s.SaveVaultIndex(newIdx); err != nil {
		t.Fatalf("saveVaultIndex error: %v", err)
	}

	got := v.store["db"]
	if !reflect.DeepEqual(got, newIdx) {
		t.Fatalf("vault stored index mismatch\nwant: %#v\n got: %#v", newIdx, got)
	}
}
