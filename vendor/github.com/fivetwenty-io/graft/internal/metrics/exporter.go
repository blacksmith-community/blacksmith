// Package metrics provides a lightweight metrics collection system for graft.
package metrics

import (
	"fmt"
	"sort"
)

// Format represents the output format for metrics export.
type Format string

const (
	// FormatPrometheus exports metrics in Prometheus text exposition format.
	FormatPrometheus Format = "prometheus"

	// FormatOtel exports metrics in OpenTelemetry-compatible JSON format.
	FormatOtel Format = "otel"

	// FormatJSON exports metrics as a simple JSON array.
	FormatJSON Format = "json"

	// FormatText exports metrics in human-readable text format.
	FormatText Format = "text"
)

// contentTypeJSON is the MIME type reported by every exporter whose
// output is JSON.
const contentTypeJSON = "application/json"

// Exporter is the interface for exporting metrics in different formats.
type Exporter interface {
	// Export converts the given metrics to the exporter's format.
	Export(metrics []Metric) ([]byte, error)

	// ContentType returns the MIME content type for the exported data.
	ContentType() string
}

// NewExporter creates a new Exporter for the given format.
// Returns an error if the format is not supported.
func NewExporter(format Format) (Exporter, error) {
	switch format {
	case FormatPrometheus:
		return NewPrometheusExporter(), nil
	case FormatOtel:
		return NewOtelExporter(), nil
	case FormatJSON:
		return NewJSONExporter(false), nil
	case FormatText:
		return NewTextExporter(), nil
	default:
		return nil, fmt.Errorf("unsupported export format: %s", format)
	}
}

// MustNewExporter is like NewExporter but panics if the format is not supported.
func MustNewExporter(format Format) Exporter {
	e, err := NewExporter(format)
	if err != nil {
		panic(err)
	}
	return e
}

// ExportRegistry exports all metrics from a registry in the given format.
func ExportRegistry(r *Registry, format Format) ([]byte, error) {
	exporter, err := NewExporter(format)
	if err != nil {
		return nil, err
	}
	return exporter.Export(r.All())
}

// ExportDefaultRegistry exports all metrics from the default registry in the given format.
func ExportDefaultRegistry(format Format) ([]byte, error) {
	return ExportRegistry(DefaultRegistry, format)
}

// sortMetrics returns a sorted copy of the metrics slice.
// Metrics are sorted by name, then by labels.
func sortMetrics(metrics []Metric) []Metric {
	sorted := make([]Metric, len(metrics))
	copy(sorted, metrics)

	sort.Slice(sorted, func(i, j int) bool {
		// First compare by name
		if sorted[i].Name() != sorted[j].Name() {
			return sorted[i].Name() < sorted[j].Name()
		}
		// Then by labels string
		return sorted[i].Labels().String() < sorted[j].Labels().String()
	})

	return sorted
}

// formatLabels formats labels for Prometheus-style output.
// Returns empty string if no labels, or {key="value",key2="value2"} format.
func formatLabels(labels Labels) string {
	if len(labels) == 0 {
		return ""
	}

	keys := labels.Keys()
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = fmt.Sprintf("%s=%q", k, labels[k])
	}

	return "{" + join(pairs, ",") + "}"
}

// escapeString escapes special characters in label values.
func escapeString(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			result = append(result, '\\', '\\')
		case '"':
			result = append(result, '\\', '"')
		case '\n':
			result = append(result, '\\', 'n')
		default:
			result = append(result, s[i])
		}
	}
	return string(result)
}

// join joins strings with a separator (avoiding strings import for a simple case).
func join(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}

	n := len(sep) * (len(parts) - 1)
	for _, s := range parts {
		n += len(s)
	}

	b := make([]byte, 0, n)
	b = append(b, parts[0]...)
	for _, s := range parts[1:] {
		b = append(b, sep...)
		b = append(b, s...)
	}
	return string(b)
}
