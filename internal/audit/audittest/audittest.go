// Package audittest holds the in-memory Vault and the logger the change
// store's tests use.
package audittest

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// KV is an in-memory Vault that keeps each value as the JSON Vault would
// hold. With Fail set, every read and write fails with it.
type KV struct {
	mu   sync.Mutex
	data map[string][]byte
	fail error
}

// NewKV returns an empty in-memory Vault.
func NewKV() *KV {
	return &KV{data: make(map[string][]byte)}
}

// Fail makes every later read and write fail with err, or succeed again
// when err is nil.
func (k *KV) Fail(err error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.fail = err
}

// Get decodes the value at path into out, and reports whether there was
// one.
func (k *KV) Get(_ context.Context, path string, out interface{}) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.fail != nil {
		return false, k.fail
	}

	raw, found := k.data[path]
	if !found {
		return false, nil
	}

	err := json.Unmarshal(raw, out)
	if err != nil {
		return false, fmt.Errorf("decoding %s: %w", path, err)
	}

	return true, nil
}

// Put encodes data and stores it at path.
func (k *KV) Put(_ context.Context, path string, data interface{}) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.fail != nil {
		return k.fail
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}

	k.data[path] = raw

	return nil
}

// Paths lists every path that holds a value, in order.
func (k *KV) Paths() []string {
	k.mu.Lock()
	defer k.mu.Unlock()

	paths := make([]string, 0, len(k.data))
	for path := range k.data {
		paths = append(paths, path)
	}

	sort.Strings(paths)

	return paths
}

// Dump returns every stored value joined into one text, so a test can show
// that a value never reached Vault.
func (k *KV) Dump() string {
	k.mu.Lock()
	defer k.mu.Unlock()

	var dump strings.Builder

	for path, raw := range k.data {
		dump.WriteString(path)
		dump.WriteString("=")
		dump.Write(raw)
		dump.WriteString("\n")
	}

	return dump.String()
}

// Logger records every line logged to it.
type Logger struct {
	mu    sync.Mutex
	lines []string
}

// Info records a line.
func (l *Logger) Info(msg string, args ...interface{}) {
	l.add(msg, args...)
}

// Error records a line.
func (l *Logger) Error(msg string, args ...interface{}) {
	l.add(msg, args...)
}

// Lines returns every recorded line.
func (l *Logger) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]string(nil), l.lines...)
}

// Text returns every recorded line joined into one text.
func (l *Logger) Text() string {
	return strings.Join(l.Lines(), "\n")
}

func (l *Logger) add(msg string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf(msg, args...))
}
