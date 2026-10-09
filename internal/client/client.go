// Package client reads decoder output, buffers pages in memory and posts
// them to the server (SPEC.md R12–R15). Page text is never written to a file
// or logged; it goes to stdout only when PrintPages is on (R13a).
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/RIKIKU/PagerArchive/internal/decoder"
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
}

// Buffer is a bounded in-memory FIFO. When full it drops the oldest page
// (section 14). It is never written to disk (R15).
type Buffer struct {
	mu      sync.Mutex
	items   []Page
	max     int
	dropped int
	ready   chan struct{}
}

// NewBuffer returns a buffer holding at most max pages.
func NewBuffer(max int) *Buffer {
	return &Buffer{max: max, ready: make(chan struct{}, 1)}
}

// Push adds a page, dropping the oldest if the buffer is full.
func (b *Buffer) Push(p Page) (dropped bool) {
	b.mu.Lock()
	if len(b.items) >= b.max {
		b.items = b.items[1:]
		b.dropped++
		dropped = true
	}
	b.items = append(b.items, p)
	b.mu.Unlock()
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
}

// Run reads lines until r ends. Lines that aren't pages are dropped silently.
func (rd *Reader) Run(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<10)
	for sc.Scan() {
		now := rd.Now().UTC() // receive time: as soon as the line arrives
		rec, ok := rd.Adapter.Decode(sc.Text())
		if !ok {
			continue
		}
		if rd.PrintPages {
			fmt.Fprintf(rd.Stdout, "%s %s %s\n", now.Format(time.RFC3339), rec.Capcode, rec.Text)
		}
		if rd.Buffer.Push(Page{
			ClientID:   rd.ClientID,
			Capcode:    rec.Capcode,
			Text:       rec.Text,
			ReceivedAt: now,
			Function:   rec.Function,
			Baud:       rec.Baud,
		}) {
			rd.Log.Warn("buffer full: dropped the oldest page")
		}
	}
	return sc.Err()
}

// Sender posts buffered pages in order, retrying with backoff.
type Sender struct {
	URL        string // server base URL
	Key        string
	HTTP       *http.Client
	Buffer     *Buffer
	Log        *slog.Logger
	MinBackoff time.Duration
	MaxBackoff time.Duration
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
		retry, err := s.post(ctx, p)
		switch {
		case err == nil:
			s.Buffer.Pop(p)
			backoff = s.MinBackoff
			continue
		case !retry:
			// The server will never accept it; don't block the queue.
			s.Log.Error("page rejected by server, dropped", "error", err.Error())
			s.Buffer.Pop(p)
			continue
		}
		s.Log.Warn("server unreachable, will retry", "error", err.Error(), "buffered", s.Buffer.Len(), "retry_in", backoff.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, s.MaxBackoff)
	}
}

// post sends one page. retry says whether a failure is worth retrying.
// Errors never contain page text.
func (s *Sender) post(ctx context.Context, p Page) (retry bool, err error) {
	body, err := json.Marshal(p)
	if err != nil {
		return false, fmt.Errorf("encode page")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.URL, "/")+"/api/v1/pages", bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("build request: invalid server URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.Key)
	resp, err := s.HTTP.Do(req)
	if err != nil {
		// A *url.Error names the URL and cause, never the request body.
		return true, fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode == http.StatusRequestTimeout, resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode >= 500, resp.StatusCode == http.StatusUnauthorized:
		// 401: a key being rotated shouldn't lose pages; keep retrying.
		return true, fmt.Errorf("server returned %d", resp.StatusCode)
	default:
		return false, fmt.Errorf("server returned %d", resp.StatusCode)
	}
}
