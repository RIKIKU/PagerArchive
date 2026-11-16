package accounts

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// R43e's caps, by the clock: per address (3 an hour, 5 a day) and per sender
// (20 an hour, 50 a day); an email that wasn't sent isn't counted.
func TestR43eInvitationEmailLimits(t *testing.T) {
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	s := &Service{Now: func() time.Time { return now }}
	refused := func(t *testing.T, err error, want string) {
		t.Helper()
		var e *Error
		if !errors.As(err, &e) || e.Status != http.StatusTooManyRequests || e.Code != "too_many_emails" || e.RetryAfter <= 0 || !strings.Contains(e.Msg, want) {
			t.Fatalf("want a 429 saying %q, got %#v", want, err)
		}
	}

	// One address: 3 in an hour, then told to wait until the first is an hour old.
	for i := range 3 {
		if _, err := s.limitEmail(1, "g@example.com", "g@example.com"); err != nil {
			t.Fatalf("email %d: %v", i+1, err)
		}
		now = now.Add(10 * time.Minute)
	}
	_, err := s.limitEmail(2, "g@example.com", "g@example.com")
	refused(t, err, "g@example.com has been sent 3 invitation emails in the last hour")
	refused(t, err, "Try again in 30 minutes.")
	var e *Error
	errors.As(err, &e)
	if e.RetryAfter != 30*time.Minute {
		t.Errorf("RetryAfter %s", e.RetryAfter)
	}
	// Another sender's limit isn't touched by a refusal.
	now = now.Add(30 * time.Minute)
	if _, err := s.limitEmail(2, "g@example.com", "g@example.com"); err != nil {
		t.Fatalf("after the wait: %v", err)
	}
	now = now.Add(time.Hour)
	if _, err := s.limitEmail(2, "g@example.com", "g@example.com"); err != nil {
		t.Fatalf("fifth: %v", err)
	}
	now = now.Add(2 * time.Hour)
	_, err = s.limitEmail(3, "g@example.com", "g@example.com")
	refused(t, err, "5 invitation emails in the last day")

	// A release takes the email back: it wasn't sent.
	release, err := s.limitEmail(4, "h@example.com", "h@example.com")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		release()
		if release, err = s.limitEmail(4, "h@example.com", "h@example.com"); err != nil {
			t.Fatalf("after releases: %v", err)
		}
	}

	// One sender: 20 an hour to different addresses, admins included.
	s = &Service{Now: func() time.Time { return now }}
	for i := range 20 {
		if _, err := s.limitEmail(9, string(rune('a'+i))+"@example.com", "x"); err != nil {
			t.Fatalf("sender's email %d: %v", i+1, err)
		}
	}
	_, err = s.limitEmail(9, "z@example.com", "z@example.com")
	refused(t, err, "You've sent 20 invitation emails in the last hour")
	if _, err := s.limitEmail(10, "z@example.com", "z@example.com"); err != nil {
		t.Errorf("another sender: %v", err)
	}
	// 50 a day: 20 more an hour later, then 10, then refused.
	for h, n := range []int{20, 10} {
		now = now.Add(time.Hour)
		for i := range n {
			if _, err := s.limitEmail(9, string(rune('a'+i))+string(rune('0'+h))+"@example.com", "x"); err != nil {
				t.Fatalf("hour %d email %d: %v", h+2, i+1, err)
			}
		}
	}
	now = now.Add(time.Hour)
	_, err = s.limitEmail(9, "y@example.com", "y@example.com")
	refused(t, err, "You've sent 50 invitation emails in the last day")
	refused(t, err, "Try again in 21 hours.")

	// Nothing older than a day is kept.
	now = now.Add(25 * time.Hour)
	s.limitEmail(9, "y@example.com", "y@example.com")
	if len(s.emails.sent) != 2 {
		t.Errorf("kept %d keys", len(s.emails.sent))
	}
}

func TestWaitText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		time.Second: "a minute", time.Minute: "a minute", 61 * time.Second: "2 minutes",
		119 * time.Minute: "119 minutes", 21 * time.Hour: "21 hours", 20*time.Hour + time.Minute: "21 hours",
	} {
		if got := waitText(d); got != want {
			t.Errorf("%s: %q, want %q", d, got, want)
		}
	}
}

// INVITATION_SENDER_LIMITS replaces the sender's two caps; the address's stay.
func TestR43eSenderLimitsSetting(t *testing.T) {
	lims, err := ParseSenderLimits(" 2, 4 ")
	if err != nil || len(lims) != 2 || lims[0] != (Limit{2, time.Hour}) || lims[1] != (Limit{4, 24 * time.Hour}) {
		t.Fatalf("ParseSenderLimits: %v, %v", lims, err)
	}
	for _, bad := range []string{"", "20", "20,", "0,5", "20,50,1", "a,b", "-1,5"} {
		if _, err := ParseSenderLimits(bad); err == nil {
			t.Errorf("ParseSenderLimits(%q) accepted", bad)
		}
	}
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	s := &Service{Now: func() time.Time { return now }, SenderLimits: lims}
	for i := range 2 {
		if _, err := s.limitEmail(1, "a"+string(rune('0'+i))+"@example.com", "x"); err != nil {
			t.Fatalf("email %d: %v", i+1, err)
		}
	}
	if _, err := s.limitEmail(1, "c@example.com", "c@example.com"); err == nil || !strings.Contains(err.Error(), "2 invitation emails") {
		t.Fatalf("third email from the sender: want refused, got %v", err)
	}
	for i := range 3 {
		if _, err := s.limitEmail(int64(10+i), "same@example.com", "same@example.com"); err != nil {
			t.Fatalf("address email %d: %v", i+1, err)
		}
	}
	if _, err := s.limitEmail(20, "same@example.com", "same@example.com"); err == nil {
		t.Fatal("fourth email to one address: want refused")
	}
}
