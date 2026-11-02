package controlapi

import (
	"sync"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/Helles01/raptor-panel/internal/api/resp"
	"github.com/Helles01/raptor-panel/internal/wings"
)

type resourceUse struct {
	MemoryBytes    int64   `json:"memory_bytes"`
	CPUAbsolute    float64 `json:"cpu_absolute"`
	DiskBytes      int64   `json:"disk_bytes"`
	NetworkRxBytes int64   `json:"network_rx_bytes"`
	NetworkTxBytes int64   `json:"network_tx_bytes"`
	Uptime         int64   `json:"uptime"`
}

type statsAttributes struct {
	CurrentState string      `json:"current_state"`
	IsSuspended  bool        `json:"is_suspended"`
	Resources    resourceUse `json:"resources"`
}

// Resources is GET /api/client/servers/{server}/resources: the state and resource use of the server as its node reports them. The
// node is asked at most once in resourcesTTL for a server, whoever asks.
func (h *Handlers) Resources(c echo.Context) error {
	p, err := caller(c)
	if err != nil {
		return err
	}
	d, found := h.cache.get(p.Server.UUID)
	if !found {
		t, err := h.target(c, p)
		if err != nil {
			return err
		}
		if d, err = h.d.Daemon.Details(c.Request().Context(), t.Node, t.UUID); err != nil {
			return DaemonError(err)
		}
		h.cache.put(p.Server.UUID, d)
	}

	state := d.State
	if state == "" {
		state = "stopped"
	}
	u := d.Utilization
	return ok(c, resp.NewItem("stats", statsAttributes{
		CurrentState: state,
		IsSuspended:  d.IsSuspended,
		Resources: resourceUse{
			MemoryBytes: u.MemoryBytes, CPUAbsolute: u.CPUAbsolute, DiskBytes: u.DiskBytes,
			NetworkRxBytes: u.Network.RxBytes, NetworkTxBytes: u.Network.TxBytes, Uptime: u.Uptime,
		},
	}))
}

// maxCachedServers bounds the cache. Entries that have expired go first; when it is still full, nothing more is kept and the node is
// asked each time, which is slower but right.
const maxCachedServers = 10_000

type cachedDetails struct {
	details wings.Details
	expires time.Time
}

// detailsCache keeps what a node said about a server for a short while. Only answers are kept, never errors.
type detailsCache struct {
	ttl time.Duration
	now func() time.Time

	mu    sync.Mutex
	items map[string]cachedDetails
}

func newDetailsCache(ttl time.Duration, now func() time.Time) *detailsCache {
	return &detailsCache{ttl: ttl, now: now, items: map[string]cachedDetails{}}
}

func (c *detailsCache) get(serverUUID string) (wings.Details, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[serverUUID]
	if !ok || !c.now().Before(e.expires) {
		delete(c.items, serverUUID)
		return wings.Details{}, false
	}
	return e.details, true
}

func (c *detailsCache) put(serverUUID string, d wings.Details) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if _, known := c.items[serverUUID]; !known && len(c.items) >= maxCachedServers {
		for k, e := range c.items {
			if !now.Before(e.expires) {
				delete(c.items, k)
			}
		}
		if len(c.items) >= maxCachedServers {
			return
		}
	}
	c.items[serverUUID] = cachedDetails{details: d, expires: now.Add(c.ttl)}
}
