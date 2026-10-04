// Package throttle keeps requests to each host under that host's rate limit.
//
// A "host" here is a registered domain, such as example.com: CDNs apply one
// limit across all of a domain's subdomains, so they share one allowance.
// Use Zone to get it from a URL.
//
// Nothing is assumed about a host until it refuses a request. Until then
// requests go out as fast as the caller makes them. The first refusal shows
// roughly where the limit is, and from then on requests to that host are
// spaced to stay under it, which is faster than repeatedly running into the
// limit and sitting out the block that follows.
package throttle

import (
	"context"
	"net"
	"net/url"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

const (
	// safety is the fraction of the measured limit that requests are paced at.
	safety = 0.85
	// minSample is the fewest served requests a measurement is trusted from.
	minSample = 10
	// slowdown is applied to the pace when a paced host refuses again.
	slowdown = 0.8
	// recovery is how long a host must go without refusing before its pace
	// is raised, and speedup is by how much.
	recovery = time.Minute
	speedup  = 1.1
	// margin is how close the pace may come back to one that was refused.
	margin = 0.95
	// memory is how far back served requests are remembered for measuring.
	memory = 2 * time.Minute
)

// Limiter paces requests per host. One Limiter is shared by everything that
// talks to the same hosts, so that parallel downloads share each host's
// allowance instead of each spending it separately.
type Limiter struct {
	mu    sync.Mutex
	hosts map[string]*host

	// Logf, when set, is told whenever a host is blocked or its pace changes.
	Logf func(format string, args ...any)

	// now and sleep are replaced in tests.
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
}

type host struct {
	// rate is the pace in requests per second; zero means unpaced.
	rate float64
	// ceiling is a pace this host is known to refuse; zero if none is known.
	ceiling float64
	// next is when the next paced request may be sent.
	next time.Time
	// blockedUntil is when the host is expected to accept requests again.
	blockedUntil time.Time
	// served holds the times of recent served requests while unpaced, to
	// measure the limit from when the first refusal comes.
	served []time.Time
	// refused records that the host has refused at least once, and
	// sinceRefusal counts requests served since its last block ended.
	refused      bool
	sinceRefusal int
	// changed is when the pace was last set.
	changed time.Time
}

// Zone returns the name requests to rawURL are limited under: the registered
// domain of its host, or the host itself where there is no such thing, as
// with an IP address.
func Zone(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return rawURL
	}
	if net.ParseIP(u.Hostname()) != nil {
		return u.Hostname()
	}
	if zone, err := publicsuffix.EffectiveTLDPlusOne(u.Hostname()); err == nil {
		return zone
	}
	return u.Hostname()
}

// New returns a Limiter with no knowledge of any host.
func New() *Limiter {
	return &Limiter{
		hosts: map[string]*host{},
		now:   time.Now,
		sleep: func(ctx context.Context, d time.Duration) error {
			select {
			case <-time.After(d):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
}

func (l *Limiter) host(name string) *host {
	h, ok := l.hosts[name]
	if !ok {
		h = &host{}
		l.hosts[name] = h
	}
	return h
}

// Wait blocks until a request to the named host may be sent.
func (l *Limiter) Wait(ctx context.Context, name string) error {
	for {
		l.mu.Lock()
		h := l.host(name)
		now := l.now()
		wait := h.blockedUntil.Sub(now)
		blocked := wait > 0
		if !blocked {
			// Take the next slot. Unpaced hosts have no spacing, so the
			// slot is always now.
			slot := h.next
			if slot.Before(now) {
				slot = now
			}
			if h.rate > 0 {
				h.next = slot.Add(time.Duration(float64(time.Second) / h.rate))
			}
			wait = slot.Sub(now)
		}
		l.mu.Unlock()

		if wait > 0 {
			if err := l.sleep(ctx, wait); err != nil {
				return err
			}
		} else if err := ctx.Err(); err != nil {
			return err
		}
		if blocked {
			continue
		}
		// A block may have started while waiting for the slot.
		l.mu.Lock()
		blocked = l.now().Before(h.blockedUntil)
		l.mu.Unlock()
		if !blocked {
			return nil
		}
	}
}

// Allowed records that the named host served a request.
func (l *Limiter) Allowed(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h := l.host(name)
	now := l.now()
	if now.Before(h.blockedUntil) {
		// A straggler sent before the block began; it says nothing new.
		return
	}
	h.sinceRefusal++
	if h.rate == 0 {
		cutoff := now.Add(-memory)
		for len(h.served) > 0 && h.served[0].Before(cutoff) {
			h.served = h.served[1:]
		}
		h.served = append(h.served, now)
		return
	}
	// A pace that has held for a while may be lower than it needs to be,
	// for instance when it was measured while something else was using the
	// host. Raise it gently, but never back up to a pace that was refused.
	if now.Sub(h.changed) >= recovery {
		raised := h.rate * speedup
		if h.ceiling > 0 {
			raised = min(raised, h.ceiling*margin)
		}
		if raised > h.rate {
			h.rate = raised
			l.logf("%s: no refusals for a while, pace raised to %.1f requests/s", name, h.rate)
		}
		h.changed = now
	}
}

func (l *Limiter) logf(format string, args ...any) {
	if l.Logf != nil {
		l.Logf(format, args...)
	}
}

// Refused records that the named host refused a request as too many, and
// blocks the host for pause. retryAfter is what the host itself asked for,
// or zero if it did not say.
func (l *Limiter) Refused(name string, pause, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h := l.host(name)
	now := l.now()

	// Refusals come in waves: every request in flight when the limit is hit
	// is refused. Only the first of a wave is news. A refusal with nothing
	// served since the last block just means that block is not over yet.
	if !now.Before(h.blockedUntil) && (!h.refused || h.sinceRefusal > 0) {
		switch {
		case h.rate > 0:
			h.ceiling = h.rate
			h.rate *= slowdown
			h.changed = now
			l.logf("%s: refused at %.1f requests/s after %d served, pace lowered to %.1f", name, h.ceiling, h.sinceRefusal, h.rate)
		case retryAfter > 0:
			// The host served this many requests in the time it now asks
			// us to wait, which is taken as its limit.
			n := 0
			for _, at := range h.served {
				if now.Sub(at) <= retryAfter {
					n++
				}
			}
			if n >= minSample {
				h.rate = safety * float64(n) / retryAfter.Seconds()
				h.changed = now
				h.served = nil
				l.logf("%s: refused after serving %d in %s, pacing at %.1f requests/s", name, n, retryAfter, h.rate)
			}
		}
	}
	h.refused, h.sinceRefusal = true, 0
	if until := now.Add(pause); until.After(h.blockedUntil) {
		if !now.Before(h.blockedUntil) {
			l.logf("%s: blocked for %s", name, pause.Round(time.Millisecond))
		}
		h.blockedUntil = until
	}
	// Slots handed out before the block are void; pacing restarts after it.
	h.next = h.blockedUntil
}

// Blocked reports how much longer the named host is expected to refuse
// requests, or zero if it is not blocked.
func (l *Limiter) Blocked(name string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return max(l.host(name).blockedUntil.Sub(l.now()), 0)
}

// Rate reports the pace the named host is held to, in requests per second,
// or zero if it is not being paced.
func (l *Limiter) Rate(name string) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.host(name).rate
}
