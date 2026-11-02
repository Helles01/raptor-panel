package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/middleware"
)

// The limits of the web server. The panel receives small requests (the biggest body is an egg file), and the browser talks to the nodes
// directly for files and the console, so nothing here has to stay open for long; a client that is slower than this is not a browser.
const (
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultReadTimeout       = 60 * time.Second
	DefaultWriteTimeout      = 60 * time.Second
	DefaultIdleTimeout       = 2 * time.Minute
	DefaultShutdownTimeout   = 30 * time.Second
	// MaxHeaderBytes is the most the headers of a request may take, cookies included.
	MaxHeaderBytes = 64 << 10
)

// Web is the panel's web server: the handler of the router, served on a listener, stopped gracefully.
type Web struct {
	Handler http.Handler
	Logger  *slog.Logger
	// The limits; the Default... values when 0.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	// ShutdownTimeout is how long requests in progress get to finish once the panel is told to stop.
	ShutdownTimeout time.Duration
}

// Listen opens the address of the configuration. It is separate from Serve so that a port that is taken is found out at once, before any
// loop starts, and so that a test can listen on port 0 and ask where it is.
func Listen(addr string) (net.Listener, error) {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("the listen address %q is not host:port: %w", addr, err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}
	return ln, nil
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

func (w Web) server() *http.Server {
	return &http.Server{
		Handler:           w.Handler,
		ReadHeaderTimeout: orDefault(w.ReadHeaderTimeout, DefaultReadHeaderTimeout),
		ReadTimeout:       orDefault(w.ReadTimeout, DefaultReadTimeout),
		WriteTimeout:      orDefault(w.WriteTimeout, DefaultWriteTimeout),
		IdleTimeout:       orDefault(w.IdleTimeout, DefaultIdleTimeout),
		MaxHeaderBytes:    MaxHeaderBytes,
		// The server's own complaints (a bad request line, a broken connection) go to the log of the panel, not to standard error.
		ErrorLog: slog.NewLogLogger(middleware.NewRedactingHandler(w.Logger.Handler()), slog.LevelWarn),
	}
}

// Serve answers requests on the listener until the context ends, then stops taking new ones and gives the ones in progress
// ShutdownTimeout to finish. It returns nil after a clean stop. The listener is closed when it returns.
func (w Web) Serve(ctx context.Context, ln net.Listener) error {
	if w.Handler == nil || w.Logger == nil || ln == nil {
		return errors.New("app: the web server needs a handler, a logger and a listener")
	}
	srv := w.server()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	w.Logger.Info("the web server is listening", "address", ln.Addr().String())

	select {
	case err := <-served:
		// Serve only returns by itself when something is wrong with the listener.
		return fmt.Errorf("the web server stopped: %w", err)
	case <-ctx.Done():
	}

	shutdown, cancel := context.WithTimeout(context.Background(), orDefault(w.ShutdownTimeout, DefaultShutdownTimeout))
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		// Requests that did not finish are cut off; their clients see a closed connection.
		_ = srv.Close()
		<-served
		return fmt.Errorf("requests were still in progress after %v and were cut off: %w", orDefault(w.ShutdownTimeout, DefaultShutdownTimeout), err)
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Loop is the web server as a loop for the Supervisor.
func (w Web) Loop(ln net.Listener) Loop {
	return Loop{Name: "http", Run: func(ctx context.Context) error { return w.Serve(ctx, ln) }}
}
