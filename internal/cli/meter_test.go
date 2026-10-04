package cli

import (
	"testing"
	"time"

	"github.com/brysonreece/kotori/internal/hls"
)

var t0 = time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)

// feed adds one sample per second: perSecond segments of a megabyte each.
func feed(m *meter, from time.Time, start, seconds, perSecond int) (time.Time, int) {
	done := start
	for s := 0; s <= seconds; s++ {
		m.add(from.Add(time.Duration(s)*time.Second), hls.Progress{Done: done, Total: 1000, Bytes: int64(done) * 1e6})
		done += perSecond
	}
	return from.Add(time.Duration(seconds) * time.Second), done - perSecond
}

func TestMeterMeasuresTheCurrentSpeed(t *testing.T) {
	var m meter
	if _, _, ok := m.rates(); ok {
		t.Error("a speed was reported before anything was measured")
	}
	m.add(t0, hls.Progress{Done: 1, Total: 1000})
	m.add(t0.Add(time.Second), hls.Progress{Done: 40, Total: 1000})
	if _, _, ok := m.rates(); ok {
		t.Error("a speed was reported from one second of data")
	}

	// A burst of 40 a second, then a steady 9: the speed is the recent one.
	m = meter{}
	at, done := feed(&m, t0, 0, 3, 40)
	feed(&m, at.Add(time.Second), done+9, 20, 9)
	segments, bytes, ok := m.rates()
	if !ok || segments < 8.9 || segments > 9.1 || bytes < 8.9e6 || bytes > 9.1e6 {
		t.Errorf("rates = %.1f segments/s, %.0f bytes/s, %v; want about 9 and 9 MB", segments, bytes, ok)
	}
}

func TestMeterStartsAgainAfterAHold(t *testing.T) {
	var m meter
	at, done := feed(&m, t0, 0, 5, 40)
	m.hold()
	if _, _, ok := m.rates(); ok {
		t.Error("a speed was reported during a hold")
	}
	// Eleven seconds pass with nothing moving, then a slower steady pace.
	resumed := at.Add(11 * time.Second)
	m.add(resumed, hls.Progress{Done: done + 1, Total: 1000, Bytes: int64(done+1) * 1e6})
	if _, _, ok := m.rates(); ok {
		t.Error("a speed was reported from a single sample after a hold")
	}
	feed(&m, resumed.Add(time.Second), done+10, 5, 9)
	if segments, _, ok := m.rates(); !ok || segments < 8.9 || segments > 9.1 {
		t.Errorf("after the hold: %.1f segments/s, %v; want about 9, unaffected by the burst or the wait", segments, ok)
	}
}

func TestEstimateLeft(t *testing.T) {
	p := hls.Progress{Done: 100, Total: 280}
	cases := []struct {
		name string
		e    estimate
		want time.Duration
		ok   bool
	}{
		{"nothing known yet", estimate{}, 0, false},
		{"measured speed", estimate{segments: 9, measured: true}, 20 * time.Second, true},
		// The burst before the first refusal is faster than the server will allow for long.
		{"measured above the pace", estimate{segments: 40, measured: true, pace: 9}, 20 * time.Second, true},
		// Sharing the host with other episodes leaves less than the full pace.
		{"measured below the pace", estimate{segments: 3, measured: true, pace: 9}, 60 * time.Second, true},
		{"held, with a pace to resume at", estimate{pace: 9, hold: 10 * time.Second}, 30 * time.Second, true},
		{"held, with no idea of the pace", estimate{hold: 10 * time.Second}, 0, false},
	}
	for _, c := range cases {
		got, ok := c.e.left(p)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: left = %s, %v; want %s, %v", c.name, got, ok, c.want, c.ok)
		}
	}
	if _, ok := (estimate{segments: 9, measured: true}).left(hls.Progress{Done: 280, Total: 280}); ok {
		t.Error("a finished download still has time left")
	}
}

func TestProgressText(t *testing.T) {
	p := hls.Progress{Done: 100, Total: 280, Bytes: 50_000_000}
	cases := []struct {
		e    estimate
		want string
	}{
		{estimate{}, "hd · 100/280 segments · 50.0 MB"},
		{estimate{segments: 9, bytes: 4_500_000, measured: true}, "hd · 100/280 segments · 50.0 MB · 4.5 MB/s · 0:20 left"},
		// During a hold nothing is moving, so no speed is shown, and the
		// time left includes the wait.
		{estimate{pace: 9, hold: 9500 * time.Millisecond}, "hd · 100/280 segments · 50.0 MB · 0:30 left · rate limited, resuming in 0:10"},
		{estimate{hold: 2 * time.Second}, "hd · 100/280 segments · 50.0 MB · rate limited, resuming in 0:02"},
	}
	for _, c := range cases {
		if got := progressText("hd", p, c.e); got != c.want {
			t.Errorf("got  %q\nwant %q", got, c.want)
		}
	}
}

func TestClock(t *testing.T) {
	cases := map[time.Duration]string{
		0:                      "0:00",
		100 * time.Millisecond: "0:01",
		59 * time.Second:       "0:59",
		61 * time.Second:       "1:01",
		3725 * time.Second:     "1:02:05",
	}
	for d, want := range cases {
		if got := clock(d); got != want {
			t.Errorf("clock(%s) = %q, want %q", d, got, want)
		}
	}
}
