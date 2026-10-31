package telemetry

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// A64: however often exports fail, the line saying so comes at most once a
// minute, and counts what was dropped since the last one.
func TestDropLogOnceAMinute(t *testing.T) {
	var out bytes.Buffer
	now := time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC)
	d := &dropLog{log: slog.New(slog.NewTextHandler(&out, nil)), every: time.Minute, now: func() time.Time { return now }}
	// A failure every 5 s for 5 minutes, 10 spans each.
	for i := 0; i < 60; i++ {
		d.failed("traces", 10, errors.New("connection refused"))
		now = now.Add(5 * time.Second)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("%d lines in 5 minutes, want 5:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], "dropped=10") || !strings.Contains(lines[1], "dropped=120") {
		t.Errorf("counts: %q, %q", lines[0], lines[1])
	}
	t.Logf("A64: 60 failed exports in 5 minutes gave %d lines: %s", len(lines), lines[1])
}
