// Package reparse re-parses stored messages when an alias, template or
// profile changes (R23a). Requests come from the reparse_requests table,
// written by any tool; the worker wakes on a PostgreSQL notification and
// also polls, so a change is never missed.
package reparse

import (
	"context"
	"log/slog"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/geocode"
	"github.com/RIKIKU/PagerArchive/internal/ingest"
	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
	"github.com/RIKIKU/PagerArchive/internal/streets"
)

const (
	channel    = "pagerarchive_reparse"
	hashKey    = "profile_hash"
	batchSize  = 500
	everything = "%"
)

// Worker re-parses messages.
type Worker struct {
	Store       *store.Store
	Network     *profile.Network
	Agencies    map[string]*profile.Agency
	Streets     *streets.Index
	Geo         *geocode.Geocoder
	ProfileHash string // hash of the profiles and address dataset in use
	Log         *slog.Logger
	Poll        time.Duration
}

// CheckProfiles queues a full re-parse if the profiles or address dataset
// changed since the last start (section 14: compared by file hashes).
func (w *Worker) CheckProfiles(ctx context.Context) error {
	old, err := w.Store.GetSetting(ctx, hashKey)
	if err == nil && old == w.ProfileHash {
		return nil
	}
	return w.Store.InTx(ctx, func(q *db.Queries) error {
		if err := q.RequestReparse(ctx, db.RequestReparseParams{CapcodePattern: everything, Reason: "profiles changed"}); err != nil {
			return err
		}
		return q.SetSetting(ctx, db.SetSettingParams{Key: hashKey, Value: w.ProfileHash})
	})
}

// Run processes requests until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := w.listen(ctx); err != nil && ctx.Err() == nil {
			w.Log.Error("re-parse worker", "error", store.SafeError(err))
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func (w *Worker) listen(ctx context.Context) error {
	conn, err := w.Store.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
		return err
	}
	for {
		if _, err := w.RunOnce(ctx); err != nil {
			return err
		}
		wait, cancel := context.WithTimeout(ctx, w.Poll)
		_, err := conn.Conn().WaitForNotification(wait)
		cancel()
		if err != nil && ctx.Err() != nil {
			return nil
		}
		if err != nil && wait.Err() == nil {
			return err // the connection failed, not the poll timeout
		}
	}
}

// RunOnce claims the queued requests and re-parses the matching messages.
// It returns how many messages it re-parsed.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	reqs, err := w.Store.ClaimReparseRequests(ctx)
	if err != nil || len(reqs) == 0 {
		return 0, err
	}
	patterns := map[string]bool{}
	for _, r := range reqs {
		patterns[r.CapcodePattern] = true
	}
	if patterns[everything] {
		patterns = map[string]bool{everything: true}
	}
	cands, err := w.candidates(ctx)
	if err != nil {
		w.requeue(ctx, patterns)
		return 0, err
	}
	n := 0
	for pattern := range patterns {
		var after int64
		for {
			rows, err := w.Store.MessagesToReparse(ctx, db.MessagesToReparseParams{Pattern: pattern, AfterID: after, Batch: batchSize})
			if err != nil {
				w.requeue(ctx, patterns)
				return n, err
			}
			for _, m := range rows {
				if err := w.reparse(ctx, cands, m); err != nil {
					w.requeue(ctx, patterns)
					return n, err
				}
				after = m.ID
				n++
			}
			if len(rows) < batchSize {
				break
			}
		}
	}
	w.Log.Info("re-parsed messages", "count", n, "requests", len(reqs))
	return n, nil
}

// candidates are every alias, for picking each message's best one.
func (w *Worker) candidates(ctx context.Context) ([]ingest.Candidate, error) {
	aliases, err := w.Store.AllAliases(ctx)
	if err != nil {
		return nil, err
	}
	cands := make([]ingest.Candidate, len(aliases))
	for i, a := range aliases {
		cands[i] = ingest.Candidate{ID: a.ID, Pattern: a.Pattern, AgencyProfile: a.AgencyProfile}
	}
	return cands, nil
}

// ReparseIDs re-parses (and re-places) the given messages now, e.g. after
// retroactive redaction changed their text (P1).
func (w *Worker) ReparseIDs(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	cands, err := w.candidates(ctx)
	if err != nil {
		return err
	}
	rows, err := w.Store.MessagesByIDs(ctx, ids)
	if err != nil {
		return err
	}
	for _, m := range rows {
		if err := w.reparse(ctx, cands, db.MessagesToReparseRow(m)); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) reparse(ctx context.Context, cands []ingest.Candidate, m db.MessagesToReparseRow) error {
	var matching []ingest.Candidate
	for _, c := range cands {
		if ingest.LikeMatch(c.Pattern, m.Capcode) {
			matching = append(matching, c)
		}
	}
	var aliasID *int64
	var agency *profile.Agency
	if best := ingest.BestAlias(matching); best != nil {
		aliasID = &best.ID
		if best.AgencyProfile != nil {
			agency = w.Agencies[*best.AgencyProfile]
		}
	}
	prep := pipeline.Prepared{Capcode: m.Capcode, Text: m.Text, Heartbeat: m.Heartbeat}
	if m.Priority != nil {
		prep.Priority = profile.Priority(*m.Priority)
	}
	f, err := ingest.FieldsOf(pipeline.Parse(w.Network, agency, w.Streets, prep))
	if err != nil {
		return err
	}
	if err := w.Store.UpdateParsed(ctx, db.UpdateParsedParams{
		ID: m.ID, AliasID: aliasID, Format: f.Format, JobNumber: f.JobNumber, EventType: f.EventType,
		Description: f.Description, Address: f.Address, Locality: f.Locality, GridRef: f.GridRef,
		PagingGroup: f.Group, Units: f.Units, Extra: f.Extra, ParseFailed: f.ParseFailed, AddressUnsplit: f.AddressUnsplit,
	}); err != nil {
		return err
	}
	return ingest.Locate(ctx, w.Geo, w.Store.Queries, m.ID, f)
}

// requeue puts claimed requests back after a failure, so none are lost.
func (w *Worker) requeue(ctx context.Context, patterns map[string]bool) {
	for p := range patterns {
		if err := w.Store.RequestReparse(ctx, db.RequestReparseParams{CapcodePattern: p, Reason: "retry"}); err != nil {
			w.Log.Error("re-queue re-parse", "error", store.SafeError(err))
		}
	}
}
