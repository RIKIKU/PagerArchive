// Package client reads decoder output, buffers pages in memory and posts
// them to the server (SPEC.md R12–R15). Page text is never written to a file
// or logged; it goes to stdout only when PrintPages is on (R13a), and never
// into telemetry (R73).
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/RIKIKU/PagerArchive/internal/decoder"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

// Page is one page waiting to be posted. Its JSON is the server's
// PageRequest.
type Page struct {
	ClientID   string    `json:"client_id"`
	Capcode    string    `json:"capcode"`
	Text       string    `json:"text"`
	ReceivedAt time.Time `json:"received_at"`
	Function   int       `json:"function"`
	Baud       int       `json:"baud"`

	trace *pageTrace // nil for a page that isn't traced
}

// pageTrace is a page's trace, from the decoded line to the server's
// answer (R71): the root "page" span, and its "buffer" span while it waits.
type pageTrace struct {
	ctx      context.Context
	root     trace.Span
	buffer   trace.Span
	attempts int
}

// end ends the page's trace with how it ended.
func (t *pageTrace) end(outcome string, errKind string) {
	if t == nil {
		return
	}
	t.buffer.End()
	t.root.SetAttributes(attribute.String("pagerarchive.ingest.outcome", outcome))
	if errKind != "" {
		t.root.SetAttributes(attribute.String("error.type", errKind))
		t.root.SetStatus(codes.Error, "")
	}
	t.root.End()
}

// Buffer is a bounded in-memory FIFO. When full it drops the oldest page
// (section 14). It is never written to disk (R15).
type Buffer struct {
	mu      sync.Mutex
	items   []Page
	max     int
	dropped int
	ready   chan struct{}
	// Metrics counts dropped pages; nil counts nothing.
	Metrics *Metrics
}

// NewBuffer returns a buffer holding at most max pages.
func NewBuffer(max int) *Buffer {
	return &Buffer{max: max, ready: make(chan struct{}, 1)}
}

// Push adds a page, dropping the oldest if the buffer is full.
func (b *Buffer) Push(p Page) (dropped bool) {
	b.mu.Lock()
	if len(b.items) >= b.max {
		b.items[0].trace.end("dropped", "buffer_full")
		b.items = b.items[1:]
		b.dropped++
		dropped = true
	}
	b.items = append(b.items, p)
	b.mu.Unlock()
	if dropped {
		b.Metrics.dropped("buffer_full")
	}
	select {
	case b.ready <- struct{}{}:
	default:
	}
	return dropped
}

// Peek returns the oldest page without removing it.
func (b *Buffer) Peek() (Page, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == 0 {
		return Page{}, false
	}
	return b.items[0], true
}

// Pop removes the oldest page if it is still p (it may have been dropped
// while being posted).
func (b *Buffer) Pop(p Page) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) > 0 && b.items[0] == p {
		b.items = b.items[1:]
	}
}

// Len is the number of buffered pages.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

// Ready is signalled when a page is pushed.
func (b *Buffer) Ready() <-chan struct{} { return b.ready }

// Reader turns decoder lines into buffered pages.
type Reader struct {
	ClientID   string
	Adapter    decoder.Adapter
	Buffer     *Buffer
	Now        func() time.Time
	PrintPages bool
	Stdout     io.Writer
	Log        *slog.Logger
	// Tracer starts each page's trace (R71) and Metrics counts lines and
	// pages (R74); nil does neither.
	Tracer  trace.Tracer
	Metrics *Metrics
}

// Run reads lines until r ends. Lines that aren't pages are dropped silently.
func (rd *Reader) Run(r io.Reader) error {
	tracer := telemetry.Or(rd.Tracer)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		now := rd.Now().UTC() // receive time: as soon as the line arrives
		rd.Metrics.lineRead()
		rec, ok := rd.Adapter.Decode(sc.Text())
		if !ok {
			continue
		}
		// The page's trace starts when its line arrived (R71).
		ctx, root := tracer.Start(context.Background(), "page", trace.WithNewRoot(), trace.WithTimestamp(now))
		_, decode := tracer.Start(ctx, "decode", trace.WithTimestamp(now))
		decode.End()
		rd.Metrics.decoded(now)
		if rd.PrintPages {
			// The console only (R13a): never a log line, so never exported.
			fmt.Fprintf(rd.Stdout, "%s %s %s\n", now.Format(time.RFC3339), rec.Capcode, rec.Text)
		}
		_, buffer := tracer.Start(ctx, "buffer")
		if rd.Buffer.Push(Page{
			ClientID:   rd.ClientID,
			Capcode:    rec.Capcode,
			Text:       rec.Text,
			ReceivedAt: now,
			Function:   rec.Function,
			Baud:       rec.Baud,
			trace:      &pageTrace{ctx: ctx, root: root, buffer: buffer},
		}) {
			rd.Log.WarnContext(ctx, "buffer full: dropped the oldest page")
		}
	}
	return sc.Err()
}

// ErrClockAhead means the server rejected a page because its receive time
// is more than 60 s ahead of the server's clock.
var ErrClockAhead = errors.New("receive time ahead of the server's clock")

// Sender posts buffered pages in order, retrying with backoff.
type Sender struct {
	URL        string // server base URL
	Key        string
	HTTP       *http.Client
	Buffer     *Buffer
	Log        *slog.Logger
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// Tracer traces each attempt, and its trace context goes with the post
	// (W3C traceparent) so the server continues the page's trace (R71);
	// Metrics counts sends (R74). nil does neither.
	Tracer  trace.Tracer
	Metrics *Metrics
}

// Run posts pages until ctx ends.
func (s *Sender) Run(ctx context.Context) {
	backoff := s.MinBackoff
	for {
		p, ok := s.Buffer.Peek()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.Buffer.Ready():
				continue
			}
		}
		outcome, retry, err := s.post(ctx, p)
		switch {
		case err == nil:
			s.Buffer.Pop(p)
			p.trace.end(outcome, "")
			s.Metrics.sent()
			backoff = s.MinBackoff
			continue
		case errors.Is(err, ErrClockAhead):
			// Dropped like any rejected page; the warning says how to fix it.
			s.Log.WarnContext(p.ctx(), "page rejected by server, dropped: this client's clock is more than 60 s ahead of the server's; check NTP")
			s.Buffer.Pop(p)
			p.trace.end("refused", "clock_ahead")
			s.Metrics.dropped("clock_ahead")
			continue
		case !retry:
			// The server will never accept it; don't block the queue.
			s.Log.ErrorContext(p.ctx(), "page rejected by server, dropped", "error", err.Error())
			s.Buffer.Pop(p)
			p.trace.end("refused", "rejected")
			s.Metrics.dropped("rejected")
			continue
		}
		s.Log.WarnContext(p.ctx(), "server unreachable, will retry", "error", err.Error(), "buffered", s.Buffer.Len(), "retry_in", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, s.MaxBackoff)
	}
}

func (p Page) ctx() context.Context {
	if p.trace == nil {
		return context.Background()
	}
	return p.trace.ctx
}

// post sends one page, as one "send" span of its trace. retry says whether
// a failure is worth retrying. Errors never contain page text.
func (s *Sender) post(ctx context.Context, p Page) (outcome string, retry bool, err error) {
	start := time.Now()
	attempt := 1
	if p.trace != nil {
		p.trace.attempts++
		attempt = p.trace.attempts
		ctx = trace.ContextWithSpanContext(ctx, trace.SpanContextFromContext(p.trace.ctx))
	}
	if attempt > 1 {
		s.Metrics.retried()
	}
	ctx, span := telemetry.Or(s.Tracer).Start(ctx, "send", trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.Int("pagerarchive.send.attempt", attempt), attribute.String("http.request.method", http.MethodPost)))
	defer span.End()
	status, outcome, retry, err := s.postOnce(ctx, p)
	if status != 0 {
		span.SetAttributes(attribute.Int("http.response.status_code", status))
	}
	if err != nil {
		kind := "unreachable"
		if status != 0 {
			kind = fmt.Sprintf("HTTP %d", status)
		}
		span.SetAttributes(attribute.String("error.type", kind))
		span.SetStatus(codes.Error, "")
		s.Metrics.failed()
		return "", retry, err
	}
	if p.trace != nil {
		// Waiting in the buffer lasted until the post that worked began:
		// the time the server was unreachable shows as buffering (R15, A62).
		p.trace.buffer.End(trace.WithTimestamp(start))
	}
	return outcome, false, nil
}

// postOnce makes the request. ctx carries the attempt's span, whose
// context is sent as traceparent.
func (s *Sender) postOnce(ctx context.Context, p Page) (status int, outcome string, retry bool, err error) {
	body, err := json.Marshal(p)
	if err != nil {
		return 0, "", false, fmt.Errorf("encode page")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.URL, "/")+"/api/v1/pages", bytes.NewReader(body))
	if err != nil {
		return 0, "", false, fmt.Errorf("build request: invalid server URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Key)
	telemetry.TraceContext.Inject(ctx, propagation.HeaderCarrier(req.Header))
	resp, err := s.HTTP.Do(req)
	if err != nil {
		// A *url.Error names the URL and cause, never the request body.
		return 0, "", true, fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	var answer struct {
		Code    string `json:"code"`
		Outcome string `json:"outcome"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&answer)
	switch {
	case resp.StatusCode == http.StatusUnprocessableEntity && answer.Code == "clock_ahead":
		return resp.StatusCode, "", false, ErrClockAhead
	case resp.StatusCode < 300:
		return resp.StatusCode, answer.Outcome, false, nil
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode >= 500, resp.StatusCode == http.StatusUnauthorized:
		// 401: a key being rotated shouldn't lose pages; keep retrying.
		return resp.StatusCode, "", true, fmt.Errorf("server returned %d", resp.StatusCode)
	default:
		return resp.StatusCode, "", false, fmt.Errorf("server returned %d", resp.StatusCode)
	}
}
