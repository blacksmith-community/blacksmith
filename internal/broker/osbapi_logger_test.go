package broker_test

import (
	"fmt"
	"sync"

	"blacksmith/internal/broker"
	"blacksmith/pkg/logger"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

// levelRecordingLogger keeps every message with its level, formatted the way
// the broker's printf-style logger formats them.
type levelRecordingLogger struct {
	*logger.NoOpLogger

	mu      sync.Mutex
	entries []string
}

func (r *levelRecordingLogger) Debug(msg string, args ...interface{}) { r.record("DEBUG", msg, args) }
func (r *levelRecordingLogger) Info(msg string, args ...interface{})  { r.record("INFO", msg, args) }
func (r *levelRecordingLogger) Warn(msg string, args ...interface{})  { r.record("WARN", msg, args) }
func (r *levelRecordingLogger) Error(msg string, args ...interface{}) { r.record("ERROR", msg, args) }

func (r *levelRecordingLogger) record(level, msg string, args []interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.entries = append(r.entries, level+" "+fmt.Sprintf(msg, args...))
}

var _ = Describe("OSB API logger adapter", func() {
	var (
		recorder *levelRecordingLogger
		adapter  interface {
			Debug(msg string, fields map[string]any)
			Info(msg string, fields map[string]any)
			Warn(msg string, fields map[string]any)
			Error(msg string, fields map[string]any)
		}
	)

	BeforeEach(func() {
		recorder = &levelRecordingLogger{NoOpLogger: &logger.NoOpLogger{}}
		adapter = broker.NewOSBAPILogger(recorder)
	})

	It("logs a handler failure at error level with its fields in key order", func() {
		adapter.Error("broker unbind failed", map[string]any{
			"status":      500,
			"operation":   "unbind",
			"instance_id": "instance-7f3c",
			"binding_id":  "binding-91ad",
			"error":       "failed to delete Valkey ACL user binding-91ad: valkey PING 10.0.0.5:6379 (plaintext) failed: MISCONF",
		})

		Expect(recorder.entries).To(Equal([]string{
			`ERROR broker unbind failed binding_id="binding-91ad" ` +
				`error="failed to delete Valkey ACL user binding-91ad: valkey PING 10.0.0.5:6379 (plaintext) failed: MISCONF" ` +
				`instance_id="instance-7f3c" operation="unbind" status=500`,
		}))
	})

	It("routes each level to the matching broker logger method", func() {
		adapter.Debug("d", nil)
		adapter.Info("i", nil)
		adapter.Warn("w", map[string]any{"error": "bad header"})

		Expect(recorder.entries).To(Equal([]string{"DEBUG d", "INFO i", `WARN w error="bad header"`}))
	})

	It("keeps percent signs in messages and values literal", func() {
		adapter.Error("100% failed", map[string]any{"error": "disk 100%s full"})

		Expect(recorder.entries).To(Equal([]string{`ERROR 100% failed error="disk 100%s full"`}))
	})
})
