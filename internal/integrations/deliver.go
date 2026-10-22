package integrations

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/search"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Delivery timing (R51a, ADR 0006 §3).
const (
	// Cutoff: no attempt starts later than this after the first receive time.
	Cutoff = 5 * time.Minute
	// PauseAfter undelivered pages in a row pause an integration.
	PauseAfter  = 3
	maxParallel = 8
	idlePoll    = time.Minute
	workerLock  = 4242_0047 // one delivery worker at a time
)

// backoff is the wait before attempt n+1 after n failed attempts.
func backoff(attempts int32) time.Duration {
	steps := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second}
	if int(attempts) <= len(steps) && attempts > 0 {
		return steps[attempts-1]
	}
	return time.Minute
}

// Delivery statuses.
const (
	statusPending   = "pending"
	statusDelivered = "delivered"
	statusFailed    = "failed"
	statusTooOld    = "too_old"
	statusCancelled = "cancelled"
)

// Stats counts deliveries since the API started, for the metrics (R52).
type Stats struct {
	mu sync.Mutex
	n  map[[2]string]uint64
}

func (s *Stats) add(kind, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n == nil {
		s.n = map[[2]string]uint64{}
	}
	s.n[[2]string{kind, result}]++
}

// Counts is deliveries by [kind, result].
func (s *Stats) Counts() map[[2]string]uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[[2]string]uint64, len(s.n))
	for k, v := range s.n {
		out[k] = v
	}
	return out
}

// message reads a stored message as the list shows it (ignored aliases
// included: the selection is explicit).
func (s *Service) message(ctx context.Context, q *db.Queries, id int64) (*db.SearchMessagesRow, error) {
	p, err := search.Filter{Ignored: true}.Params(s.Network, nil, 1)
	if err != nil {
		return nil, err
	}
	p.OnlyIds = []int64{id}
	rows, err := q.SearchMessages(ctx, p)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// Enqueue queues a newly stored page for every integration it matches, in
// the ingest transaction q, so a stored page and its deliveries commit
// together (ADR 0006 §3). Duplicates of a page never get here (R51b).
func (s *Service) Enqueue(ctx context.Context, q *db.Queries, messageID int64) error {
	if !s.Enabled() {
		return nil
	}
	m, err := s.message(ctx, q, messageID)
	if err != nil || m == nil {
		return err
	}
	cands, err := q.MatchingIntegrations(ctx, db.MatchingIntegrationsParams{Capcode: m.Capcode, AliasID: m.AliasID})
	if err != nil {
		return err
	}
	now := s.now()
	// R51a: a page already older than the cut-off when it arrives is not sent.
	late := !now.Before(m.SentAt.Add(Cutoff))
	for _, c := range cands {
		l, err := decode(db.Integration{ID: c.ID, Config: []byte("{}"), Filter: c.Filter})
		if err != nil {
			return err
		}
		if !l.filter.Matches(*m, s.Network, c.OwnerRole == adminRole) {
			continue
		}
		p := db.InsertDeliveryParams{IntegrationID: c.ID, MessageID: messageID, Status: statusPending, NextAttemptAt: &now}
		if late {
			p.Status, p.NextAttemptAt = statusTooOld, nil
			s.Stats.add(c.Kind, statusTooOld)
		}
		if err := q.InsertDelivery(ctx, p); err != nil {
			return err
		}
	}
	return nil
}

// Wake starts sending at once, e.g. after a page was stored.
func (s *Service) Wake() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	w := s.wake
	s.mu.Unlock()
	select {
	case w <- struct{}{}:
	default:
	}
}

func (s *Service) wakeChan() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.wake == nil {
		s.wake = make(chan struct{}, 1)
	}
	return s.wake
}

// Run sends due deliveries until ctx ends. Pending deliveries survive a
// restart in the database, and are picked up again here.
func (s *Service) Run(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	conn, err := s.Store.Pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()
	for {
		var ok bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", workerLock).Scan(&ok); err == nil && ok {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
	defer func() { _, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", workerLock) }()
	wake := s.wakeChan()
	for {
		s.ProcessDue(ctx)
		wait := idlePoll
		if next, err := s.Store.NextDeliveryDue(ctx); err == nil { // no pending rows: NULL, an error
			wait = min(max(next.Sub(s.now()), 100*time.Millisecond), idlePoll)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			s.inflight.Wait()
			return
		case <-wake:
		case <-t.C:
		}
		t.Stop()
	}
}

// ProcessDue starts sending every due delivery: one integration's in
// order, different integrations in parallel. It returns without waiting.
func (s *Service) ProcessDue(ctx context.Context) {
	due, err := s.Store.DueDeliveries(ctx, s.now())
	if err != nil {
		if ctx.Err() == nil {
			s.Log.Error("due deliveries", "error", dbErr("due", err).Error())
		}
		return
	}
	byIntegration := map[int64][]db.DueDeliveriesRow{}
	var order []int64
	for _, d := range due {
		if _, seen := byIntegration[d.IntegrationID]; !seen {
			order = append(order, d.IntegrationID)
		}
		byIntegration[d.IntegrationID] = append(byIntegration[d.IntegrationID], d)
	}
	s.mu.Lock()
	if s.busy == nil {
		s.busy = map[int64]bool{}
	}
	for _, id := range order {
		if s.busy[id] || len(s.busy) >= maxParallel {
			continue
		}
		s.busy[id] = true
		s.inflight.Add(1)
		go s.deliverAll(ctx, id, byIntegration[id])
	}
	s.mu.Unlock()
}

// Drain sends everything due now and waits for it (tests).
func (s *Service) Drain(ctx context.Context) {
	s.ProcessDue(ctx)
	s.inflight.Wait()
}

func (s *Service) deliverAll(ctx context.Context, id int64, due []db.DueDeliveriesRow) {
	defer func() {
		s.mu.Lock()
		delete(s.busy, id)
		s.mu.Unlock()
		s.inflight.Done()
		s.Wake() // its next due rows, and anything that waited for a slot
	}()
	sort.SliceStable(due, func(i, j int) bool { return due[i].MessageID < due[j].MessageID })
	for _, d := range due {
		if ctx.Err() != nil {
			return
		}
		if stop := s.deliverOne(ctx, d); stop {
			return
		}
	}
}

// deliverOne makes one attempt. It reports whether the integration's other
// due rows should wait (it was paused, deleted, or is failing right now).
func (s *Service) deliverOne(ctx context.Context, d db.DueDeliveriesRow) bool {
	r, err := s.Store.IntegrationByID(ctx, d.IntegrationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true // deleted: its deliveries went with it
	}
	if err != nil {
		s.Log.Error("delivery: integration", "integration", d.IntegrationID, "error", dbErr("load", err).Error())
		return true
	}
	l, err := fromByID(r)
	if err != nil {
		return true
	}
	finish := func(status string, res result) {
		s.finish(ctx, l, d, status, d.Attempts, res)
	}
	// Paused, or the owner was deactivated (R51): stop.
	if l.row.Status != "active" || l.ownerStatus != "active" {
		finish(statusCancelled, result{})
		return false
	}
	m, err := s.message(ctx, s.Store.Queries, d.MessageID)
	if err != nil {
		s.Log.Error("delivery: message", "integration", l.row.ID, "message", d.MessageID, "error", dbErr("load", err).Error())
		return true
	}
	if m == nil {
		finish(statusCancelled, result{}) // deleted meanwhile
		return false
	}
	now := s.now()
	deadline := m.SentAt.Add(Cutoff)
	if !now.Before(deadline) {
		s.tooOld(ctx, l, d, result{err: "skipped: too old"})
		return false
	}
	sec, err := s.secretsOf(l.row)
	if err != nil {
		res := result{outcome: refused, err: err.Error()}
		s.failed(ctx, l, d, d.Attempts, res)
		return false
	}
	p := s.pageOf(*m, l.ownerRole == adminRole)
	actx, cancel := context.WithTimeout(ctx, AttemptTimeout)
	res := s.send(actx, l, sec, p, false)
	cancel()
	attempts := d.Attempts + 1
	switch res.outcome {
	case delivered:
		s.finish(ctx, l, d, statusDelivered, attempts, res)
		if err := s.Store.IntegrationDelivered(ctx, db.IntegrationDeliveredParams{ID: l.row.ID, At: s.now()}); err != nil {
			s.Log.Error("delivery: record", "integration", l.row.ID, "error", dbErr("record", err).Error())
		}
		return false
	case retryable:
		next := s.now().Add(backoff(attempts))
		if res.retryAfter > 0 && s.now().Add(res.retryAfter).After(next) {
			next = s.now().Add(res.retryAfter)
		}
		if !next.Before(deadline) {
			d.Attempts = attempts
			s.tooOld(ctx, l, d, res)
			return false
		}
		s.logAttempt(l, d, "retry", res)
		lr, le := strp(res.status), strp(res.err)
		if err := s.Store.RetryDelivery(ctx, db.RetryDeliveryParams{IntegrationID: l.row.ID, MessageID: d.MessageID, Attempts: attempts,
			NextAttemptAt: next, LastResult: lr, LastError: le, At: s.now()}); err != nil {
			s.Log.Error("delivery: retry", "integration", l.row.ID, "error", dbErr("retry", err).Error())
		}
		if err := s.Store.IntegrationAttemptFailed(ctx, db.IntegrationAttemptFailedParams{ID: l.row.ID, Error: res.err, At: s.now()}); err != nil {
			s.Log.Error("delivery: record", "integration", l.row.ID, "error", dbErr("record", err).Error())
		}
		return false
	default: // refused, or sent with no answer (never retried, owner Q2)
		s.failed(ctx, l, d, attempts, res)
		return false
	}
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (s *Service) logAttempt(l loaded, d db.DueDeliveriesRow, status string, res result) {
	s.Log.Info("delivery", "integration", l.row.ID, "kind", l.row.Kind, "message", d.MessageID, "status", status, "result", res.status, "error", res.err)
}

// finish takes a delivery off the queue.
func (s *Service) finish(ctx context.Context, l loaded, d db.DueDeliveriesRow, status string, attempts int32, res result) {
	s.logAttempt(l, d, status, res)
	if err := s.Store.FinishDelivery(ctx, db.FinishDeliveryParams{Status: status, Attempts: attempts, LastResult: strp(res.status), LastError: strp(res.err),
		At: s.now(), IntegrationID: l.row.ID, MessageID: d.MessageID}); err != nil {
		s.Log.Error("delivery: finish", "integration", l.row.ID, "error", dbErr("finish", err).Error())
		return
	}
	s.Stats.add(l.row.Kind, status)
}

// tooOld: the cut-off passed. It counts as an undelivered page only if it
// was attempted: a page that waited because the API was down isn't the
// endpoint's fault.
func (s *Service) tooOld(ctx context.Context, l loaded, d db.DueDeliveriesRow, res result) {
	if res.err == "" {
		res.err = "skipped: too old"
	}
	s.finish(ctx, l, d, statusTooOld, d.Attempts, res)
	if d.Attempts > 0 {
		s.pageFailed(ctx, l, res.err)
	}
}

func (s *Service) failed(ctx context.Context, l loaded, d db.DueDeliveriesRow, attempts int32, res result) {
	s.finish(ctx, l, d, statusFailed, attempts, res)
	s.pageFailed(ctx, l, res.err)
}

// pageFailed counts an undelivered page, and pauses the integration after
// PauseAfter in a row (R51, A31).
func (s *Service) pageFailed(ctx context.Context, l loaded, msg string) {
	if msg == "" {
		msg = "not delivered"
	}
	r, err := s.Store.IntegrationPageFailed(ctx, db.IntegrationPageFailedParams{ID: l.row.ID, Error: msg, At: s.now()})
	if err != nil {
		s.Log.Error("delivery: record", "integration", l.row.ID, "error", dbErr("record", err).Error())
		return
	}
	if r.FailedPages >= PauseAfter && r.Status == "active" {
		if err := s.pause(ctx, l.row.ID, "failing"); err != nil {
			s.Log.Error("delivery: pause", "integration", l.row.ID, "error", err.Error())
		}
	}
}
