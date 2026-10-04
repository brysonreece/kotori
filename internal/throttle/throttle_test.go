package throttle

import (
	"context"
	"math"
	"testing"
	"time"
)

// clock is a fake clock: sleeping advances it instead of taking time.
type clock struct{ t time.Time }

func fake() (*Limiter, *clock) {
	c := &clock{t: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	l := New()
	l.now = func() time.Time { return c.t }
	l.sleep = func(ctx context.Context, d time.Duration) error {
		c.t = c.t.Add(d)
		return ctx.Err()
	}
	return l, c
}

func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// serve sends n requests that are all served, and returns how long they took.
func serve(t *testing.T, l *Limiter, c *clock, host string, n int) time.Duration {
	t.Helper()
	start := c.t
	for i := 0; i < n; i++ {
		if err := l.Wait(context.Background(), host); err != nil {
			t.Fatal(err)
		}
		l.Allowed(host)
	}
	return c.t.Sub(start)
}

func near(got, want float64) bool { return math.Abs(got-want) < 0.01 }

func TestUnknownHostIsNotPaced(t *testing.T) {
	l, c := fake()
	if took := serve(t, l, c, "cdn", 500); took != 0 {
		t.Errorf("500 requests to a host that never refused took %s", took)
	}
	if l.Rate("cdn") != 0 {
		t.Errorf("rate = %v, want unpaced", l.Rate("cdn"))
	}
}

func TestFirstRefusalSetsThePace(t *testing.T) {
	l, c := fake()
	// 100 requests are served within 3 seconds, then the host refuses and
	// asks for 10 seconds: a limit of about 100 per 10 seconds.
	for i := 0; i < 100; i++ {
		l.Wait(context.Background(), "cdn")
		l.Allowed("cdn")
		c.advance(30 * time.Millisecond)
	}
	l.Refused("cdn", 10*time.Second, 10*time.Second)
	if got := l.Rate("cdn"); !near(got, 8.5) {
		t.Fatalf("rate = %.2f/s, want 8.5/s (85%% of 100 per 10s)", got)
	}

	if got := l.Blocked("cdn"); got != 10*time.Second {
		t.Errorf("Blocked = %s, want 10s", got)
	}
	c.advance(4 * time.Second)
	if got := l.Blocked("cdn"); got != 6*time.Second {
		t.Errorf("Blocked = %s after 4s, want 6s", got)
	}
	c.advance(-4 * time.Second)

	// The next request waits out the block, and the ones after are spaced.
	before := c.t
	l.Wait(context.Background(), "cdn")
	if waited := c.t.Sub(before); waited != 10*time.Second {
		t.Errorf("waited %s for the block to end, want 10s", waited)
	}
	l.Allowed("cdn")
	if got := l.Blocked("cdn"); got != 0 {
		t.Errorf("Blocked = %s once the block is over, want 0", got)
	}
	took := serve(t, l, c, "cdn", 85)
	if took < 9900*time.Millisecond || took > 10100*time.Millisecond {
		t.Errorf("85 paced requests took %s, want about 10s", took)
	}
}

func TestOneWaveOfRefusalsCountsOnce(t *testing.T) {
	l, c := fake()
	serve(t, l, c, "cdn", 100)
	// Eight requests were in flight when the limit was hit.
	for i := 0; i < 8; i++ {
		l.Refused("cdn", 10*time.Second, 10*time.Second)
		c.advance(20 * time.Millisecond)
	}
	// A straggler that was served after all changes nothing either.
	l.Allowed("cdn")
	l.Refused("cdn", 10*time.Second, 10*time.Second)
	if got := l.Rate("cdn"); !near(got, 8.5) {
		t.Errorf("rate = %.2f/s after one wave, want 8.5/s", got)
	}
}

func TestBlockThatOutlastsRetryAfter(t *testing.T) {
	l, c := fake()
	serve(t, l, c, "cdn", 100)
	l.Refused("cdn", 10*time.Second, 10*time.Second)
	// After the wait the host still refuses, now without saying for how
	// long. That extends the block; it is not a reason to slow down.
	l.Wait(context.Background(), "cdn")
	l.Refused("cdn", time.Second, 0)
	if got := l.Rate("cdn"); !near(got, 8.5) {
		t.Errorf("rate = %.2f/s, want it unchanged at 8.5/s", got)
	}
	before := c.t
	l.Wait(context.Background(), "cdn")
	if waited := c.t.Sub(before); waited != time.Second {
		t.Errorf("waited %s, want the 1s extension", waited)
	}
}

func TestPacedRefusalSlowsDownThenRecovers(t *testing.T) {
	l, c := fake()
	serve(t, l, c, "cdn", 100)
	l.Refused("cdn", 10*time.Second, 10*time.Second)
	serve(t, l, c, "cdn", 20)

	// Refused again while pacing: the pace was too high.
	l.Refused("cdn", 10*time.Second, 10*time.Second)
	if got := l.Rate("cdn"); !near(got, 6.8) {
		t.Fatalf("rate = %.2f/s after a paced refusal, want 6.8/s", got)
	}

	// With no further refusals the pace climbs back, but stops short of
	// the pace that was refused.
	for i := 0; i < 10; i++ {
		serve(t, l, c, "cdn", 1)
		c.advance(recovery)
	}
	serve(t, l, c, "cdn", 1)
	if got := l.Rate("cdn"); !near(got, 8.5*margin) {
		t.Errorf("rate = %.2f/s after recovering, want %.2f/s, just under the refused pace", got, 8.5*margin)
	}
}

func TestRefusalWithoutRetryAfterDoesNotGuess(t *testing.T) {
	l, c := fake()
	serve(t, l, c, "cdn", 100)
	l.Refused("cdn", 2*time.Second, 0)
	if l.Rate("cdn") != 0 {
		t.Errorf("rate = %v, want unpaced: there is nothing to measure against", l.Rate("cdn"))
	}
	before := c.t
	l.Wait(context.Background(), "cdn")
	if waited := c.t.Sub(before); waited != 2*time.Second {
		t.Errorf("waited %s, want the 2s block", waited)
	}
}

func TestTooFewRequestsToMeasure(t *testing.T) {
	l, c := fake()
	serve(t, l, c, "cdn", 3)
	l.Refused("cdn", 10*time.Second, 10*time.Second)
	if l.Rate("cdn") != 0 {
		t.Errorf("rate = %v, want unpaced after only 3 requests", l.Rate("cdn"))
	}
}

func TestHostsAreIndependent(t *testing.T) {
	l, c := fake()
	serve(t, l, c, "a", 100)
	l.Refused("a", 10*time.Second, 10*time.Second)
	if took := serve(t, l, c, "b", 200); took != 0 {
		t.Errorf("host b waited %s while host a was blocked", took)
	}
}

func TestWaitStopsWhenCancelled(t *testing.T) {
	l := New()
	l.Refused("cdn", time.Hour, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Wait(ctx, "cdn"); err == nil {
		t.Error("expected an error from a cancelled wait")
	}
}

func TestZone(t *testing.T) {
	cases := map[string]string{
		// Subdomains of one domain share its limit.
		"https://blwhh.hiddenvertex.space/anime/a/b/seg-1.jpg": "hiddenvertex.space",
		"https://pazt1.hiddenvertex.space/x.png?token=1":       "hiddenvertex.space",
		"https://a.b.example.co.uk/x":                          "example.co.uk",
		"https://example.com:8443/x":                           "example.com",
		// Where there is no registered domain, the host stands for itself.
		"http://127.0.0.1:8080/seg.ts": "127.0.0.1",
		"http://localhost/seg.ts":      "localhost",
		"not a url":                    "not a url",
	}
	for raw, want := range cases {
		if got := Zone(raw); got != want {
			t.Errorf("Zone(%q) = %q, want %q", raw, got, want)
		}
	}
}
