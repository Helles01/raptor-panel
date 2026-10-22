package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestLimiter(t *testing.T, limit int, window time.Duration) (*Limiter, *fakeClock) {
	t.Helper()
	l, err := NewLimiter(limit, window)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	l.now = clock.now
	return l, clock
}

func TestLimiterWindow(t *testing.T) {
	l, clock := newTestLimiter(t, 3, time.Minute)

	for i := 1; i <= 3; i++ {
		if ok, _ := l.Allow("a"); !ok {
			t.Fatalf("request %d of 3 was refused", i)
		}
	}
	ok, retry := l.Allow("a")
	if ok || retry != time.Minute {
		t.Errorf("fourth request: allowed=%v retry=%v, want refused for 1m", ok, retry)
	}

	clock.t = clock.t.Add(40 * time.Second)
	if ok, retry := l.Allow("a"); ok || retry != 20*time.Second {
		t.Errorf("later in the window: allowed=%v retry=%v, want refused for 20s", ok, retry)
	}

	clock.t = clock.t.Add(20 * time.Second)
	if ok, _ := l.Allow("a"); !ok {
		t.Error("a new window must allow requests again")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l, _ := newTestLimiter(t, 2, time.Minute)
	l.Allow("a")
	l.Allow("a")
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("key a should be exhausted")
	}

	if ok, _ := l.Allow("b"); !ok {
		t.Error("exhausting one key must not affect another")
	}
}

func TestNewLimiterRejectsBadSettings(t *testing.T) {
	for _, tt := range []struct {
		limit  int
		window time.Duration
	}{{0, time.Minute}, {-1, time.Minute}, {5, 0}, {5, -time.Second}} {
		if _, err := NewLimiter(tt.limit, tt.window); err == nil {
			t.Errorf("NewLimiter(%d, %v) succeeded", tt.limit, tt.window)
		}
	}
}

func TestLimiterStaysBounded(t *testing.T) {
	l, clock := newTestLimiter(t, 1, time.Minute)
	l.maxKeys = 100

	for i := 0; i < 1000; i++ {
		l.Allow(fmt.Sprintf("key-%d", i))
	}
	if len(l.hits) > 100 {
		t.Errorf("table grew to %d entries, want at most 100", len(l.hits))
	}

	clock.t = clock.t.Add(2 * time.Minute)
	l.Allow("fresh")
	if len(l.hits) > 100 {
		t.Errorf("expired entries were not dropped: %d", len(l.hits))
	}
}

// echoWith returns an Echo whose client address detection works like production: only trusted proxies are believed.
func echoWith(t *testing.T, trusted ...string) *echo.Echo {
	t.Helper()
	var prefixes []netip.Prefix
	for _, s := range trusted {
		prefixes = append(prefixes, netip.MustParsePrefix(s))
	}
	e := echo.New()
	e.IPExtractor = NewProxyTrust(prefixes, nil).ClientIP
	e.HTTPErrorHandler = func(err error, c echo.Context) {
		var ae *apierr.Error
		if errors.As(err, &ae) {
			apierr.Write(c.Response(), ae)
			return
		}
		apierr.Write(c.Response(), apierr.Internal(err))
	}
	return e
}

func do(e *echo.Echo, remote string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = remote
	if mutate != nil {
		mutate(r)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, r)
	return rec
}

func TestThrottlePerClientAddress(t *testing.T) {
	l, _ := newTestLimiter(t, 3, time.Minute)
	e := echoWith(t)
	e.POST("/login", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, Throttle(l, KeyByIP))

	for i := 1; i <= 3; i++ {
		if rec := do(e, "198.51.100.1:1000", nil); rec.Code != http.StatusNoContent {
			t.Fatalf("request %d from A: status %d", i, rec.Code)
		}
	}
	blocked := do(e, "198.51.100.1:1000", nil)
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") != "60" {
		t.Errorf("fourth request from A: status %d retry-after %q", blocked.Code, blocked.Header().Get("Retry-After"))
	}
	if !strings.Contains(blocked.Body.String(), "ThrottleRequestsException") {
		t.Errorf("body = %s", blocked.Body)
	}

	// The reference once used one shared bucket, so a single address could lock out every user.
	if rec := do(e, "198.51.100.2:2000", nil); rec.Code != http.StatusNoContent {
		t.Errorf("client B was affected by client A: status %d", rec.Code)
	}
}

func TestThrottleCannotBeSteeredByForgedHeaders(t *testing.T) {
	l, _ := newTestLimiter(t, 2, time.Minute)
	e := echoWith(t, "10.0.0.0/8")
	e.POST("/login", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, Throttle(l, KeyByIP))

	forge := func(ip string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("X-Forwarded-For", ip) }
	}

	// An untrusted client rotating the header still counts as one address.
	do(e, "198.51.100.1:1", forge("1.1.1.1"))
	do(e, "198.51.100.1:1", forge("2.2.2.2"))
	if rec := do(e, "198.51.100.1:1", forge("3.3.3.3")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("rotating X-Forwarded-For escaped the limit: status %d", rec.Code)
	}

	// Through the trusted proxy, different real clients are separate buckets.
	do(e, "10.0.0.5:1", forge("203.0.113.1"))
	do(e, "10.0.0.5:1", forge("203.0.113.1"))
	if rec := do(e, "10.0.0.5:1", forge("203.0.113.1")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("third request of one real client: status %d", rec.Code)
	}
	if rec := do(e, "10.0.0.5:1", forge("203.0.113.2")); rec.Code != http.StatusNoContent {
		t.Errorf("another client behind the same proxy was blocked: status %d", rec.Code)
	}
}

func TestThrottleKeyByIPAnd(t *testing.T) {
	l, _ := newTestLimiter(t, 1, time.Minute)
	e := echoWith(t)
	user := func(c echo.Context) string { return c.QueryParam("user") }
	e.POST("/login", func(c echo.Context) error { return c.NoContent(http.StatusNoContent) }, Throttle(l, KeyByIPAnd(user)))

	post := func(remote, query string) int {
		return do(e, remote, func(r *http.Request) { r.URL.RawQuery = query }).Code
	}

	if post("198.51.100.1:1", "user=ada") != http.StatusNoContent {
		t.Fatal("first attempt for ada was refused")
	}
	if post("198.51.100.1:1", "user=ada") != http.StatusTooManyRequests {
		t.Error("second attempt for the same address and account must be refused")
	}
	if post("198.51.100.1:1", "user=bob") != http.StatusNoContent {
		t.Error("another account from the same address has its own bucket")
	}
	if post("198.51.100.2:1", "user=ada") != http.StatusNoContent {
		t.Error("the same account from another address has its own bucket")
	}
}

func TestThrottleFailsClosedOnAnEmptyKey(t *testing.T) {
	l, _ := newTestLimiter(t, 1, time.Minute)
	e := echoWith(t)
	reached := 0
	e.POST("/login", func(c echo.Context) error { reached++; return c.NoContent(http.StatusNoContent) },
		Throttle(l, func(echo.Context) string { return "" }))

	for i := 0; i < 3; i++ {
		if rec := do(e, "198.51.100.1:1", nil); rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 for an empty key", rec.Code)
		}
	}
	if reached != 0 {
		t.Error("a request with an empty key must not reach the handler")
	}
	if len(l.hits) != 0 {
		t.Error("an empty key must not be counted in a shared bucket")
	}
}
