package ingest

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/neardup"
	"github.com/RIKIKU/PagerArchive/internal/pipeline"
	"github.com/RIKIKU/PagerArchive/internal/profile"
	"github.com/RIKIKU/PagerArchive/internal/store"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// reportBatch is how many messages the voting section reads at a time.
const reportBatch = 500

// DedupReport counts what dedup-report shows (R19b), for the messages sent
// since a time, in one read-only transaction. Each copy is parsed with its
// capcode's agency profile now, as ingest would parse it (without the
// address dataset, so an address's split isn't judged). It holds counts
// only: no text, capcode or client ID leaves it (P1).
func DedupReport(ctx context.Context, st *store.Store, net *profile.Network, agencies map[string]*profile.Agency, ratio float64, since time.Time) (*neardup.Report, error) {
	var rep *neardup.Report
	err := st.ReadOnly(ctx, func(q *db.Queries) error {
		var err error
		rep, err = dedupReport(ctx, q, net, agencies, ratio, since)
		return err
	})
	return rep, err
}

func dedupReport(ctx context.Context, q *db.Queries, net *profile.Network, agencies map[string]*profile.Agency, ratio float64, since time.Time) (*neardup.Report, error) {
	aliases, err := q.AllAliases(ctx)
	if err != nil {
		return nil, fmt.Errorf("aliases: %s", store.SafeError(err))
	}
	cands := make([]Candidate, len(aliases))
	for i, a := range aliases {
		cands[i] = Candidate{ID: a.ID, Pattern: a.Pattern, AgencyProfile: a.AgencyProfile, Ignored: a.Ignored}
	}
	agencyOf := map[string]*profile.Agency{}
	agency := func(capcode string) *profile.Agency {
		if a, ok := agencyOf[capcode]; ok {
			return a
		}
		var matching []Candidate
		for _, c := range cands {
			if LikeMatch(c.Pattern, capcode) {
				matching = append(matching, c)
			}
		}
		var a *profile.Agency
		if best := BestAlias(matching); best != nil && best.AgencyProfile != nil {
			a = agencies[*best.AgencyProfile]
		}
		agencyOf[capcode] = a
		return a
	}
	quality := func(capcode, text string, heartbeat bool) neardup.Quality {
		// Each copy's priority comes from its own marker (R5), as ingest's does.
		p := pipeline.Prepared{Capcode: capcode, Text: text, Heartbeat: heartbeat}
		if rs := []rune(text); len(rs) >= 2 {
			p.Priority = net.Markers[string(rs[:2])]
		}
		return QualityOf(pipeline.Parse(net, agency(capcode), nil, p))
	}

	rep := neardup.NewReport(ratio)
	rep.RedactionMarker = pipeline.Redacted
	variants, err := q.DedupReportVariants(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("near duplicates: %s", store.SafeError(err))
	}
	for _, v := range variants {
		rep.AddKept(v.MessageText, v.VariantText,
			quality(v.Capcode, v.MessageText, v.Heartbeat), quality(v.Capcode, v.VariantText, v.Heartbeat))
	}
	close, err := q.DedupReportCloseMessages(ctx, since)
	if err != nil {
		return nil, fmt.Errorf("close messages: %s", store.SafeError(err))
	}
	for _, c := range close {
		rep.AddClose(c.FirstText, c.SecondText, OtherJob(c.FirstJob, c.SecondJob))
	}
	if err := voteReport(ctx, q, rep, quality, since); err != nil {
		return nil, err
	}
	return rep, nil
}

// voteReport is the voting section (ADR 0050, measuring voting): messages
// by how many clients heard them, and those that three or more heard, in
// one pass in batches.
func voteReport(ctx context.Context, q *db.Queries, rep *neardup.Report, quality func(capcode, text string, heartbeat bool) neardup.Quality, since time.Time) error {
	heard, err := q.DedupReportHeardBy(ctx, since)
	if err != nil {
		return fmt.Errorf("heard by: %s", store.SafeError(err))
	}
	for _, h := range heard {
		rep.AddHeardBy(int(h.Clients), int(h.Messages))
	}
	clients, err := q.DedupReportClientCopies(ctx, since)
	if err != nil {
		return fmt.Errorf("receivers: %s", store.SafeError(err))
	}
	// Receivers are numbered by copies heard, the most first (an ID only
	// breaks a tie, and is never printed).
	slices.SortFunc(clients, func(a, b db.DedupReportClientCopiesRow) int {
		return cmp.Or(cmp.Compare(b.Copies, a.Copies), cmp.Compare(a.ClientID, b.ClientID))
	})
	index := map[string]int{}
	for i, c := range clients {
		index[c.ClientID] = i
		rep.AddReceiver(int(c.Copies))
	}

	afterSent, afterID := since, int64(0)
	for {
		rows, err := q.DedupReportCopies(ctx, db.DedupReportCopiesParams{Since: since, AfterSent: afterSent, AfterID: afterID, Batch: reportBatch})
		if err != nil {
			return fmt.Errorf("copies: %s", store.SafeError(err))
		}
		if len(rows) == 0 {
			return nil
		}
		// Rows come by message, then client: one copy per client (its first).
		for i := 0; i < len(rows); {
			m := rows[i]
			var copies []string
			var receivers []int
			seen := map[string]bool{}
			for ; i < len(rows) && rows[i].ID == m.ID; i++ {
				r := rows[i]
				if seen[r.ClientID] {
					continue
				}
				seen[r.ClientID] = true
				text := m.MessageText
				if r.CopyText != nil {
					text = *r.CopyText
				}
				x, ok := index[r.ClientID]
				if !ok {
					x = -1
				}
				copies = append(copies, text)
				receivers = append(receivers, x)
			}
			rep.AddCopies(m.MessageText, copies, receivers, func(text string) neardup.Quality {
				return quality(m.Capcode, text, m.Heartbeat)
			})
			afterSent, afterID = m.SentAt, m.ID
		}
	}
}
