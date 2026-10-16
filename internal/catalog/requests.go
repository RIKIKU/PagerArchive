package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
	"github.com/RIKIKU/PagerArchive/internal/store/db"
)

// Request kinds and statuses (R54-R56).
const (
	KindAdd    = "add"
	KindChange = "change"
	KindDelete = "delete"

	StatusWaiting      = "waiting"
	StatusApproved     = "approved"
	StatusRejected     = "rejected"
	StatusWithdrawn    = "withdrawn"
	StatusAliasDeleted = "alias_deleted"
)

// MaxWaitingPerUser caps one user's waiting requests (ADR 0007 §2).
const MaxWaitingPerUser = 20

// RequestInput is a request as a user submits or edits it (R54). A change
// sets only what it changes: Name nil, SetTemplate false or Ignored nil
// leave that field as it is. A new alias gives all three (Ignored defaults
// to false; SetTemplate is implied, and TemplateID null means none).
type RequestInput struct {
	Kind        string  `json:"kind"`
	AliasID     *int64  `json:"alias_id"`
	Pattern     string  `json:"pattern"`
	Name        *string `json:"name"`
	SetTemplate bool    `json:"set_template"`
	TemplateID  *int64  `json:"template_id"`
	Ignored     *bool   `json:"ignored"`
	Note        string  `json:"note"`
}

// prepared is a checked request, ready to store.
type prepared struct {
	kind, pattern string
	aliasID       *int64
	aliasName     *string
	name          *string
	setTemplate   bool
	templateID    *int64
	templateName  *string
	ignored       *bool
	note          *string
	baseChange    int64
}

func checkNote(s string) (*string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return nil, nil
	case utf8.RuneCountInString(s) > 500:
		return nil, refuse(http.StatusUnprocessableEntity, "bad_note", "A note is at most 500 characters.")
	}
	return &s, nil
}

// prepare checks a request when it's submitted or edited (R54, R27a).
func prepare(ctx context.Context, q *db.Queries, in RequestInput) (prepared, error) {
	p := prepared{kind: in.Kind}
	var err error
	if p.note, err = checkNote(in.Note); err != nil {
		return p, err
	}
	switch in.Kind {
	case KindAdd:
		if in.AliasID != nil {
			return p, refuse(http.StatusUnprocessableEntity, "bad_request", "A request for a new alias names a capcode or pattern, not an alias.")
		}
		if p.pattern, err = checkPattern(in.Pattern); err != nil {
			return p, err
		}
		if in.Name == nil {
			return p, refuse(http.StatusUnprocessableEntity, "bad_name", "Give the alias a name.")
		}
		name, err := CheckName(*in.Name)
		if err != nil {
			return p, err
		}
		p.name, p.setTemplate, p.templateID = &name, true, in.TemplateID
		if p.templateName, err = templateName(ctx, q, in.TemplateID); err != nil {
			return p, err
		}
		ignored := in.Ignored != nil && *in.Ignored
		p.ignored = &ignored
		if cur, err := q.AliasByKey(ctx, p.pattern); err == nil {
			return p, refuse(http.StatusConflict, "duplicate_alias",
				"%s already has the alias %q. Each capcode or pattern has one alias: request a change to %q instead.", p.pattern, cur.Name, cur.Name)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return p, dbErr("alias", err)
		}
		return p, nil
	case KindChange, KindDelete:
	default:
		return p, refuse(http.StatusUnprocessableEntity, "bad_kind", `A request is for a new alias ("add"), a change ("change") or a deletion ("delete").`)
	}
	if in.AliasID == nil {
		return p, refuse(http.StatusUnprocessableEntity, "bad_request", "Say which alias the request is for.")
	}
	cur, err := q.AliasView(ctx, *in.AliasID)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, refuse(http.StatusNotFound, "no_alias", "That alias doesn't exist any more.")
	}
	if err != nil {
		return p, dbErr("alias", err)
	}
	p.aliasID, p.pattern, p.aliasName = &cur.ID, cur.Pattern, &cur.Name
	if in.Pattern != "" {
		if want, err := NormalisePattern(strings.TrimSpace(in.Pattern)); err != nil || want != cur.Pattern {
			return p, refuse(http.StatusUnprocessableEntity, "pattern_fixed",
				"A request can't change an alias's capcode or pattern. Request its deletion, and a new alias for the new one.")
		}
	}
	if p.baseChange, err = q.LatestAliasChange(ctx, cur.ID); err != nil {
		return p, dbErr("history", err)
	}
	if in.Kind == KindDelete {
		if in.Name != nil || in.SetTemplate || in.Ignored != nil {
			return p, refuse(http.StatusUnprocessableEntity, "bad_request", "A request to delete an alias changes nothing else.")
		}
		return p, nil
	}
	// A change: keep only what differs from the alias now.
	if in.Name != nil {
		name, err := CheckName(*in.Name)
		if err != nil {
			return p, err
		}
		if name != cur.Name {
			p.name = &name
		}
	}
	if in.SetTemplate && !sameID(in.TemplateID, cur.TemplateID) {
		if p.templateName, err = templateName(ctx, q, in.TemplateID); err != nil {
			return p, err
		}
		p.setTemplate, p.templateID = true, in.TemplateID
	}
	if in.Ignored != nil && *in.Ignored != cur.Ignored {
		p.ignored = in.Ignored
	}
	if p.name == nil && !p.setTemplate && p.ignored == nil {
		return p, refuse(http.StatusUnprocessableEntity, "nothing_to_change", "That's how the alias is already. Change at least one thing.")
	}
	return p, nil
}

// SubmitRequest stores a user's request (R54).
func (c *Catalog) SubmitRequest(ctx context.Context, userID int64, in RequestInput) (int64, error) {
	var id int64
	err := c.Store.InTx(ctx, func(q *db.Queries) error {
		n, err := q.CountWaitingRequestsOf(ctx, userID)
		if err != nil {
			return dbErr("count requests", err)
		}
		if n >= MaxWaitingPerUser {
			return refuse(http.StatusConflict, "too_many", "You have %d requests waiting, the most there can be. Wait for them to be reviewed, or withdraw some.", n)
		}
		p, err := prepare(ctx, q, in)
		if err != nil {
			return err
		}
		if err := oneWaiting(ctx, q, userID, 0, p); err != nil {
			return err
		}
		id, err = q.CreateAliasRequest(ctx, db.CreateAliasRequestParams{Kind: p.kind, RequestedBy: userID, AliasID: p.aliasID, Pattern: p.pattern,
			AliasName: p.aliasName, Name: p.name, SetTemplate: p.setTemplate, TemplateID: p.templateID, TemplateName: p.templateName,
			Ignored: p.ignored, Note: p.note, BaseChange: p.baseChange})
		if err != nil {
			return dbErr("store request", err)
		}
		return nil
	})
	return id, err
}

// oneWaiting refuses a second waiting request from one user for the same
// alias, or the same capcode or pattern (ADR 0007 §2).
func oneWaiting(ctx context.Context, q *db.Queries, userID, notID int64, p prepared) error {
	_, err := q.WaitingRequestOf(ctx, db.WaitingRequestOfParams{RequestedBy: userID, NotID: notID, AliasID: p.aliasID, Pattern: p.pattern})
	if err == nil {
		return refuse(http.StatusConflict, "already_requested", "You already have a waiting request for this. Edit that one instead (My requests).")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return dbErr("requests", err)
	}
	return nil
}

// lockRequest reads a request for a change, locking it. Users may only
// touch their own: anyone else's is "not found".
func lockRequest(ctx context.Context, q *db.Queries, id int64, owner *int64) (db.AliasRequest, error) {
	r, err := q.AliasRequestForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && owner != nil && r.RequestedBy != *owner) {
		return r, refuse(http.StatusNotFound, "no_request", "No such request.")
	}
	if err != nil {
		return r, dbErr("request", err)
	}
	if r.Status != StatusWaiting {
		return r, refuse(http.StatusConflict, "not_waiting", "That request isn't waiting any more: it was %s.", StatusText(r.Status))
	}
	return r, nil
}

// StatusText is how a status reads to people.
func StatusText(status string) string {
	if status == StatusAliasDeleted {
		return "closed because the alias was deleted"
	}
	return status
}

// EditRequest changes a user's own waiting request (R56). It is checked
// again, and counts as seen against the alias as it is now (ADR 0007 §3).
func (c *Catalog) EditRequest(ctx context.Context, userID, id int64, in RequestInput) error {
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		r, err := lockRequest(ctx, q, id, &userID)
		if err != nil {
			return err
		}
		// What the request is about stays: its kind and its alias, or its
		// capcode or pattern.
		in.Kind, in.AliasID = r.Kind, r.AliasID
		if r.Kind == KindAdd && strings.TrimSpace(in.Pattern) == "" {
			in.Pattern = r.Pattern
		}
		if r.Kind != KindAdd {
			in.Pattern = ""
		}
		p, err := prepare(ctx, q, in)
		if err != nil {
			return err
		}
		if r.Kind == KindAdd && p.pattern != r.Pattern {
			return refuse(http.StatusUnprocessableEntity, "pattern_fixed", "A request's capcode or pattern can't be changed. Withdraw it and make a new one.")
		}
		if err := oneWaiting(ctx, q, userID, id, p); err != nil {
			return err
		}
		if _, err := q.EditAliasRequest(ctx, db.EditAliasRequestParams{ID: id, Name: p.name, SetTemplate: p.setTemplate, TemplateID: p.templateID,
			TemplateName: p.templateName, Ignored: p.ignored, Note: p.note, BaseChange: p.baseChange}); err != nil {
			return dbErr("edit request", err)
		}
		return nil
	})
}

// WithdrawRequest withdraws a user's own waiting request (R56).
func (c *Catalog) WithdrawRequest(ctx context.Context, userID, id int64) error {
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		if _, err := lockRequest(ctx, q, id, &userID); err != nil {
			return err
		}
		return closeRequest(ctx, q, id, StatusWithdrawn, nil, nil, nil)
	})
}

func closeRequest(ctx context.Context, q *db.Queries, id int64, status string, reviewer *int64, feedback *string, aliasID *int64) error {
	if err := q.CloseAliasRequest(ctx, db.CloseAliasRequestParams{ID: id, Status: status, ReviewedBy: reviewer, Feedback: feedback, AliasID: aliasID}); err != nil {
		return dbErr("close request", err)
	}
	return nil
}

// ApproveRequest applies a waiting request exactly as submitted (R55):
// a change sets only the fields it asks for. A new alias is checked
// against R27a again; if the capcode or pattern has an alias now, approval
// is refused with the reason and the request stays waiting. The change and
// its history entry (naming the requester and the approver, R57) are made
// in the same transaction as the approval.
func (c *Catalog) ApproveRequest(ctx context.Context, adminID, id int64) error {
	err := c.inTx(ctx, func(q *db.Queries, rp reparses) error {
		r, err := lockRequest(ctx, q, id, nil)
		if err != nil {
			return err
		}
		by := By{Source: SourceRequest, UserID: &adminID, RequestedBy: &r.RequestedBy, RequestID: &r.ID}
		switch r.Kind {
		case KindAdd:
			if r.TemplateID == nil && r.TemplateName != nil {
				return refuse(http.StatusConflict, "no_template", "The template this request names was deleted. Reject it with feedback instead.")
			}
			ignored := r.Ignored != nil && *r.Ignored
			aliasID, err := addAlias(ctx, q, settings{pattern: r.Pattern, name: *r.Name, tplID: r.TemplateID, tplName: r.TemplateName, ignored: ignored}, by, rp)
			var dup *DuplicateAliasError
			if errors.As(err, &dup) {
				return refuse(http.StatusConflict, "duplicate_alias",
					"Can't approve: %s now has the alias %q, and each capcode or pattern has one alias (R27a). Reject this request with feedback instead.", dup.Pattern, dup.Existing)
			}
			if err != nil {
				return err
			}
			return closeRequest(ctx, q, r.ID, StatusApproved, &adminID, nil, &aliasID)
		case KindChange, KindDelete:
			if r.AliasID == nil {
				return refuse(http.StatusConflict, "no_alias", "That alias doesn't exist any more.")
			}
			// Closed first: deleting an alias closes its other waiting
			// requests as "alias deleted" (R55).
			if err := closeRequest(ctx, q, r.ID, StatusApproved, &adminID, nil, nil); err != nil {
				return err
			}
			if r.Kind == KindDelete {
				_, err := deleteAlias(ctx, q, *r.AliasID, by, rp)
				return err
			}
			if r.SetTemplate && r.TemplateID == nil && r.TemplateName != nil {
				return refuse(http.StatusConflict, "no_template", "The template this request names was deleted. Reject it with feedback instead.")
			}
			return changeAlias(ctx, q, *r.AliasID, change{name: r.Name, setTemplate: r.SetTemplate, tplID: r.TemplateID, tplName: r.TemplateName, ignored: r.Ignored}, by, rp)
		}
		return errors.New("unknown request kind")
	})
	if errors.Is(err, errRaced) {
		return refuse(http.StatusConflict, "duplicate_alias", "Can't approve: that capcode or pattern got an alias at the same moment (R27a). Reload to see it.")
	}
	return err
}

// RejectRequest rejects a waiting request with the admin's feedback, which
// is required (R55). Nothing else changes.
func (c *Catalog) RejectRequest(ctx context.Context, adminID, id int64, feedback string) error {
	feedback = strings.TrimSpace(feedback)
	switch {
	case feedback == "":
		return refuse(http.StatusUnprocessableEntity, "feedback_required", "Say why it's rejected: rejecting needs feedback for the person who asked.")
	case utf8.RuneCountInString(feedback) > 500:
		return refuse(http.StatusUnprocessableEntity, "bad_feedback", "Feedback is at most 500 characters.")
	}
	return c.Store.InTx(ctx, func(q *db.Queries) error {
		if _, err := lockRequest(ctx, q, id, nil); err != nil {
			return err
		}
		return closeRequest(ctx, q, id, StatusRejected, &adminID, &feedback, nil)
	})
}

// Settings are an alias's settings as a request shows them.
type Settings struct {
	Pattern    string  `json:"pattern"`
	Name       string  `json:"name"`
	TemplateID *int64  `json:"template_id"`
	Template   *string `json:"template"`
	Ignored    bool    `json:"ignored"`
}

// Proposal is what a request asks for. For a change, a null field (or
// set_template false) is left as it is.
type Proposal struct {
	Name        *string `json:"name"`
	SetTemplate bool    `json:"set_template"`
	TemplateID  *int64  `json:"template_id"`
	Template    *string `json:"template"`
	Ignored     *bool   `json:"ignored"`
}

// RequestView is a request as its owner or an admin sees it.
type RequestView struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	AliasID   *int64    `json:"alias_id"` // null for a new alias not yet approved, or once the alias is deleted
	Pattern   string    `json:"pattern"`
	AliasName *string   `json:"alias_name"` // the alias's name when the request was made
	Current   *Settings `json:"current"`    // the alias now, if it exists
	Proposed  Proposal  `json:"proposed"`
	Note      *string   `json:"note"`
	Feedback  *string   `json:"feedback"`
	// ChangedSince: the alias changed after the request was made (R55).
	ChangedSince bool `json:"changed_since_requested"`
	// NowAliasedAs: for a new alias, the alias its capcode or pattern has
	// gained meanwhile; approving is then refused (A45).
	NowAliasedAs *string `json:"now_aliased_as"`
	// Admins only: who asked and who reviewed (owner, 2026-10-16).
	RequestedBy string     `json:"requested_by,omitempty"`
	ReviewedBy  string     `json:"reviewed_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at"`
}

// RequestFilter selects requests to list.
type RequestFilter struct {
	RequestedBy *int64 // nil: everyone's (admins)
	AliasID     *int64
	ID          *int64
	Which       string // "waiting", "closed" or "all"
	Max         int32
	Admin       bool // name the people involved
}

// Requests lists requests, newest first, with the marks of ADR 0007 §3.
func (c *Catalog) Requests(ctx context.Context, f RequestFilter) ([]RequestView, error) {
	if f.Which == "" {
		f.Which = "all"
	}
	if f.Max == 0 {
		f.Max = 200
	}
	rows, err := c.Store.ListAliasRequests(ctx, db.ListAliasRequestsParams{RequestedBy: f.RequestedBy, ID: f.ID, AliasID: f.AliasID, Which: f.Which, MaxRows: f.Max})
	if err != nil {
		return nil, dbErr("requests", err)
	}
	out := make([]RequestView, 0, len(rows))
	for _, r := range rows {
		v := RequestView{ID: r.ID, Kind: r.Kind, Status: r.Status, AliasID: r.AliasID, Pattern: r.Pattern, AliasName: r.AliasName,
			Proposed: Proposal{Name: r.Name, SetTemplate: r.SetTemplate, TemplateID: r.TemplateID, Template: r.TemplateName, Ignored: r.Ignored},
			Note:     r.Note, Feedback: r.Feedback, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ClosedAt: r.ClosedAt}
		if r.CurName != nil {
			v.Current = &Settings{Pattern: *r.CurPattern, Name: *r.CurName, TemplateID: r.CurTemplateID, Template: r.CurTemplateName, Ignored: *r.CurIgnored}
		}
		if r.Status == StatusWaiting {
			v.ChangedSince = r.Kind != KindAdd && r.LatestChange > r.BaseChange
			v.NowAliasedAs = r.OccupantName
		}
		if f.Admin {
			v.RequestedBy = accounts.Display(r.RequesterName, r.RequesterEmail, r.RequesterStatus)
			if r.ReviewerStatus != nil {
				v.ReviewedBy = accounts.Display(r.ReviewerName, r.ReviewerEmail, *r.ReviewerStatus)
			}
		}
		out = append(out, v)
	}
	if f.Which == "waiting" && f.RequestedBy == nil {
		slices.Reverse(out) // oldest first, for review
	}
	return out, nil
}

// HistoryEntry is one change to an alias (R57), for admins.
type HistoryEntry struct {
	ID          int64             `json:"id"`
	AliasID     int64             `json:"alias_id"`
	AliasName   string            `json:"alias_name"` // its name at the time
	Pattern     string            `json:"pattern"`    // its capcode or pattern at the time
	Action      string            `json:"action"`     // added, changed or deleted
	Changes     map[string][2]any `json:"changes"`    // field -> [before, after]
	Source      string            `json:"source"`     // admin, request, cli or import
	MadeBy      string            `json:"made_by"`    // made or approved it
	RequestedBy *string           `json:"requested_by"`
	RequestID   *int64            `json:"request_id"`
	At          time.Time         `json:"at"`
	// Earlier: an entry of a deleted alias that had this capcode or
	// pattern (owner, 2026-10-16).
	Earlier bool `json:"earlier"`
}

// History lists an alias's changes, and those of deleted aliases that had
// its capcode or pattern; aliasID nil gives only the latter. Admins only
// (owner, 2026-10-16): it names people.
func (c *Catalog) History(ctx context.Context, aliasID *int64, patternKey string) ([]HistoryEntry, error) {
	rows, err := c.Store.AliasHistory(ctx, db.AliasHistoryParams{AliasID: aliasID, PatternKey: patternKey})
	if err != nil {
		return nil, dbErr("history", err)
	}
	out := make([]HistoryEntry, 0, len(rows))
	for _, r := range rows {
		e := HistoryEntry{ID: r.ID, AliasID: r.AliasID, AliasName: r.AliasName, Pattern: r.PatternKey, Action: r.Action, Source: r.Source,
			RequestID: r.RequestID, At: r.At, Earlier: aliasID == nil || r.AliasID != *aliasID}
		if err := json.Unmarshal(r.Changes, &e.Changes); err != nil {
			return nil, err
		}
		switch {
		case r.MadeByStatus != nil:
			e.MadeBy = accounts.Display(r.MadeByName, r.MadeByEmail, *r.MadeByStatus)
		case r.Source == SourceImport:
			e.MadeBy = "import"
		default:
			e.MadeBy = "command line"
		}
		if r.RequestedByStatus != nil {
			d := accounts.Display(r.RequestedByName, r.RequestedByEmail, *r.RequestedByStatus)
			e.RequestedBy = &d
		}
		out = append(out, e)
	}
	return out, nil
}
