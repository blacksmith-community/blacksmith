package server

import (
	"context"
	"log/slog"
	"maps"
	"slices"

	osbapi "github.com/fivetwenty-io/osbapi/v2/pkg/osbapi"
)

// slogLogger is the handler's default osbapi.Logger. It resolves
// slog.Default() on every call, so a default installed by the embedding
// program after NewHandler returns still receives the handler's logs.
type slogLogger struct{}

var _ osbapi.Logger = slogLogger{}

func (slogLogger) Debug(msg string, fields map[string]any) { logSlog(slog.LevelDebug, msg, fields) }
func (slogLogger) Info(msg string, fields map[string]any)  { logSlog(slog.LevelInfo, msg, fields) }
func (slogLogger) Warn(msg string, fields map[string]any)  { logSlog(slog.LevelWarn, msg, fields) }
func (slogLogger) Error(msg string, fields map[string]any) { logSlog(slog.LevelError, msg, fields) }

// logSlog emits msg at level with fields as attributes in key order, so the
// output is stable across runs.
func logSlog(level slog.Level, msg string, fields map[string]any) {
	attrs := make([]slog.Attr, 0, len(fields))
	for _, k := range slices.Sorted(maps.Keys(fields)) {
		attrs = append(attrs, slog.Any(k, fields[k]))
	}
	slog.Default().LogAttrs(context.Background(), level, msg, attrs...)
}
