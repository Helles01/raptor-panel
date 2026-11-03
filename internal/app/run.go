package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

// Options is what a running panel is made of: the handler of the router, the loops beside it, and where to listen.
type Options struct {
	Handler http.Handler
	Loops   []Loop
	Logger  *slog.Logger
	// Listen is host:port, from the configuration.
	Listen string
	// Web and Supervisor carry the limits of the server and the time to stop; their zero values are the defaults. Handler and Logger of Web
	// are set from this struct.
	Web        Web
	Supervisor Supervisor
	// Ready is called with the address that was opened, once the panel is listening (a test listens on port 0 and asks where).
	Ready func(addr net.Addr)
}

// Run opens the address and runs the web server and the loops together until the context ends or one of them fails. The address is opened
// first, so that a port that is taken stops the panel before any loop has done anything.
func Run(ctx context.Context, o Options) error {
	if o.Handler == nil || o.Logger == nil {
		return errors.New("app: running needs a handler and a logger")
	}
	ln, err := Listen(o.Listen)
	if err != nil {
		return err
	}
	if o.Ready != nil {
		o.Ready(ln.Addr())
	}
	web := o.Web
	web.Handler, web.Logger = o.Handler, o.Logger
	sup := o.Supervisor
	sup.Logger = o.Logger

	loops := append([]Loop{web.Loop(ln)}, o.Loops...)
	return sup.Run(ctx, loops...)
}

// Signals returns a context that ends when the process is asked to stop (Ctrl-C, or SIGTERM from a service manager). The function it
// returns gives the signals back to their default meaning: a second Ctrl-C then ends the process at once.
func Signals(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
