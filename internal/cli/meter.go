package cli

import (
	"fmt"
	"time"

	"github.com/brysonreece/kotori/internal/hls"
)

const (
	// meterWindow is how far back the current speed is measured over.
	meterWindow = 15 * time.Second
	// meterMinimum is the shortest stretch a speed is trusted from.
	meterMinimum = 2 * time.Second
)

// meter measures how fast a download is going right now, as opposed to on
// average since it started. The average is a poor guide: downloads begin
// with a burst the server then stops, and holds add time in which nothing
// moves.
type meter struct {
	samples []sample
	// held is set while a rate limit holds the download up. What was
	// measured before a hold says little about the pace after it.
	held bool
}

type sample struct {
	at    time.Time
	done  int
	bytes int64
}

// add records the progress at a moment in time.
func (m *meter) add(at time.Time, p hls.Progress) {
	if m.held {
		m.samples, m.held = nil, false
	}
	m.samples = append(m.samples, sample{at, p.Done, p.Bytes})
	for len(m.samples) > 2 && at.Sub(m.samples[0].at) > meterWindow {
		m.samples = m.samples[1:]
	}
}

// hold notes that the download is being held up.
func (m *meter) hold() {
	m.held = true
}

// rates returns the current speed in segments and bytes per second. ok is
// false until there is enough to measure from.
func (m *meter) rates() (segments, bytes float64, ok bool) {
	if m.held || len(m.samples) < 2 {
		return 0, 0, false
	}
	first, last := m.samples[0], m.samples[len(m.samples)-1]
	span := last.at.Sub(first.at)
	if span < meterMinimum || last.done == first.done {
		return 0, 0, false
	}
	return float64(last.done-first.done) / span.Seconds(), float64(last.bytes-first.bytes) / span.Seconds(), true
}

// estimate is what is known about a download's speed at one moment.
type estimate struct {
	// segments and bytes are the measured speed per second; measured says
	// whether there is one yet.
	segments, bytes float64
	measured        bool
	// pace is the rate the server's limit holds the download to, in
	// segments per second, or zero if it is not being paced.
	pace float64
	// hold is how much longer a rate limit keeps the download waiting.
	hold time.Duration
}

// left estimates the time remaining: any hold still to sit out, then the
// remaining segments at the slower of the measured speed and the pace the
// server allows. ok is false when there is nothing to estimate from.
func (e estimate) left(p hls.Progress) (time.Duration, bool) {
	rate := e.pace
	if e.measured && (rate == 0 || e.segments < rate) {
		rate = e.segments
	}
	if rate <= 0 || p.Done >= p.Total {
		return 0, false
	}
	return e.hold + time.Duration(float64(p.Total-p.Done)/rate*float64(time.Second)), true
}

// progressText describes a download in flight: how much is done, how fast
// it is going and roughly how long is left.
func progressText(server string, p hls.Progress, e estimate) string {
	text := fmt.Sprintf("%s · %d/%d segments · %s", server, p.Done, p.Total, megabytes(p.Bytes))
	if e.measured && e.hold == 0 {
		text += fmt.Sprintf(" · %.1f MB/s", e.bytes/1e6)
	}
	if left, ok := e.left(p); ok {
		text += " · " + clock(left) + " left"
	}
	if e.hold > 0 {
		text += fmt.Sprintf(" · rate limited, resuming in %s", clock(e.hold))
	}
	return text
}

// clock writes a duration as m:ss, or h:mm:ss from an hour up, rounding up
// so that a countdown never shows zero while there is time to go.
func clock(d time.Duration) string {
	seconds := int((d + time.Second - 1) / time.Second)
	if seconds >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
	}
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
