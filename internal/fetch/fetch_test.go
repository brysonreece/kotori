package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"10":                            10 * time.Second,
		" 120 ":                         2 * time.Minute,
		"Sat, 03 Oct 2026 12:00:30 GMT": 30 * time.Second,
		// Missing, unreadable or already past all mean "no advice given".
		"":                              0,
		"soon":                          0,
		"-5":                            0,
		"0":                             0,
		"Sat, 03 Oct 2026 11:59:00 GMT": 0,
	}
	for header, want := range cases {
		if got := retryAfter(header, now); got != want {
			t.Errorf("retryAfter(%q) = %s, want %s", header, got, want)
		}
	}
}

func TestStatusErrorCarriesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/limited" {
			w.Header().Set("Retry-After", "7")
		}
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	defer server.Close()

	for path, want := range map[string]time.Duration{"/limited": 7 * time.Second, "/bare": 0} {
		_, err := New().Get(context.Background(), server.URL+path, nil)
		var status *StatusError
		if !errors.As(err, &status) || status.Code != http.StatusTooManyRequests || status.RetryAfter != want {
			t.Errorf("%s: got %v, want a 429 with Retry-After %s", path, err, want)
		}
	}
}
