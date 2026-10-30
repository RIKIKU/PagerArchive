package client

import (
	"context"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics are the client's numbers, exported over OTLP (R74). Together they
// tell a stuck radio (lines stop) from a network problem (lines and pages
// go on, sends fail and pages buffer). nil counts nothing.
type Metrics struct {
	linesRead, pagesDecoded, pagesSent, sendFailures, sendRetries, pagesDropped metric.Int64Counter

	lastDecoded, lastSent atomic.Int64 // Unix nanoseconds; 0 for never
	radio                 atomic.Int32 // 1 while rtl_fm and multimon-ng run
}

// NewMetrics registers the client's instruments on m. buf is the buffer
// whose length is reported; radio says the client runs the radio pipeline
// (R13), so whether it's running is reported too.
func NewMetrics(m metric.Meter, buf *Buffer, radio bool) (*Metrics, error) {
	c := &Metrics{}
	var err error
	counter := func(name, unit, desc string) metric.Int64Counter {
		if err != nil {
			return nil
		}
		var ctr metric.Int64Counter
		ctr, err = m.Int64Counter(name, metric.WithUnit(unit), metric.WithDescription(desc))
		return ctr
	}
	c.linesRead = counter("pagerarchive.client.lines_read", "{line}", "Lines read from the decoder, pages or not.")
	c.pagesDecoded = counter("pagerarchive.client.pages_decoded", "{page}", "Lines that decoded to a page.")
	c.pagesSent = counter("pagerarchive.client.pages_sent", "{page}", "Pages the server accepted.")
	c.sendFailures = counter("pagerarchive.client.send_failures", "{attempt}", "Posts that failed: unreachable, refused or an error answer.")
	c.sendRetries = counter("pagerarchive.client.send_retries", "{attempt}", "Posts that were a page's second or later try.")
	c.pagesDropped = counter("pagerarchive.client.pages_dropped", "{page}", "Pages given up on, by reason: buffer_full (R15), rejected or clock_ahead.")
	if err != nil {
		return nil, err
	}
	buffered, err := m.Int64ObservableGauge("pagerarchive.client.pages_buffered", metric.WithUnit("{page}"),
		metric.WithDescription("Pages in memory waiting to be sent (R15)."))
	if err != nil {
		return nil, err
	}
	lastDecoded, err := m.Float64ObservableGauge("pagerarchive.client.last_decoded_timestamp", metric.WithUnit("s"),
		metric.WithDescription("When the last page was decoded, as a Unix time."))
	if err != nil {
		return nil, err
	}
	lastSent, err := m.Float64ObservableGauge("pagerarchive.client.last_sent_timestamp", metric.WithUnit("s"),
		metric.WithDescription("When the server last accepted a page, as a Unix time."))
	if err != nil {
		return nil, err
	}
	observed := []metric.Observable{buffered, lastDecoded, lastSent}
	var running metric.Int64ObservableGauge
	if radio {
		if running, err = m.Int64ObservableGauge("pagerarchive.client.radio_running",
			metric.WithDescription("1 while rtl_fm and multimon-ng are running, 0 when they've stopped (R13).")); err != nil {
			return nil, err
		}
		observed = append(observed, running)
	}
	_, err = m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(buffered, int64(buf.Len()))
		if t := c.lastDecoded.Load(); t != 0 {
			o.ObserveFloat64(lastDecoded, float64(t)/1e9)
		}
		if t := c.lastSent.Load(); t != 0 {
			o.ObserveFloat64(lastSent, float64(t)/1e9)
		}
		if radio {
			o.ObserveInt64(running, int64(c.radio.Load()))
		}
		return nil
	}, observed...)
	return c, err
}

func (c *Metrics) add(ctr metric.Int64Counter, attrs ...attribute.KeyValue) {
	if c != nil {
		ctr.Add(context.Background(), 1, metric.WithAttributes(attrs...))
	}
}

func (c *Metrics) lineRead() {
	if c != nil {
		c.add(c.linesRead)
	}
}

func (c *Metrics) decoded(at time.Time) {
	if c != nil {
		c.add(c.pagesDecoded)
		c.lastDecoded.Store(at.UnixNano())
	}
}

func (c *Metrics) sent() {
	if c != nil {
		c.add(c.pagesSent)
		c.lastSent.Store(time.Now().UnixNano())
	}
}

func (c *Metrics) failed() {
	if c != nil {
		c.add(c.sendFailures)
	}
}

func (c *Metrics) retried() {
	if c != nil {
		c.add(c.sendRetries)
	}
}

func (c *Metrics) dropped(reason string) {
	if c != nil {
		c.add(c.pagesDropped, attribute.String("reason", reason))
	}
}

// Radio says whether the radio pipeline is running.
func (c *Metrics) Radio(running bool) {
	if c == nil {
		return
	}
	if running {
		c.radio.Store(1)
	} else {
		c.radio.Store(0)
	}
}
