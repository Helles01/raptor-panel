// Package healthapi serves the two endpoints that a load balancer, a container runtime or a monitor asks whether the panel is well:
// /healthz says the process is up and answering, /readyz says it can reach what it needs (the database, for one). Neither needs a login,
// so neither tells more than "ok" or "failed" for each check: what failed is in the log, not in the answer.
package healthapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
)

// Check looks at one thing the panel depends on. An error means it is not well.
type Check func(ctx context.Context) error

// DefaultTimeout is how long the checks may take together.
const DefaultTimeout = 3 * time.Second

// Deps are everything the endpoints need.
type Deps struct {
	// Checks are what /readyz looks at, by name.
	Checks map[string]Check
	// Timeout bounds the checks together; DefaultTimeout when 0. A check that is not done by then has failed.
	Timeout time.Duration
	// Limiter, if set, limits the requests of one client address.
	Limiter *middleware.Limiter
	Logger  *slog.Logger
}

// Handlers are the endpoints.
type Handlers struct {
	d Deps
}

// New checks the dependencies and returns the endpoints.
func New(d Deps) (*Handlers, error) {
	if d.Logger == nil {
		return nil, errors.New("healthapi: a logger is required")
	}
	for name, c := range d.Checks {
		if name == "" || c == nil {
			return nil, errors.New("healthapi: a check needs a name and something to run")
		}
	}
	if d.Timeout <= 0 {
		d.Timeout = DefaultTimeout
	}
	return &Handlers{d: d}, nil
}

// Register adds /healthz and /readyz to the group.
func (h *Handlers) Register(g api.Group) {
	var mw []echo.MiddlewareFunc
	if h.d.Limiter != nil {
		mw = append(mw, middleware.Throttle(h.d.Limiter, middleware.KeyByIP))
	}
	g.GET("/healthz", h.Live, api.Public(), mw...)
	g.GET("/readyz", h.Ready, api.Public(), mw...)
}

type live struct {
	Status string `json:"status"`
}

type ready struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// Live is GET /healthz: the process is there and answers.
func (h *Handlers) Live(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, live{Status: "ok"})
}

// Ready is GET /readyz: every check is run, all at once, and the panel is ready if none failed.
func (h *Handlers) Ready(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), h.d.Timeout)
	defer cancel()

	results := make(map[string]string, len(h.d.Checks))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, check := range h.d.Checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := h.run(ctx, check)
			outcome := "ok"
			if err != nil {
				outcome = "failed"
				h.d.Logger.Warn("a readiness check failed", "check", name, "error", err)
			}
			mu.Lock()
			results[name] = outcome
			mu.Unlock()
		}()
	}
	wg.Wait()

	status, code := "ok", http.StatusOK
	for _, outcome := range results {
		if outcome != "ok" {
			status, code = "unavailable", http.StatusServiceUnavailable
		}
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(code, ready{Status: status, Checks: results})
}

// run does one check, and ends it when the time is up, whether or not it noticed. A panic is a failure.
func (h *Handlers) run(ctx context.Context, check Check) error {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- fmt.Errorf("the check panicked: %v", p)
			}
		}()
		done <- check(ctx)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
