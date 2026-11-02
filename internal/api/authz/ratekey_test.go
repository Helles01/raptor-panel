package authz_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/apitest"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
)

func limited(t *testing.T, limit int) (*apitest.Client, *int) {
	t.Helper()
	w := apitest.NewWorld()
	w.AddUser(1, false)
	w.AddUser(2, false)
	l, err := middleware.NewLimiter(limit, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	return w.Serve(t, func(g api.Group) {
		g.AfterAuth(middleware.Throttle(l, authz.RateKey)).GET("/api/ping", func(c echo.Context) error {
			calls++
			return c.NoContent(http.StatusNoContent)
		}, api.Authenticated())
	}), &calls
}

func TestTheLimitCountsPerUser(t *testing.T) {
	client, calls := limited(t, 3)
	for i := 0; i < 3; i++ {
		if rp := client.As(1, "GET", "/api/ping", ""); rp.Status != 204 {
			t.Fatalf("request %d: %d", i+1, rp.Status)
		}
	}
	rp := client.As(1, "GET", "/api/ping", "")
	if rp.Status != 429 || rp.Header.Get("Retry-After") == "" || *calls != 3 {
		t.Errorf("the fourth: %d %v, handler calls %d", rp.Status, rp.Header, *calls)
	}
	// Another user has a bucket of their own, whatever the address.
	if rp := client.As(2, "GET", "/api/ping", ""); rp.Status != 204 {
		t.Errorf("another user: %d", rp.Status)
	}
}

func TestARequestTheAuthorizerRefusesIsNotCounted(t *testing.T) {
	client, calls := limited(t, 1)
	// A request without a key never reaches the limit: it is not the user's, and it must not use their allowance up.
	for i := 0; i < 5; i++ {
		if rp := client.As(0, "GET", "/api/ping", ""); rp.Status != 401 {
			t.Fatalf("nobody: %d", rp.Status)
		}
	}
	if rp := client.As(1, "GET", "/api/ping", ""); rp.Status != 204 || *calls != 1 {
		t.Errorf("the user: %d", rp.Status)
	}
}

func TestRateKey(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	c := e.NewContext(req, httptest.NewRecorder())
	if got := authz.RateKey(c); got != "203.0.113.9" {
		t.Errorf("no user: %q", got)
	}
}
