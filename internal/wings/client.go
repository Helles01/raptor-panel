package wings

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxResponseBytes = 1 << 20
	maxCommands      = 100
	maxCommandLength = 4096
	maxMessageLength = 500
)

// PowerActions are the signals a server accepts.
var powerActions = map[string]bool{"start": true, "stop": true, "restart": true, "kill": true}

// Error is a failed call to a node. Message is what a user may be shown; the cause is for logs only.
type Error struct {
	// Status is the HTTP status the panel should answer with: the node's own status for a rejected request, 504 when the
	// node could not be reached, 502 for a reply that makes no sense.
	Status    int
	Message   string
	RequestID string
	// Unreachable is true when there was no reply at all.
	Unreachable bool

	cause error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("wings %d: %s: %v", e.Status, e.Message, e.cause)
	}
	return fmt.Sprintf("wings %d: %s", e.Status, e.Message)
}

// Unwrap returns the underlying error, which is for logs only.
func (e *Error) Unwrap() error { return e.cause }

// ClientOptions configure the connections to nodes.
type ClientOptions struct {
	// Timeout is the longest a whole request may take.
	Timeout time.Duration
	// ConnectTimeout is the longest connecting may take.
	ConnectTimeout time.Duration
	// InsecureTLS turns off the check of the node's certificate. Only for development.
	InsecureTLS bool
}

// Factory makes Clients that share connections. Create one per process.
type Factory struct {
	transport *http.Transport
	timeout   time.Duration
}

// NewFactory returns a Factory.
func NewFactory(o ClientOptions) (*Factory, error) {
	if o.Timeout <= 0 || o.ConnectTimeout <= 0 {
		return nil, errors.New("wings: the timeouts must be positive")
	}
	return &Factory{
		timeout: o.Timeout,
		transport: &http.Transport{
			// The node token travels in a header, so no proxy from the environment may get to see it.
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: o.ConnectTimeout}).DialContext,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: o.InsecureTLS}, //nolint:gosec // opt-in for development
			TLSHandshakeTimeout:   o.ConnectTimeout,
			ResponseHeaderTimeout: o.Timeout,
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
		},
	}, nil
}

// Client talks to one node.
type Client struct {
	node Node
	http *http.Client
}

// Client returns a Client for a node. The node is validated.
func (f *Factory) Client(n Node) (*Client, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}
	return &Client{
		node: n,
		http: &http.Client{
			Transport: f.transport,
			Timeout:   f.timeout,
			// A redirect would send the node token to whatever address it names.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Power sends a power signal ("start", "stop", "restart" or "kill") to a server.
func (c *Client) Power(ctx context.Context, serverUUID, action string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	if !powerActions[action] {
		return fmt.Errorf("wings: %q is not a power action", action)
	}
	return c.do(ctx, http.MethodPost, "/api/servers/"+serverUUID+"/power", map[string]string{"action": action}, nil)
}

// Command sends commands to the console of a server.
func (c *Client) Command(ctx context.Context, serverUUID string, commands ...string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	if len(commands) == 0 || len(commands) > maxCommands {
		return fmt.Errorf("wings: send between 1 and %d commands", maxCommands)
	}
	for _, cmd := range commands {
		if cmd == "" || len(cmd) > maxCommandLength || strings.ContainsRune(cmd, 0) {
			return errors.New("wings: a command is empty, too long or contains a NUL character")
		}
	}
	return c.do(ctx, http.MethodPost, "/api/servers/"+serverUUID+"/commands", map[string][]string{"commands": commands}, nil)
}

// Details is the live state of a server as the node reports it.
type Details struct {
	State       string      `json:"state"`
	IsSuspended bool        `json:"is_suspended"`
	Utilization Utilization `json:"utilization"`
}

// Utilization is the resource use of a server.
type Utilization struct {
	MemoryBytes      int64   `json:"memory_bytes"`
	MemoryLimitBytes int64   `json:"memory_limit_bytes"`
	CPUAbsolute      float64 `json:"cpu_absolute"`
	DiskBytes        int64   `json:"disk_bytes"`
	Uptime           int64   `json:"uptime"`
	Network          struct {
		RxBytes int64 `json:"rx_bytes"`
		TxBytes int64 `json:"tx_bytes"`
	} `json:"network"`
}

// Details fetches the state and resource use of a server.
func (c *Client) Details(ctx context.Context, serverUUID string) (Details, error) {
	if err := checkUUID("the server", serverUUID); err != nil {
		return Details{}, err
	}
	var d Details
	if err := c.do(ctx, http.MethodGet, "/api/servers/"+serverUUID, nil, &d); err != nil {
		return Details{}, err
	}
	return d, nil
}

// Deauthorize tells the node to end the websocket and SFTP access, and refuse the signed tokens, of a user. With server
// UUIDs it is limited to those servers. This is how a revoked user loses access that the panel cannot take back itself.
func (c *Client) Deauthorize(ctx context.Context, userUUID string, serverUUIDs ...string) error {
	if err := checkUUID("the user", userUUID); err != nil {
		return err
	}
	for _, s := range serverUUIDs {
		if err := checkUUID("a server", s); err != nil {
			return err
		}
	}
	if serverUUIDs == nil {
		serverUUIDs = []string{}
	}
	return c.do(ctx, http.MethodPost, "/api/deauthorize-user", map[string]any{"user": userUUID, "servers": serverUUIDs}, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var payload io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		payload = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.node.Address()+path, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.node.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Raptor Panel")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.http.Do(req)
	if err != nil {
		return &Error{
			Status: http.StatusGatewayTimeout, Unreachable: true, cause: err,
			Message: "Could not establish a connection to the machine running this server. Please try again.",
		}
	}
	defer res.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes+1))
	if err != nil || len(raw) > maxResponseBytes {
		return &Error{Status: http.StatusBadGateway, Message: "The machine running this server sent an unusable reply.", cause: err}
	}

	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return &Error{Status: http.StatusBadGateway, Message: "The machine running this server sent an unusable reply.", cause: err}
		}
		return nil
	}
	return replyError(res, raw)
}

// replyError turns a refused request into the messages of the reference panel's DaemonConnectionException.
func replyError(res *http.Response, raw []byte) *Error {
	requestID := res.Header.Get("X-Request-Id")
	shownID := requestID
	if shownID == "" || hasControl(shownID) || len(shownID) > 64 {
		shownID, requestID = "<nil>", ""
	}

	status := res.StatusCode
	// 1xx and 3xx cannot be an answer to these calls (redirects are not followed), and a 2xx with an error would have
	// been handled by the caller; say "bad gateway" rather than pass a misleading success status on.
	if status < 400 {
		status = http.StatusBadGateway
	}

	e := &Error{Status: status, RequestID: requestID}
	if status < 500 {
		var body struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &body) == nil && body.Error != "" {
			e.Message = fmt.Sprintf("An error occurred on the remote host: %s. (request id: %s)", cleanMessage(body.Error), shownID)
			return e
		}
	}
	e.Message = fmt.Sprintf("There was an error while communicating with the machine running this server. This error has been logged, please try again. (code: %d) (request_id: %s)", res.StatusCode, shownID)
	return e
}

// cleanMessage makes a message from a node safe to show: no control characters, a bounded length.
func cleanMessage(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxMessageLength {
		s = string([]rune(s)[:maxMessageLength])
	}
	return strings.TrimRight(s, ". ")
}
