package middleware

import (
	"errors"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
)

const defaultMaxKeys = 100_000

// Limiter is a fixed-window rate limiter keyed by an arbitrary string. It lives in memory, per process.
type Limiter struct {
	limit   int
	window  time.Duration
	maxKeys int
	now     func() time.Time

	mu   sync.Mutex
	hits map[string]*hitWindow
}

type hitWindow struct {
	start time.Time
	count int
}

// NewLimiter allows limit requests per key in each window.
func NewLimiter(limit int, window time.Duration) (*Limiter, error) {
	if limit < 1 || window <= 0 {
		return nil, errors.New("a limiter needs a positive limit and window")
	}
	return &Limiter{limit: limit, window: window, maxKeys: defaultMaxKeys, now: time.Now, hits: map[string]*hitWindow{}}, nil
}

// Allow records one request for key. When it is over the limit it reports how long until the key may try again.
func (l *Limiter) Allow(key string) (allowed bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) >= l.window {
		if !ok {
			l.makeRoom(now)
		}
		l.hits[key] = &hitWindow{start: now, count: 1}
		return true, 0
	}

	w.count++
	if w.count > l.limit {
		return false, w.start.Add(l.window).Sub(now)
	}
	return true, 0
}

// makeRoom keeps the table bounded: it drops expired windows and, if the table is still full, an arbitrary tenth of it.
// Blocked reports whether key has already failed limit times in the current window and for how much longer it stays blocked.
// It counts nothing. With Fail it limits an endpoint where only the failures should count, such as a sign-in: a client with
// many successful sessions is not slowed down. A Limiter is used with Allow, or with Blocked and Fail, not with both.
func (l *Limiter) Blocked(key string) (blocked bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) >= l.window || w.count < l.limit {
		return false, 0
	}
	return true, w.start.Add(l.window).Sub(now)
}

// Fail counts one failure for key.
func (l *Limiter) Fail(key string) { l.Allow(key) }

func (l *Limiter) makeRoom(now time.Time) {
	if len(l.hits) < l.maxKeys {
		return
	}
	for k, w := range l.hits {
		if now.Sub(w.start) >= l.window {
			delete(l.hits, k)
		}
	}
	drop := l.maxKeys / 10
	for k := range l.hits {
		if len(l.hits) < l.maxKeys {
			break
		}
		if drop--; drop < 0 {
			break
		}
		delete(l.hits, k)
	}
}

// KeyFunc returns the bucket a request counts against.
//
// It must separate requests: a key that is the same for everybody turns the limiter into a switch that one client can
// flip for all others (the reference once locked every user out of login that way). Include the client address, and for
// login-like endpoints also the account.
type KeyFunc func(c echo.Context) string

// KeyByIP counts per client address.
func KeyByIP(c echo.Context) string { return c.RealIP() }

// KeyByIPAnd counts per client address and one more value of the request, such as the login name.
func KeyByIPAnd(value func(c echo.Context) string) KeyFunc {
	return func(c echo.Context) string {
		return c.RealIP() + "|" + value(c)
	}
}

// Throttle rejects requests over the limit with a 429. A request whose key is empty is refused with a 500 instead of
// being counted: failing closed is better than letting everybody share one bucket.
func Throttle(l *Limiter, key KeyFunc) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			k := key(c)
			if k == "" || k == "|" {
				return apierr.Internal(errors.New("throttle: the key function returned an empty key"))
			}
			if ok, retry := l.Allow(k); !ok {
				return apierr.TooManyRequests(retry)
			}
			return next(c)
		}
	}
}
