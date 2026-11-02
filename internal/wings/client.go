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

// MaxCommandLength is the longest command, in bytes, that Command sends.
const MaxCommandLength = 4096

const (
	maxResponseBytes = 1 << 20
	maxCommands      = 100
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
	// NodeStatus is the status the node answered with, 0 when it did not answer. Status can differ from it when the panel makes up
	// its own (a reply it cannot use), which a caller that acts on what the node said, such as "502: the server is offline", must
	// be able to tell apart.
	NodeStatus int

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
	// LongTimeout is the longest a call that is slow by nature may take: packing and unpacking archives. 15 minutes when zero.
	LongTimeout time.Duration
	// InsecureTLS turns off the check of the node's certificate. Only for development.
	InsecureTLS bool
}

// Factory makes Clients that share connections. Create one per process.
type Factory struct {
	transport   *http.Transport
	longTransp  *http.Transport
	timeout     time.Duration
	longTimeout time.Duration
}

// NewFactory returns a Factory.
func NewFactory(o ClientOptions) (*Factory, error) {
	if o.Timeout <= 0 || o.ConnectTimeout <= 0 {
		return nil, errors.New("wings: the timeouts must be positive")
	}
	if o.LongTimeout <= 0 {
		o.LongTimeout = 15 * time.Minute
	}
	transport := func(headerTimeout time.Duration) *http.Transport {
		return &http.Transport{
			// The node token travels in a header, so no proxy from the environment may get to see it.
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: o.ConnectTimeout}).DialContext,
			TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: o.InsecureTLS}, //nolint:gosec // opt-in for development
			TLSHandshakeTimeout:   o.ConnectTimeout,
			ResponseHeaderTimeout: headerTimeout,
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
		}
	}
	return &Factory{
		timeout: o.Timeout, longTimeout: o.LongTimeout,
		transport: transport(o.Timeout), longTransp: transport(o.LongTimeout),
	}, nil
}

// Client talks to one node.
type Client struct {
	node Node
	http *http.Client
	// long is for the calls that may take as long as LongTimeout.
	long *http.Client
}

// Client returns a Client for a node. The node is validated.
func (f *Factory) Client(n Node) (*Client, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}
	return &Client{
		node: n,
		http: noRedirects(f.transport, f.timeout),
		long: noRedirects(f.longTransp, f.longTimeout),
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
		if cmd == "" || len(cmd) > MaxCommandLength || strings.ContainsRune(cmd, 0) {
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

func noRedirects(t http.RoundTripper, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: t,
		Timeout:   timeout,
		// A redirect would send the node token to whatever address it names.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// do makes a call with a JSON body, if there is one, and decodes a JSON reply into out, if there is one.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	k := call{method: method, path: path, out: out}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		k.body, k.contentType = b, "application/json"
	}
	return c.run(ctx, k)
}

// call describes one request to a node.
type call struct {
	method, path string
	// body is sent as it is, with contentType.
	body        []byte
	contentType string
	// out takes the JSON of the reply; raw keeps the reply as it is. With neither the reply is dropped.
	out any
	raw *[]byte
	// limit is the largest reply taken, maxResponseBytes when zero; a larger one is ErrTooLarge for a raw reply.
	limit int64
	// long lets the call take as long as LongTimeout.
	long bool
}

// ErrTooLarge is a reply that is larger than the caller allowed.
var ErrTooLarge = errors.New("wings: the reply is larger than allowed")

func (c *Client) run(ctx context.Context, k call) error {
	var payload io.Reader
	if k.body != nil {
		payload = bytes.NewReader(k.body)
	}
	req, err := http.NewRequestWithContext(ctx, k.method, c.node.Address()+k.path, payload)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.node.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Raptor Panel")
	if k.body != nil && k.contentType != "" {
		req.Header.Set("Content-Type", k.contentType)
	}

	client := c.http
	if k.long {
		client = c.long
	}
	res, err := client.Do(req)
	if err != nil {
		return &Error{
			Status: http.StatusGatewayTimeout, Unreachable: true, cause: err,
			Message: "Could not establish a connection to the machine running this server. Please try again.",
		}
	}
	defer res.Body.Close()

	limit := k.limit
	if limit <= 0 {
		limit = maxResponseBytes
	}
	if k.raw != nil && res.StatusCode >= 200 && res.StatusCode < 300 && res.ContentLength > limit {
		return ErrTooLarge
	}
	// A refusal is read in full up to the usual limit, whatever the call allows for its good reply.
	readLimit := limit
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		readLimit = maxResponseBytes
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, readLimit+1))
	if err != nil || int64(len(raw)) > readLimit {
		if err == nil && k.raw != nil && res.StatusCode >= 200 && res.StatusCode < 300 {
			return ErrTooLarge
		}
		return &Error{Status: http.StatusBadGateway, NodeStatus: res.StatusCode, Message: "The machine running this server sent an unusable reply.", cause: err}
	}

	if res.StatusCode >= 200 && res.StatusCode < 300 {
		switch {
		case k.raw != nil:
			*k.raw = raw
		case k.out != nil:
			if err := json.Unmarshal(raw, k.out); err != nil {
				return &Error{Status: http.StatusBadGateway, NodeStatus: res.StatusCode, Message: "The machine running this server sent an unusable reply.", cause: err}
			}
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

	e := &Error{Status: status, NodeStatus: res.StatusCode, RequestID: requestID}
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

// Delete tells the node to remove a server and its files. The panel does it to the old node once a transfer has ended.
func (c *Client) Delete(ctx context.Context, serverUUID string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/api/servers/"+serverUUID, nil, nil)
}

// Reinstall tells the node to run the install script of a server again.
func (c *Client) Reinstall(ctx context.Context, serverUUID string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/servers/"+serverUUID+"/reinstall", nil, nil)
}

// CreateBackup tells the node to make a backup of a server. adapter is the disk it is stored on, ignore the files to leave out, one
// pattern per line. The node answers at once and reports the end of the backup to the panel on its own.
func (c *Client) CreateBackup(ctx context.Context, serverUUID, adapter, backupUUID, ignore string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	if err := checkUUID("the backup", backupUUID); err != nil {
		return err
	}
	if adapter != "wings" && adapter != "s3" {
		return fmt.Errorf("wings: %q is not a backup disk", adapter)
	}
	return c.do(ctx, http.MethodPost, "/api/servers/"+serverUUID+"/backup", map[string]string{"adapter": adapter, "uuid": backupUUID, "ignore": ignore}, nil)
}

// RestoreBackup tells the node to unpack a backup over the files of a server, or over an emptied directory with truncate.
// downloadURL is where the node fetches the archive from when it is not on the node itself.
func (c *Client) RestoreBackup(ctx context.Context, serverUUID, backupUUID, adapter string, truncate bool, downloadURL string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	if err := checkUUID("the backup", backupUUID); err != nil {
		return err
	}
	if adapter != "wings" && adapter != "s3" {
		return fmt.Errorf("wings: %q is not a backup disk", adapter)
	}
	return c.do(ctx, http.MethodPost, "/api/servers/"+serverUUID+"/backup/"+backupUUID+"/restore",
		map[string]any{"adapter": adapter, "truncate_directory": truncate, "download_url": downloadURL}, nil)
}

// DeleteBackup tells the node to delete the archive of a backup. A node that does not know the backup answers 404 (*Error with
// NodeStatus 404), which a caller that is deleting it anyway may take as done.
func (c *Client) DeleteBackup(ctx context.Context, serverUUID, backupUUID string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	if err := checkUUID("the backup", backupUUID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodDelete, "/api/servers/"+serverUUID+"/backup/"+backupUUID, nil, nil)
}

func jsonBody(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	return b, nil
}

// UpdateConfiguration sends a running node its new configuration (as JSON, in the form of nodes.WingsConfigJSON), which Wings applies
// to what it can without a restart.
func (c *Client) UpdateConfiguration(ctx context.Context, config []byte) error {
	if !json.Valid(config) {
		return errors.New("wings: the configuration is not JSON")
	}
	return c.run(ctx, call{method: http.MethodPost, path: "/api/update", body: config, contentType: "application/json"})
}

// CreateServer asks a node to set a server up from the configuration the panel serves it (GET /api/remote/servers/{uuid}). The node
// installs the server and, with startOnCompletion, starts it when the installation is done.
func (c *Client) CreateServer(ctx context.Context, serverUUID string, startOnCompletion bool) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/servers", map[string]any{"uuid": serverUUID, "start_on_completion": startOnCompletion}, nil)
}

// Sync tells a node to fetch the configuration of a server again and apply what it can without a restart.
func (c *Client) Sync(ctx context.Context, serverUUID string) error {
	if err := checkUUID("the server", serverUUID); err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/api/servers/"+serverUUID+"/sync", nil, nil)
}
