package broker

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"blacksmith/pkg/logger"

	"github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// osbapiLogger adapts a blacksmith logger to the osbapi.Logger interface the
// OSB handler logs through, so the handler's request failures land in the
// broker log alongside everything else.
type osbapiLogger struct {
	logger logger.Logger
}

var _ osbapi.Logger = (*osbapiLogger)(nil)

// NewOSBAPILogger returns an osbapi.Logger that writes through l.
func NewOSBAPILogger(l logger.Logger) osbapi.Logger {
	return &osbapiLogger{logger: l}
}

func (l *osbapiLogger) Debug(msg string, fields map[string]any) {
	l.logger.Debug("%s", formatOSBAPIFields(msg, fields))
}

func (l *osbapiLogger) Info(msg string, fields map[string]any) {
	l.logger.Info("%s", formatOSBAPIFields(msg, fields))
}

func (l *osbapiLogger) Warn(msg string, fields map[string]any) {
	l.logger.Warn("%s", formatOSBAPIFields(msg, fields))
}

func (l *osbapiLogger) Error(msg string, fields map[string]any) {
	l.logger.Error("%s", formatOSBAPIFields(msg, fields))
}

// formatOSBAPIFields renders msg followed by fields as key=value pairs in key
// order, quoting string values so an error message with spaces stays one value.
func formatOSBAPIFields(msg string, fields map[string]any) string {
	var builder strings.Builder

	builder.WriteString(msg)

	for _, key := range slices.Sorted(maps.Keys(fields)) {
		value := fields[key]
		if s, ok := value.(string); ok {
			fmt.Fprintf(&builder, " %s=%q", key, s)

			continue
		}

		fmt.Fprintf(&builder, " %s=%v", key, value)
	}

	return builder.String()
}
