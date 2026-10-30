// Package nightly is the nightly job (R45, P1): it deletes messages older
// than the retention period, applies changed redaction rules to stored
// messages, and vacuums. Page text is never logged.
package nightly

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/reparse"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/telemetry"
)

// Retention limits, in days (R45).
const (
	DefaultDays = 3650
	MinDays     = 30
	MaxDays     = 36500
)

const (
	daysKey      = "retention_days"
	lastRunKey   = "nightly_last_run"
	redactionKey = "redaction_hash"
	batch        = 1000
	deliveryDays = 30
	advisoryLock = 4242_0045 // one nightly run at a time
)

// Job runs the nightly work. At is the local time of day it runs (default
// 03:00, setting NIGHTLY_AT) in Location, the deployment time zone (R35a).
type Job struct {
	Store    *store.Store
	Network  *profile.Network
	Reparse  *reparse.Worker
	Location *time.Location
	At       time.Duration // since local midnight
	Now      func() time.Time
	Log      *slog.Logger
	// Tracer traces each run and its steps (R72); nil traces nothing.
	Tracer trace.Tracer
}

// ParseAt reads a "HH:MM" time of day.
func ParseAt(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, fmt.Errorf("NIGHTLY_AT must be HH:MM, e.g. 03:00")
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// runOn is the run time on the local date of t.
func (j *Job) runOn(t time.Time) time.Time {
	l := t.In(j.Location)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, j.Location).Add(j.At)
}

// NextRun is the first scheduled run after now.
func (j *Job) NextRun(now time.Time) time.Time {
	r := j.runOn(now)
	if !r.After(now) {
		r = j.runOn(now.In(j.Location).AddDate(0, 0, 1))
	}
	return r
}

// previousRun is the last scheduled run at or before now.
func (j *Job) previousRun(now time.Time) time.Time {
	r := j.runOn(now)
	if r.After(now) {
		r = j.runOn(now.In(j.Location).AddDate(0, 0, -1))
	}
	return r
}

// Cutoff is the start of the cut-off date for a run at runAt: messages sent
// before it are deleted. It is a calendar date in the deployment time zone
// (R45): the run's local date minus days.
func (j *Job) Cutoff(runAt time.Time, days int) time.Time {
	l := runAt.In(j.Location)
	return time.Date(l.Year(), l.Month(), l.Day()-days, 0, 0, 0, 0, j.Location)
}

// Days is the retention period in force.
func (j *Job) Days(ctx context.Context) (int, error) {
	v, err := j.Store.GetSetting(ctx, daysKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultDays, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultDays, nil
	}
	return n, nil
}

// SetDays saves a new retention period. It takes effect at the next run.
func (j *Job) SetDays(ctx context.Context, days int) error {
	if days < MinDays || days > MaxDays {
		return fmt.Errorf("retention must be %d to %d days", MinDays, MaxDays)
	}
	return j.Store.SetSetting(ctx, db.SetSettingParams{Key: daysKey, Value: strconv.Itoa(days)})
}

// Result is what one run did. It's kept for the admin screen and, from M5,
// the metrics endpoint (R52). It never holds page text.
type Result struct {
	At       time.Time `json:"at"`
	Cutoff   time.Time `json:"cutoff"`
	Deleted  int64     `json:"deleted"`
	Redacted int64     `json:"redacted"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
}

// LastResult is the most recent run's result, if any.
func (j *Job) LastResult(ctx context.Context) (*Result, error) {
	v, err := j.Store.GetSetting(ctx, lastRunKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return nil, nil
	}
	return &r, nil
}

// Run runs the job at each scheduled time until ctx ends. A run missed
// while the API was down is made up when it starts (ADR 0005).
func (j *Job) Run(ctx context.Context) {
	for ctx.Err() == nil {
		now := j.Now()
		last, err := j.LastResult(ctx)
		if err == nil && (last == nil || last.At.Before(j.previousRun(now))) {
			j.runLogged(ctx, now)
			// A failed run is recorded too, so this doesn't spin; the pause
			// covers a run refused because another holds the lock.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
			continue
		}
		wait := time.Until(j.NextRun(now))
		if err != nil {
			wait = time.Minute
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (j *Job) runLogged(ctx context.Context, now time.Time) {
	_, _ = j.RunOnce(ctx, now) // it logs the result, in its trace
}

// RunOnce does one run as if at now. Tests call it with a controllable
// clock (section 14). The result is recorded whether or not it failed.
func (j *Job) RunOnce(ctx context.Context, now time.Time) (res Result, err error) {
	ctx, span := telemetry.Or(j.Tracer).Start(ctx, "nightly", trace.WithNewRoot())
	defer func() {
		span.SetAttributes(attribute.Int64("pagerarchive.nightly.deleted", res.Deleted), attribute.Int64("pagerarchive.nightly.redacted", res.Redacted))
		if err != nil {
			span.SetAttributes(attribute.String("error.type", "failed"))
			span.SetStatus(codes.Error, "")
			j.Log.ErrorContext(ctx, "nightly job", "error", err.Error(), "deleted", res.Deleted, "redacted", res.Redacted)
		} else {
			j.Log.InfoContext(ctx, "nightly job", "deleted", res.Deleted, "redacted", res.Redacted, "cutoff", res.Cutoff.Format(time.DateOnly))
		}
		span.End()
	}()
	res = Result{At: now.UTC()}
	conn, err := j.Store.Pool.Acquire(ctx)
	if err != nil {
		return res, errors.New(store.SafeError(err))
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", advisoryLock).Scan(&locked); err != nil {
		return res, errors.New(store.SafeError(err))
	}
	if !locked {
		return res, errors.New("another nightly run is in progress")
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLock)

	err = j.run(ctx, now, &res)
	res.OK = err == nil
	if err != nil {
		res.Error = err.Error()
	}
	data, _ := json.Marshal(res)
	if serr := j.Store.SetSetting(ctx, db.SetSettingParams{Key: lastRunKey, Value: string(data)}); serr != nil && err == nil {
		err = errors.New(store.SafeError(serr))
	}
	return res, err
}

func (j *Job) run(ctx context.Context, now time.Time, res *Result) error {
	days, err := j.Days(ctx)
	if err != nil {
		return fmt.Errorf("retention setting: %s", store.SafeError(err))
	}
	tr := telemetry.Or(j.Tracer)
	// 1. Retention (R45): everything sent before the cut-off date.
	res.Cutoff = j.Cutoff(now, days)
	sctx, span := tr.Start(ctx, "nightly.retention")
	defer span.End() // already ended, unless it failed
	for {
		n, err := j.Store.DeleteMessagesBefore(sctx, db.DeleteMessagesBeforeParams{Cutoff: res.Cutoff, Batch: batch})
		if err != nil {
			return fmt.Errorf("retention: %s", store.SafeError(err))
		}
		res.Deleted += n
		if n < batch {
			break
		}
	}
	// Integrations' delivery records are kept for 30 days (ADR 0006 §1).
	if _, err := j.Store.PruneDeliveries(sctx, now.Add(-deliveryDays*24*time.Hour)); err != nil {
		return fmt.Errorf("delivery records: %s", store.SafeError(err))
	}
	span.End()
	// 2. Retroactive redaction (P1), when the rules changed since the last
	// time they were applied to every stored message.
	hash := RedactionHash(j.Network)
	if old, err := j.Store.GetSetting(ctx, redactionKey); err != nil || old != hash {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("redaction setting: %s", store.SafeError(err))
		}
		rctx, rspan := tr.Start(ctx, "nightly.redaction")
		n, err := j.redactAll(rctx)
		rspan.End()
		res.Redacted = n
		if err != nil {
			return err
		}
		if err := j.Store.SetSetting(ctx, db.SetSettingParams{Key: redactionKey, Value: hash}); err != nil {
			return fmt.Errorf("redaction setting: %s", store.SafeError(err))
		}
	}
	// 3. Vacuum, so deleted and replaced text doesn't linger in table pages
	// (P1). Write-ahead logs and backups are out of scope.
	vctx, vspan := tr.Start(ctx, "nightly.vacuum")
	defer vspan.End()
	for _, t := range []string{"messages", "receptions"} {
		if _, err := j.Store.Pool.Exec(vctx, "VACUUM (ANALYZE) "+t); err != nil {
			return fmt.Errorf("vacuum %s: %s", t, store.SafeError(err))
		}
	}
	return nil
}

// redactAll applies the current rules to every stored message, then
// re-parses the ones that changed so their parsed fields, job and location
// follow (P1, section 14).
func (j *Job) redactAll(ctx context.Context) (int64, error) {
	var after, changed int64
	for {
		rows, err := j.Store.MessagesForRedaction(ctx, db.MessagesForRedactionParams{AfterID: after, Batch: batch})
		if err != nil {
			return changed, fmt.Errorf("redaction: %s", store.SafeError(err))
		}
		var ids []int64
		for _, m := range rows {
			after = m.ID
			text := pipeline.Redact(j.Network, m.Text)
			if text == m.Text {
				continue
			}
			key := pipeline.DedupKey(pipeline.Prepared{Capcode: m.Capcode, Text: text})
			if err := j.Store.UpdateRedactedText(ctx, db.UpdateRedactedTextParams{ID: m.ID, Text: text, DedupKey: key}); err != nil {
				return changed, fmt.Errorf("redaction: %s", store.SafeError(err))
			}
			ids = append(ids, m.ID)
		}
		if j.Reparse != nil {
			if err := j.Reparse.ReparseIDs(ctx, ids); err != nil {
				return changed, fmt.Errorf("re-parse after redaction: %s", store.SafeError(err))
			}
		}
		changed += int64(len(ids))
		if len(rows) < batch {
			return changed, nil
		}
	}
}

// RedactionHash identifies the network's redaction rules.
func RedactionHash(n *profile.Network) string {
	h := sha256.New()
	for _, r := range n.Redactions {
		fmt.Fprintf(h, "%s\x00%s\x00%d\n", r.Name, r.Re.String(), r.Group)
	}
	return hex.EncodeToString(h.Sum(nil))
}
