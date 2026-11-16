package accounts

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Limit is at most N emails in each Per.
type Limit struct {
	N   int
	Per time.Duration
}

// The caps on invitation emails (R43e), counted for new invitations and
// resends alike: per sender, admins included, and per address, whoever sends.
var (
	SenderLimits  = []Limit{{20, time.Hour}, {50, 24 * time.Hour}}
	AddressLimits = []Limit{{3, time.Hour}, {5, 24 * time.Hour}}
)

// emailLimiter counts the invitation emails sent, in memory, like Send test's
// limit (R51c): a restart of the API starts the counts again.
type emailLimiter struct {
	mu   sync.Mutex
	sent map[string][]time.Time // by key, oldest first
}

// reserve counts one email to key for each of keys' limits, if none is
// reached, and returns a release that takes it back (the email wasn't sent).
// Otherwise it counts nothing and returns the key whose limit was reached,
// the limit and how long until it allows one more.
func (l *emailLimiter) reserve(now time.Time, keys map[string][]Limit) (release func(), key string, hit Limit, wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sent == nil {
		l.sent = map[string][]time.Time{}
	}
	l.forget(now)
	for k, limits := range keys {
		for _, lim := range limits {
			if w := waitFor(l.sent[k], now, lim); w > wait {
				key, hit, wait = k, lim, w
			}
		}
	}
	if wait > 0 {
		return nil, key, hit, wait
	}
	for k := range keys {
		l.sent[k] = append(l.sent[k], now)
	}
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		for k := range keys {
			ts := l.sent[k]
			for i := len(ts) - 1; i >= 0; i-- {
				if ts[i].Equal(now) {
					l.sent[k] = append(ts[:i:i], ts[i+1:]...)
					break
				}
			}
		}
	}, "", Limit{}, 0
}

// forget drops what no limit counts any more.
func (l *emailLimiter) forget(now time.Time) {
	var longest time.Duration
	for _, lim := range append(append([]Limit{}, SenderLimits...), AddressLimits...) {
		longest = max(longest, lim.Per)
	}
	for k, ts := range l.sent {
		i := 0
		for i < len(ts) && now.Sub(ts[i]) >= longest {
			i++
		}
		if i == len(ts) {
			delete(l.sent, k)
		} else {
			l.sent[k] = ts[i:]
		}
	}
}

// waitFor is how long until ts, oldest first, has fewer than lim.N in the
// last lim.Per: 0 if it has now.
func waitFor(ts []time.Time, now time.Time, lim Limit) time.Duration {
	var in []time.Time
	for _, t := range ts {
		if now.Sub(t) < lim.Per {
			in = append(in, t)
		}
	}
	if len(in) < lim.N {
		return 0
	}
	return in[len(in)-lim.N].Add(lim.Per).Sub(now)
}

// limitEmail counts one invitation email from sender to the address with
// emailKey, or refuses it with 429, saying which limit and how long to wait.
func (s *Service) limitEmail(senderID int64, emailKey, address string) (func(), error) {
	sender, to := "sender:"+strconv.FormatInt(senderID, 10), "address:"+emailKey
	release, key, hit, wait := s.emails.reserve(s.now(), map[string][]Limit{sender: SenderLimits, to: AddressLimits})
	if release != nil {
		return release, nil
	}
	if key == to {
		return nil, &Error{Status: http.StatusTooManyRequests, Code: "too_many_emails", RetryAfter: wait,
			Msg: fmt.Sprintf("%s has been sent %d invitation emails in the last %s, which is the most allowed. Try again in %s.", address, hit.N, span(hit.Per), waitText(wait))}
	}
	return nil, &Error{Status: http.StatusTooManyRequests, Code: "too_many_emails", RetryAfter: wait,
		Msg: fmt.Sprintf("You've sent %d invitation emails in the last %s, which is the most allowed. Try again in %s.", hit.N, span(hit.Per), waitText(wait))}
}

func span(d time.Duration) string {
	switch d {
	case time.Hour:
		return "hour"
	case 24 * time.Hour:
		return "day"
	}
	return d.String()
}

// waitText is a wait as people say it, rounded up to the minute.
func waitText(d time.Duration) string {
	m := int((d + time.Minute - 1) / time.Minute)
	switch {
	case m <= 1:
		return "a minute"
	case m < 120:
		return fmt.Sprintf("%d minutes", m)
	default:
		return fmt.Sprintf("%d hours", (m+59)/60)
	}
}
