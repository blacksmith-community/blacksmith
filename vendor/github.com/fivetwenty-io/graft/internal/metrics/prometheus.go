package metrics

import (
	"bytes"
	"fmt"
	"math"
	"sort"
)

// Prometheus format constants.
const (
	positiveInfinity = "+Inf"
)

// PrometheusExporter exports metrics in Prometheus text exposition format.
// See: https://prometheus.io/docs/instrumenting/exposition_formats/
type PrometheusExporter struct{}

// NewPrometheusExporter creates a new PrometheusExporter.
func NewPrometheusExporter() *PrometheusExporter {
	return &PrometheusExporter{}
}

// ContentType returns the MIME content type for Prometheus format.
func (e *PrometheusExporter) ContentType() string {
	return "text/plain; version=0.0.4; charset=utf-8"
}

// Export converts the given metrics to Prometheus text format.
func (e *PrometheusExporter) Export(metrics []Metric) ([]byte, error) {
	var buf bytes.Buffer

	// Group metrics by name for proper formatting
	grouped := groupMetricsByName(metrics)

	// Sort group names for consistent output
	names := make([]string, 0, len(grouped))
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		group := grouped[name]
		if len(group) == 0 {
			continue
		}

		// Determine metric type from first metric
		metricType := group[0].Type()

		// Write HELP comment (using metric name as help text)
		fmt.Fprintf(&buf, "# HELP %s %s metric\n", name, metricType)

		// Write TYPE comment
		fmt.Fprintf(&buf, "# TYPE %s %s\n", name, prometheusTypeName(metricType))

		// Sort metrics within group by labels for consistent output
		sortedGroup := sortMetrics(group)

		// Write metric values
		for _, m := range sortedGroup {
			e.writeMetric(&buf, m)
		}

		buf.WriteString("\n")
	}

	return buf.Bytes(), nil
}

// writeMetric writes a single metric in Prometheus format.
func (e *PrometheusExporter) writeMetric(buf *bytes.Buffer, m Metric) {
	switch m.Type() {
	case MetricTypeCounter, MetricTypeGauge:
		e.writeSimpleMetric(buf, m)
	case MetricTypeHistogram:
		e.writeHistogram(buf, m)
	case MetricTypeSummary:
		// Summary type not yet implemented, treat as simple metric
		e.writeSimpleMetric(buf, m)
	}
}

// writeSimpleMetric writes a counter or gauge metric.
func (e *PrometheusExporter) writeSimpleMetric(buf *bytes.Buffer, m Metric) {
	labelStr := formatLabels(m.Labels())
	value := metricValue(m)
	fmt.Fprintf(buf, "%s%s %s\n", m.Name(), labelStr, formatFloat(value))
}

// writeHistogram writes a histogram metric with buckets, count, and sum.
func (e *PrometheusExporter) writeHistogram(buf *bytes.Buffer, m Metric) {
	name := m.Name()
	labels := m.Labels()
	labelStr := formatLabels(labels)

	hv, ok := m.Value().(HistogramValue)
	if !ok {
		return
	}

	// Get sorted bucket boundaries
	bounds := make([]float64, 0, len(hv.Buckets))
	for bound := range hv.Buckets {
		bounds = append(bounds, bound)
	}
	sort.Float64s(bounds)

	// Write bucket values
	for _, bound := range bounds {
		count := hv.Buckets[bound]
		bucketLabels := labels.Clone()
		if bucketLabels == nil {
			bucketLabels = make(Labels)
		}
		bucketLabels["le"] = formatBucketBound(bound)
		bucketLabelStr := formatLabels(bucketLabels)
		fmt.Fprintf(buf, "%s_bucket%s %d\n", name, bucketLabelStr, count)
	}

	// Write sum and count
	fmt.Fprintf(buf, "%s_sum%s %s\n", name, labelStr, formatFloat(hv.Sum))
	fmt.Fprintf(buf, "%s_count%s %d\n", name, labelStr, hv.Count)
}

// prometheusTypeName converts our metric type to Prometheus type name.
func prometheusTypeName(t MetricType) string {
	switch t {
	case MetricTypeCounter:
		return "counter"
	case MetricTypeGauge:
		return "gauge"
	case MetricTypeHistogram:
		return "histogram"
	case MetricTypeSummary:
		return "summary"
	default:
		return "untyped"
	}
}

// groupMetricsByName groups metrics by their name.
func groupMetricsByName(metrics []Metric) map[string][]Metric {
	grouped := make(map[string][]Metric)
	for _, m := range metrics {
		name := m.Name()
		grouped[name] = append(grouped[name], m)
	}
	return grouped
}

// metricValue returns the numeric value of a metric.
func metricValue(m Metric) float64 {
	switch v := m.Value().(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case uint64:
		return float64(v)
	case int:
		return float64(v)
	default:
		return 0
	}
}

// formatFloat formats a float64 for Prometheus output.
func formatFloat(v float64) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 1) {
		return positiveInfinity
	}
	if math.IsInf(v, -1) {
		return "-Inf"
	}
	// Use %g for compact representation, but ensure precision
	return fmt.Sprintf("%g", v)
}

// formatBucketBound formats a bucket boundary for Prometheus.
func formatBucketBound(bound float64) string {
	if math.IsInf(bound, 1) {
		return positiveInfinity
	}
	return formatFloat(bound)
}
