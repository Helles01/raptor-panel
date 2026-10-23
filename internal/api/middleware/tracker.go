package middleware

import (
	"sync"
	"time"

	"github.com/Helles01/raptor-panel/internal/auth/lockout"
)

// Tracker counts failures against counters that differ in their limits and windows, such as the three counters of a sign-in
// attempt. It keeps one Limiter for each pair of limit and window and counts in memory, per process: a restart empties it, and
// with several processes each has its own.
type Tracker struct {
	mu       sync.Mutex
	limiters map[tracked]*Limiter
	now      func() time.Time
}

type tracked struct {
	limit  int
	window time.Duration
}

// NewTracker returns an empty Tracker.
func NewTracker() *Tracker {
	return &Tracker{limiters: map[tracked]*Limiter{}, now: time.Now}
}

func (t *Tracker) limiter(c lockout.Counter) (*Limiter, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := tracked{c.Limit, c.Window}
	if l, ok := t.limiters[k]; ok {
		return l, true
	}
	l, err := NewLimiter(c.Limit, c.Window)
	if err != nil {
		return nil, false
	}
	l.now = t.now
	t.limiters[k] = l
	return l, true
}

// Blocked reports whether any of the counters has used up its failures, and for how long the longest of them stays blocked. A
// counter that cannot work (no limit, or no window) counts as blocked: failing closed is better than not counting at all.
func (t *Tracker) Blocked(counters []lockout.Counter) (blocked bool, retryAfter time.Duration) {
	for _, c := range counters {
		l, ok := t.limiter(c)
		if !ok {
			return true, c.Window
		}
		if b, retry := l.Blocked(c.Key); b {
			blocked = true
			retryAfter = max(retryAfter, retry)
		}
	}
	return blocked, retryAfter
}

// Fail counts one failure on every counter.
func (t *Tracker) Fail(counters []lockout.Counter) {
	for _, c := range counters {
		if l, ok := t.limiter(c); ok {
			l.Fail(c.Key)
		}
	}
}
