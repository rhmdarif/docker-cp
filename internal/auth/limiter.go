package auth

import (
	"net/netip"
	"sync"
	"time"
)

const (
	// pruneAbove: Take sweeps expired keys once the map is larger than this.
	pruneAbove = 1024
	// maxLimiterKeys caps tracked keys; a new key beyond it is refused, so a
	// flood of distinct addresses cannot grow memory without bound.
	maxLimiterKeys = 10000
)

// LimiterKey maps a client IP to its limiter key: IPv4 (including
// IPv4-mapped IPv6) as-is, IPv6 by its /64 prefix, since one host usually
// owns a whole /64. Unparseable input is returned unchanged.
func LimiterKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is4() {
		return a.String()
	}
	p, err := a.WithZone("").Prefix(64)
	if err != nil {
		return ip
	}
	return p.String()
}

// Limiter blocks a key once it has max failures within window.
type Limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	now    func() time.Time
	fails  map[string][]time.Time
}

func NewLimiter(max int, window time.Duration, now func() time.Time) *Limiter {
	return &Limiter{max: max, window: window, now: now, fails: map[string][]time.Time{}}
}

// recent drops failures older than the window; caller holds l.mu.
func (l *Limiter) recent(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	old := l.fails[key]
	kept := old[:0]
	for _, t := range old {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.fails, key)
		return nil
	}
	l.fails[key] = kept
	return kept
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) < l.max
}

func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fails[key] = append(l.recent(key), l.now())
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	delete(l.fails, key)
	l.mu.Unlock()
}

// Take checks and records an attempt for key under one lock. It returns
// false, recording nothing, when max attempts already fall in the window.
// Use it before the expensive check so concurrent requests cannot all slip
// past a separate Allow; call Reset on success.
// Past maxLimiterKeys tracked keys, a key not yet tracked is refused.
func (l *Limiter) Take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.fails) > pruneAbove {
		for k := range l.fails {
			l.recent(k)
		}
	}
	recent := l.recent(key)
	if recent == nil && len(l.fails) >= maxLimiterKeys {
		return false
	}
	if len(recent) >= l.max {
		return false
	}
	l.fails[key] = append(recent, l.now())
	return true
}

// size returns the number of tracked keys (for tests).
func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.fails)
}
