package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
)

// Options are the dependencies of the server.
type Options struct {
	// Logger receives the request log. Whatever handler it has is wrapped so that secrets are redacted.
	Logger *slog.Logger
	// Trust decides which proxies are believed about the client address. Nil trusts none.
	Trust *middleware.ProxyTrust
	// Authorizer decides access for every route.
	Authorizer Authorizer
}

// Server is the HTTP server before it is started.
type Server struct {
	Echo   *echo.Echo
	Router *Router
}

// New builds the server with its fixed middleware chain: recover, request ID, request log, security headers.
func New(opts Options) (*Server, error) {
	if opts.Logger == nil {
		return nil, errors.New("api: a logger is required")
	}
	if opts.Authorizer == nil {
		return nil, errors.New("api: an authorizer is required")
	}
	trust := opts.Trust
	if trust == nil {
		trust = middleware.NewProxyTrust(nil, opts.Logger)
	}
	log := slog.New(middleware.NewRedactingHandler(opts.Logger.Handler()))

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	// Without an extractor Echo believes X-Forwarded-For and X-Real-IP from anybody.
	e.IPExtractor = trust.ClientIP
	e.HTTPErrorHandler = errorHandler
	e.Use(middleware.Recover(log), middleware.RequestID(), middleware.RequestLogger(log), middleware.SecurityHeaders())

	return &Server{Echo: e, Router: NewRouter(e, opts.Authorizer)}, nil
}

// Handler returns the http.Handler to serve. It refuses if a route was added without a permission.
func (s *Server) Handler() (http.Handler, error) {
	if err := s.Router.Verify(); err != nil {
		return nil, err
	}
	return s.Echo, nil
}

// errorHandler writes every error in the API's JSON format. Anything that is not an API error is logged by the request
// logger and answered with the generic 500.
func errorHandler(err error, c echo.Context) {
	if c.Response().Committed {
		return
	}

	ae := toAPIError(err)
	if c.Request().Method == http.MethodHead {
		c.Response().WriteHeader(ae.Status)
		return
	}
	apierr.Write(c.Response(), ae)
}

func toAPIError(err error) *apierr.Error {
	var ae *apierr.Error
	if errors.As(err, &ae) {
		return ae
	}

	var he *echo.HTTPError
	if errors.As(err, &he) {
		switch he.Code {
		case http.StatusNotFound:
			return apierr.NotFound()
		case http.StatusMethodNotAllowed:
			return apierr.MethodNotAllowed()
		case http.StatusBadRequest:
			return apierr.BadRequest("The request is malformed.")
		case http.StatusRequestEntityTooLarge:
			return apierr.TooLarge()
		case http.StatusUnauthorized:
			return apierr.Unauthenticated()
		case http.StatusForbidden:
			return apierr.Forbidden("This action is unauthorized.")
		}
		if he.Code >= 400 && he.Code < 500 {
			return &apierr.Error{Status: he.Code, Code: apierr.CodeHTTP, Detail: http.StatusText(he.Code)}
		}
	}
	return apierr.Internal(err)
}
