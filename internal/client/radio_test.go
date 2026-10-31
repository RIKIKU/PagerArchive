package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/RIKIKU/PagerArchive/internal/radio"
)

// hungExporter never finishes an export, like a collector that has hung.
type hungExporter struct {
	release   chan struct{}
	exporting chan struct{}
}

func (e *hungExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return sdkmetric.DefaultTemporalitySelector(k)
}
func (e *hungExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}
func (e *hungExporter) Export(context.Context, *metricdata.ResourceMetrics) error {
	select {
	case e.exporting <- struct{}{}:
	default:
	}
	<-e.release
	return nil
}
func (e *hungExporter) ForceFlush(context.Context) error { return nil }
func (e *hungExporter) Shutdown(context.Context) error   { return nil }

// Measuring the radio's audio never waits for the metrics side: while the
// exporter hangs, the audio still reaches multimon-ng at full speed and
// unchanged (R74a).
func TestRadioMeasuringNeverBlocks(t *testing.T) {
	hung := &hungExporter{release: make(chan struct{}), exporting: make(chan struct{}, 1)}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(hung, sdkmetric.WithInterval(10*time.Millisecond))))
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = mp.Shutdown(ctx)
	}()
	defer close(hung.release)
	h := radio.NewHealth(time.Now)
	if _, err := NewMetrics(mp.Meter("test"), NewBuffer(10), h); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hung.exporting:
	case <-time.After(5 * time.Second):
		t.Fatal("the exporter was never called")
	}

	in := make([]byte, 60*2*radio.SampleRate) // a minute of audio
	_, _ = rand.Read(in)
	var out bytes.Buffer
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- h.Pass(&out, bytes.NewReader(in)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the audio stalled with the exporter hung: %d of %d bytes reached multimon-ng in 5 s", h.Bytes(), len(in))
	}
	if !bytes.Equal(out.Bytes(), in) {
		t.Fatal("multimon-ng got different audio from what rtl_fm wrote")
	}
	t.Logf("R74a: a minute of audio passed unchanged in %v while the metrics exporter hung", time.Since(start).Round(time.Millisecond))
}

// rtl_fm's messages leave only as counts by kind, never their text (R70);
// the radio's other numbers are reported too.
func TestRadioExportsKindsOnly(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	h := radio.NewHealth(time.Now)
	if _, err := NewMetrics(mp.Meter("test"), NewBuffer(10), h); err != nil {
		t.Fatal(err)
	}
	h.Watch(strings.NewReader("cb transfer status: 1, canceling...\nUsing device 0: Generic RTL2832U OEM SN: 77770001\nSTANDIN-UNKNOWN secret words\n"), io.Discard)
	_ = h.Pass(io.Discard, bytes.NewReader(make([]byte, 4410)))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int64{}
	seen := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			seen[m.Name] = true
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, dp := range sum.DataPoints {
					for _, kv := range dp.Attributes.ToSlice() {
						v := kv.Value.Emit()
						if strings.Contains(v, "secret") || strings.Contains(v, "77770001") || strings.Contains(v, "canceling") {
							t.Errorf("%s exports rtl_fm's text: %s=%q", m.Name, kv.Key, v)
						}
						if m.Name == "pagerarchive.client.radio.messages" {
							if !slices.Contains(radio.Kinds, v) {
								t.Errorf("message kind %q isn't one of %v", v, radio.Kinds)
							}
							kinds[v] = dp.Value
						}
					}
				}
			}
		}
	}
	if kinds[radio.USBTransferFailed] != 1 || kinds[radio.Other] != 2 || len(kinds) != len(radio.Kinds) {
		t.Errorf("message counts %v, want usb_transfer_failed 1, other 2 and every kind reported", kinds)
	}
	for _, name := range []string{"pagerarchive.client.radio_running", "pagerarchive.client.radio.audio", "pagerarchive.client.radio.level", "pagerarchive.client.radio.silent"} {
		if !seen[name] {
			t.Errorf("no %s", name)
		}
	}
	if !t.Failed() {
		t.Logf("R74a: rtl_fm's messages exported as counts by kind only: %v", kinds)
	}
}

// A client reading stdin has no radio, so no radio health (R74a).
func TestStdinClientHasNoRadioHealth(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	if _, err := NewMetrics(mp.Meter("test"), NewBuffer(10), nil); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if strings.Contains(m.Name, "radio") {
				t.Errorf("a stdin client reports %s", m.Name)
			}
		}
	}
}
