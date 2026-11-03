package metrics

import (
	"bytes"
	"fmt"
	"math"
	"sort"
)

// TextExporter exports metrics in human-readable text format.
// This format is designed for terminal output and log files.
type TextExporter struct{}

// NewTextExporter creates a new TextExporter.
func NewTextExporter() *TextExporter {
	return &TextExporter{}
}

// ContentType returns the MIME content type for text format.
func (e *TextExporter) ContentType() string {
	return "text/plain"
}

// Export converts the given metrics to human-readable text format.
func (e *TextExporter) Export(metrics []Metric) ([]byte, error) {
	var buf bytes.Buffer

	// Group metrics by name
	grouped := groupMetricsByName(metrics)

	// Sort names for consistent output
	names := make([]string, 0, len(grouped))
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)

	// Calculate max name length for alignment
	maxNameLen := 0
	for _, name := range names {
		if len(name) > maxNameLen {
			maxNameLen = len(name)
		}
	}

	for i, name := range names {
		group := grouped[name]
		if len(group) == 0 {
			continue
		}

		// Determine metric type from first metric
		metricType := group[0].Type()

		// Write section header
		fmt.Fprintf(&buf, "=== %s (%s) ===\n", name, metricType)

		// Sort metrics within group by labels
		sortedGroup := sortMetrics(group)

		// Write metric values
		for _, m := range sortedGroup {
			e.writeMetric(&buf, m)
		}

		// Add spacing between groups
		if i < len(names)-1 {
			buf.WriteString("\n")
		}
	}

	return buf.Bytes(), nil
}

// writeMetric writes a single metric in human-readable format.
func (e *TextExporter) writeMetric(buf *bytes.Buffer, m Metric) {
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
func (e *TextExporter) writeSimpleMetric(buf *bytes.Buffer, m Metric) {
	labels := m.Labels()
	value := metricValue(m)

	if len(labels) == 0 {
		fmt.Fprintf(buf, "  value: %s\n", formatTextValue(value))
	} else {
		fmt.Fprintf(buf, "  %s: %s\n", labels.String(), formatTextValue(value))
	}
}

// writeHistogram writes a histogram metric with distribution summary.
func (e *TextExporter) writeHistogram(buf *bytes.Buffer, m Metric) {
	labels := m.Labels()
	hv, ok := m.Value().(HistogramValue)
	if !ok {
		return
	}

	// Write labels if present
	if len(labels) > 0 {
		fmt.Fprintf(buf, "  [%s]\n", labels.String())
	}

	// Write basic statistics
	fmt.Fprintf(buf, "    count:  %d\n", hv.Count)
	fmt.Fprintf(buf, "    sum:    %s\n", formatTextValue(hv.Sum))

	// Calculate and write mean
	mean := float64(0)
	if hv.Count > 0 {
		mean = hv.Sum / float64(hv.Count)
	}
	fmt.Fprintf(buf, "    mean:   %s\n", formatTextValue(mean))

	// Calculate and write percentiles
	percentiles := e.calculatePercentiles(hv)
	if len(percentiles) > 0 {
		fmt.Fprintf(buf, "    percentiles:\n")
		for _, p := range percentiles {
			fmt.Fprintf(buf, "      p%d: %s\n", int(p.percentile*100), formatTextValue(p.value))
		}
	}

	// Write bucket distribution
	e.writeBucketDistribution(buf, hv)
}

// percentileResult holds a calculated percentile.
type percentileResult struct {
	percentile float64
	value      float64
}

// calculatePercentiles estimates percentiles from histogram buckets.
func (e *TextExporter) calculatePercentiles(hv HistogramValue) []percentileResult {
	if hv.Count == 0 {
		return nil
	}

	// Get sorted bucket boundaries
	bounds := make([]float64, 0, len(hv.Buckets))
	for bound := range hv.Buckets {
		bounds = append(bounds, bound)
	}
	sort.Float64s(bounds)

	// Calculate percentiles using linear interpolation
	targetPercentiles := []float64{0.50, 0.90, 0.95, 0.99}
	results := make([]percentileResult, 0, len(targetPercentiles))

	for _, p := range targetPercentiles {
		targetCount := uint64(float64(hv.Count) * p)
		if targetCount == 0 {
			targetCount = 1
		}

		// Find the bucket containing the target count
		var prevBound float64
		var prevCount uint64
		for _, bound := range bounds {
			count := hv.Buckets[bound]
			if count >= targetCount {
				// Linear interpolation within bucket
				if count == prevCount {
					results = append(results, percentileResult{p, bound})
				} else {
					// Interpolate
					ratio := float64(targetCount-prevCount) / float64(count-prevCount)
					value := prevBound + ratio*(bound-prevBound)
					if math.IsInf(bound, 1) {
						value = prevBound
					}
					results = append(results, percentileResult{p, value})
				}
				break
			}
			prevBound = bound
			prevCount = count
		}
	}

	return results
}

// writeBucketDistribution writes a visual bucket distribution.
func (e *TextExporter) writeBucketDistribution(buf *bytes.Buffer, hv HistogramValue) {
	if hv.Count == 0 || len(hv.Buckets) == 0 {
		return
	}

	// Get sorted bucket boundaries (excluding +Inf for display)
	bounds := make([]float64, 0, len(hv.Buckets))
	for bound := range hv.Buckets {
		if !math.IsInf(bound, 1) {
			bounds = append(bounds, bound)
		}
	}
	sort.Float64s(bounds)

	if len(bounds) == 0 {
		return
	}

	fmt.Fprintf(buf, "    distribution:\n")

	// Calculate bucket deltas for histogram bars
	var prevCount uint64
	maxDelta := uint64(0)
	deltas := make([]uint64, len(bounds))
	for i, bound := range bounds {
		count := hv.Buckets[bound]
		deltas[i] = count - prevCount
		if deltas[i] > maxDelta {
			maxDelta = deltas[i]
		}
		prevCount = count
	}

	// Write distribution bars
	maxBarWidth := 40
	for i, bound := range bounds {
		barWidth := 0
		if maxDelta > 0 {
			barWidth = int(float64(deltas[i]) / float64(maxDelta) * float64(maxBarWidth))
		}
		bar := repeatChar('#', barWidth)

		fmt.Fprintf(buf, "      <= %-10s |%-40s| %d\n",
			formatTextValue(bound), bar, deltas[i])
	}
}

// formatTextValue formats a float value for human-readable output.
func formatTextValue(v float64) string {
	if math.IsNaN(v) {
		return "NaN"
	}
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	if math.IsInf(v, -1) {
		return "-Inf"
	}

	// Use appropriate formatting based on magnitude
	absV := math.Abs(v)
	switch {
	case absV == 0:
		return "0"
	case absV < 0.001:
		return fmt.Sprintf("%.6f", v)
	case absV < 1:
		return fmt.Sprintf("%.4f", v)
	case absV < 1000:
		return fmt.Sprintf("%.2f", v)
	case absV < 1000000:
		return fmt.Sprintf("%.0f", v)
	default:
		return fmt.Sprintf("%.3e", v)
	}
}

// repeatChar returns a string with the given character repeated n times.
func repeatChar(c byte, n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = c
	}
	return string(b)
}
