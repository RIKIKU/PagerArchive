package integrations

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// AttemptTimeout bounds one delivery attempt (ADR 0006 §3).
const AttemptTimeout = 10 * time.Second

type outcome int

const (
	delivered outcome = iota
	retryable         // not sent, or the endpoint said it failed: try again
	refused           // the endpoint refused it, and will again
	noAnswer          // sent, but no answer: never retried (owner, Q2)
)

// result is one attempt's result. status and err are built from fixed
// parts only, never from a URL, an answer's body or page text (P1).
type result struct {
	outcome    outcome
	status     string // e.g. "HTTP 204 No Content", "MQTT acknowledged"
	err        string
	retryAfter time.Duration
	body       []byte // the answer's first 1 KB, kept for Send test only
}

// send makes one attempt to deliver p.
func (s *Service) send(ctx context.Context, l loaded, sec Secrets, p page, keepBody bool) result {
	if l.kind == MQTT {
		body, err := payload(l, p)
		if err != nil {
			return result{outcome: refused, err: "couldn't build the payload"}
		}
		return s.publish(ctx, l, sec, l.config.Topic, body, false)
	}
	req, err := s.request(ctx, l, sec, p)
	if err != nil {
		return result{outcome: refused, err: safeText(err)}
	}
	req.Header.Set("User-Agent", "PagerArchive/"+s.version())
	return s.do(req, keepBody)
}

func (s *Service) version() string {
	if s.Version == "" {
		return "dev"
	}
	return s.Version
}

func safeText(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Msg
	}
	return "couldn't build the request"
}

func (s *Service) httpClient() *http.Client {
	s.clientOnce.Do(func() {
		s.client = &http.Client{
			Transport: &http.Transport{
				Proxy:                 nil, // environment proxies would get around the address check
				DialContext:           s.Guard.DialContext,
				ForceAttemptHTTP2:     true,
				TLSHandshakeTimeout:   AttemptTimeout,
				ResponseHeaderTimeout: AttemptTimeout,
				IdleConnTimeout:       30 * time.Second,
				MaxIdleConnsPerHost:   2,
			},
			// Redirects aren't followed: a 3xx counts as a failure (ADR 0006 §5).
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	})
	return s.client
}

// do sends an HTTP request and classifies the answer.
func (s *Service) do(req *http.Request, keepBody bool) result {
	wrote := false
	trace := &httptrace.ClientTrace{WroteRequest: func(i httptrace.WroteRequestInfo) { wrote = i.Err == nil }}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	resp, err := s.httpClient().Do(req)
	if err != nil {
		if wrote {
			return result{outcome: noAnswer, err: fmt.Sprintf("no answer within %d s", int(AttemptTimeout.Seconds()))}
		}
		return result{outcome: retryable, err: netError(err)}
	}
	defer resp.Body.Close()
	r := result{status: fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))}
	var body []byte
	if keepBody || resp.StatusCode == http.StatusTooManyRequests {
		body, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	}
	if keepBody {
		r.body = body[:min(len(body), 1<<10)]
	}
	switch c := resp.StatusCode; {
	case c >= 200 && c < 300:
		r.outcome = delivered
	case c == http.StatusRequestTimeout, c == http.StatusTooEarly, c == http.StatusTooManyRequests, c >= 500 && c != http.StatusNotImplemented:
		r.outcome, r.err = retryable, r.status
		r.retryAfter = retryAfter(resp.Header.Get("Retry-After"), body)
	default:
		r.outcome, r.err = refused, r.status
	}
	return r
}

// retryAfter reads a Retry-After header, or the retry_after field Discord
// and Telegram put in a 429's body.
func retryAfter(header string, body []byte) time.Duration {
	if n, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil && n > 0 {
		return time.Duration(n * float64(time.Second))
	}
	if t, err := http.ParseTime(header); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	var b struct {
		RetryAfter float64 `json:"retry_after"` // Discord
		Parameters struct {
			RetryAfter float64 `json:"retry_after"` // Telegram
		} `json:"parameters"`
	}
	if json.Unmarshal(body, &b) == nil {
		if b.RetryAfter > 0 {
			return time.Duration(b.RetryAfter * float64(time.Second))
		}
		if b.Parameters.RetryAfter > 0 {
			return time.Duration(b.Parameters.RetryAfter * float64(time.Second))
		}
	}
	return 0
}

// netError describes a connection failure without Go's own message, which
// quotes the URL, and a URL can carry credentials (ADR 0006 §9).
func netError(err error) string {
	var addr *AddressError
	var dns *net.DNSError
	var unknownCA x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var verify *tls.CertificateVerificationError
	var header tls.RecordHeaderError
	var timeout interface{ Timeout() bool }
	switch {
	case errors.As(err, &addr):
		return "address not allowed: " + addr.Error()
	case errors.Is(err, ErrLookup), errors.As(err, &dns):
		return "DNS lookup failed"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection reset"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return "network unreachable"
	case errors.As(err, &unknownCA), errors.As(err, &invalid), errors.As(err, &hostname), errors.As(err, &verify):
		return "TLS: certificate not trusted"
	case errors.As(err, &header):
		return "TLS: the server didn't answer with TLS"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &timeout) && timeout.Timeout():
		return fmt.Sprintf("couldn't connect within %d s", int(AttemptTimeout.Seconds()))
	case strings.Contains(fmt.Sprintf("%T", errors.Unwrap(err)), "tls"):
		return "TLS handshake failed"
	}
	return "connection failed"
}

// bodyText is an answer's body for the owner's Send test, as valid UTF-8.
func bodyText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}
