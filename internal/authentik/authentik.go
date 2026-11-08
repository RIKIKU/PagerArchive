// Package authentik talks to Authentik's API for the app's account
// management (ADR 0004): invitations, and enabling, disabling and deleting
// users. It never logs email addresses or response bodies.
package authentik

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/accounts"
)

// EnrolmentFlow is the enrolment flow's slug in deploy/authentik/blueprints/pagerarchive.yaml.
const EnrolmentFlow = "pagerarchive-enrolment"

// InvitationTemplate is the invitation email, in deploy/authentik/templates.
const InvitationTemplate = "pagerarchive/invitation.html"

// Client is Authentik's API, as the app's service account.
type Client struct {
	URL   *url.URL // public URL, as browsers reach it
	Token string
	HTTP  *http.Client // see Transport

	mu     sync.Mutex
	flowPK string
}

var _ accounts.Directory = (*Client)(nil)

// Transport sends requests for the public URL to internal instead, keeping
// the public Host and scheme in the headers, so Authentik builds the same
// URLs (issuer, links) as for a browser. With internal nil it changes
// nothing. The API container uses it to reach Authentik on the compose
// network without going through the reverse proxy.
func Transport(public, internal *url.URL, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if internal == nil {
		return base
	}
	return roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != public.Host {
			return base.RoundTrip(r)
		}
		r = r.Clone(r.Context())
		r.Host = public.Host
		r.URL.Scheme, r.URL.Host = internal.Scheme, internal.Host
		r.Header.Set("X-Forwarded-Proto", public.Scheme)
		r.Header.Set("X-Forwarded-Host", public.Host)
		return base.RoundTrip(r)
	})
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// apiError is a failed call: the operation and status, never the body.
type apiError struct {
	op     string
	status int
}

func (e *apiError) Error() string { return fmt.Sprintf("%s: HTTP %d", e.op, e.status) }

func (c *Client) do(ctx context.Context, op, method, path string, body, out any, okStatus ...int) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	path, query, _ := strings.Cut(path, "?")
	u := c.URL.JoinPath(path)
	u.RawQuery = query
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// An http.Client error quotes the URL, whose query can hold an address.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return 0, fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()
	for _, s := range okStatus {
		if resp.StatusCode == s {
			// Only a success has a body to decode: an accepted 404 means "gone".
			if out != nil && resp.StatusCode != http.StatusNoContent && resp.StatusCode < 300 {
				if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
					return resp.StatusCode, fmt.Errorf("%s: decode: %w", op, err)
				}
			}
			return resp.StatusCode, nil
		}
	}
	return resp.StatusCode, &apiError{op: op, status: resp.StatusCode}
}

func (c *Client) enrolmentFlowPK(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.flowPK != "" {
		return c.flowPK, nil
	}
	var out struct {
		Results []struct {
			PK string `json:"pk"`
		} `json:"results"`
	}
	q := url.Values{"slug": {EnrolmentFlow}}
	if _, err := c.do(ctx, "find enrolment flow", http.MethodGet, "/api/v3/flows/instances/?"+q.Encode(), nil, &out, http.StatusOK); err != nil {
		return "", err
	}
	if len(out.Results) != 1 {
		return "", fmt.Errorf("enrolment flow %q not found: is the release's deploy/authentik/blueprints/pagerarchive.yaml next to compose.yaml?", EnrolmentFlow)
	}
	c.flowPK = out.Results[0].PK
	return c.flowPK, nil
}

// CreateInvitation creates an invitation whose fixed data sets the email and
// username and records the app's invitation ID on the user. It isn't
// single-use: opening the link uses nothing up, and the enrolment flow
// deletes it once the emailed code is verified (R43a, ADR 0021).
func (c *Client) CreateInvitation(ctx context.Context, inv accounts.DirectoryInvitation) (string, error) {
	flow, err := c.enrolmentFlowPK(ctx)
	if err != nil {
		return "", err
	}
	body := map[string]any{
		"name":       fmt.Sprintf("pagerarchive-%d", inv.AppID),
		"expires":    inv.Expires.UTC().Format(time.RFC3339),
		"single_use": false,
		"flow":       flow,
		"fixed_data": map[string]any{
			"email":      inv.Address,
			"username":   inv.Key,
			"attributes": map[string]any{"pagerarchive_invitation": strconv.FormatInt(inv.AppID, 10)},
		},
	}
	var out struct {
		PK string `json:"pk"`
	}
	if _, err := c.do(ctx, "create invitation", http.MethodPost, "/api/v3/stages/invitation/invitations/", body, &out, http.StatusCreated); err != nil {
		return "", err
	}
	return out.PK, nil
}

// SendInvitation has Authentik email the invitation link.
func (c *Client) SendInvitation(ctx context.Context, id, address string) error {
	_, err := c.do(ctx, "send invitation", http.MethodPost, "/api/v3/stages/invitation/invitations/"+url.PathEscape(id)+"/send_email/",
		map[string]any{"email_addresses": []string{address}, "template": InvitationTemplate}, nil, http.StatusNoContent, http.StatusOK)
	return err
}

// DeleteInvitation deletes an invitation, so its link stops working.
func (c *Client) DeleteInvitation(ctx context.Context, id string) error {
	_, err := c.do(ctx, "delete invitation", http.MethodDelete, "/api/v3/stages/invitation/invitations/"+url.PathEscape(id)+"/",
		nil, nil, http.StatusNoContent, http.StatusNotFound)
	return err
}

// RepairInvitation reports whether an invitation still works, and makes one
// created before ADR 0021 (single-use, so opening it used it up) reusable.
// An expired invitation isn't found.
func (c *Client) RepairInvitation(ctx context.Context, id string) (bool, error) {
	path := "/api/v3/stages/invitation/invitations/" + url.PathEscape(id) + "/"
	var out struct {
		SingleUse bool `json:"single_use"`
	}
	status, err := c.do(ctx, "read invitation", http.MethodGet, path, nil, &out, http.StatusOK, http.StatusNotFound)
	if err != nil || status == http.StatusNotFound {
		return false, err
	}
	if out.SingleUse {
		if _, err := c.do(ctx, "make invitation reusable", http.MethodPatch, path, map[string]any{"single_use": false}, nil, http.StatusOK); err != nil {
			return true, err
		}
	}
	return true, nil
}

// EnrolmentURL is the browser link that starts enrolment with an invitation.
func (c *Client) EnrolmentURL(id string) string {
	return c.URL.JoinPath("/if/flow/", EnrolmentFlow, "/").String() + "?itoken=" + url.QueryEscape(id)
}

// SetActive enables or disables a user. Authentik ends a disabled user's
// sessions itself.
func (c *Client) SetActive(ctx context.Context, subject string, active bool) error {
	_, err := c.do(ctx, "set user active", http.MethodPatch, "/api/v3/core/users/"+url.PathEscape(subject)+"/",
		map[string]any{"is_active": active}, nil, http.StatusOK)
	return err
}

// DeleteUser deletes a user.
func (c *Client) DeleteUser(ctx context.Context, subject string) error {
	_, err := c.do(ctx, "delete user", http.MethodDelete, "/api/v3/core/users/"+url.PathEscape(subject)+"/",
		nil, nil, http.StatusNoContent, http.StatusNotFound)
	return err
}

// UserByUsername finds a user by username: their ID (the OIDC subject) and
// whether they're active.
func (c *Client) UserByUsername(ctx context.Context, username string) (accounts.DirectoryUser, bool, error) {
	var out struct {
		Results []struct {
			PK       int64 `json:"pk"`
			IsActive bool  `json:"is_active"`
		} `json:"results"`
	}
	q := url.Values{"username": {username}}
	if _, err := c.do(ctx, "find user", http.MethodGet, "/api/v3/core/users/?"+q.Encode(), nil, &out, http.StatusOK); err != nil {
		return accounts.DirectoryUser{}, false, err
	}
	if len(out.Results) == 0 {
		return accounts.DirectoryUser{}, false, nil
	}
	r := out.Results[0]
	return accounts.DirectoryUser{Subject: strconv.FormatInt(r.PK, 10), Active: r.IsActive}, true, nil
}

// EmailHolders finds who holds an address. The identification stages match
// ignoring case and take the oldest match (R44c), so the search's results are
// compared the same way. Its results also hold partial matches.
func (c *Client) EmailHolders(ctx context.Context, address string) (accounts.EmailHolders, error) {
	var holders accounts.EmailHolders
	for page := 1; page != 0; {
		var out struct {
			Pagination struct {
				Next int `json:"next"`
			} `json:"pagination"`
			Results []struct {
				Email       string `json:"email"`
				IsSuperuser bool   `json:"is_superuser"`
			} `json:"results"`
		}
		q := url.Values{"search": {address}, "include_groups": {"false"}, "page": {strconv.Itoa(page)}}
		if _, err := c.do(ctx, "find address", http.MethodGet, "/api/v3/core/users/?"+q.Encode(), nil, &out, http.StatusOK); err != nil {
			return accounts.EmailHolders{}, err
		}
		for _, u := range out.Results {
			if strings.EqualFold(u.Email, address) {
				holders.Count++
				holders.Administrator = holders.Administrator || u.IsSuperuser
			}
		}
		page = out.Pagination.Next
	}
	return holders, nil
}
