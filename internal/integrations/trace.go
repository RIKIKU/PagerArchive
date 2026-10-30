package integrations

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

// Deliveries run after the page's request has ended, from the queue, so
// each attempt's span is a child of the page's ingest span, found here by
// message ID: one trace then shows the page from the receiver to every
// delivery and retry (R71, ADR 0013 §5). Only span contexts are kept, in
// memory, for as long as a delivery can still be attempted; after a
// restart, an attempt starts its own trace.
type parents struct {
	mu sync.Mutex
	m  map[int64]parent
}

type parent struct {
	sc      trace.SpanContext
	expires time.Time
}

// maxParents bounds the map; hobby scale is about a page a minute.
const maxParents = 10000

func (p *parents) put(id int64, sc trace.SpanContext, now time.Time) {
	if !sc.IsValid() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[int64]parent{}
	}
	if len(p.m) >= maxParents {
		for k, v := range p.m {
			if now.After(v.expires) || len(p.m) >= maxParents {
				delete(p.m, k)
			}
		}
	}
	p.m[id] = parent{sc: sc, expires: now.Add(Cutoff + time.Minute)}
}

func (p *parents) get(id int64, now time.Time) (trace.SpanContext, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.m[id]
	if !ok || now.After(v.expires) {
		return trace.SpanContext{}, false
	}
	return v.sc, true
}

// startAttempt starts a delivery attempt's span, under the page's ingest
// span when it's known. It carries the destination's kind and the attempt's
// number and result, never the integration, its owner or its address.
func (s *Service) startAttempt(ctx context.Context, d db.DueDeliveriesRow) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{trace.WithAttributes(attribute.Int("pagerarchive.delivery.attempt", int(d.Attempts)+1))}
	if sc, ok := s.parents.get(d.MessageID, s.now()); ok {
		ctx = trace.ContextWithSpanContext(ctx, sc)
	} else {
		opts = append(opts, trace.WithNewRoot())
	}
	return telemetry.Or(s.Tracer).Start(ctx, "integration.deliver", opts...)
}

// attemptResult records an attempt's result on its span: delivered, retry,
// failed, too_old or cancelled, and for a failure its kind.
func attemptResult(ctx context.Context, status string, res result) {
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("pagerarchive.delivery.result", status))
	if status == statusDelivered || status == statusCancelled {
		return
	}
	kind := map[outcome]string{retryable: "retryable", refused: "refused", noAnswer: "no_answer"}[res.outcome]
	if res.outcome == delivered {
		kind = status
	}
	span.SetAttributes(attribute.String("error.type", telemetry.ErrorKind(kind)))
	if res.status != "" {
		span.SetAttributes(attribute.String("error.type", telemetry.ErrorKind(res.status)))
	}
	span.SetStatus(codes.Error, "")
}
