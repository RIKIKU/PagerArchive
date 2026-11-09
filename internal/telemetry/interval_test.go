package telemetry

import "testing"

// ADR 0022: with OTEL_METRIC_EXPORT_INTERVAL unset, metrics go every 15 s,
// not the SDK's 60 s; set, the SDK's reading of it stands.
func TestMetricIntervalDefault(t *testing.T) {
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "")
	if n := len(readerOptions()); n != 1 || MetricInterval.Seconds() != 15 {
		t.Errorf("unset: %d options, interval %v; want WithInterval(15s)", n, MetricInterval)
	}
	t.Setenv("OTEL_METRIC_EXPORT_INTERVAL", "60000")
	if n := len(readerOptions()); n != 0 {
		t.Errorf("set to 60000: %d options, want none, so the SDK reads the setting", n)
	}
}
