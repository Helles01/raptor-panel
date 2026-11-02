package appnodes

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/wings"
)

func TestTheRealDaemonCarriesTheConfiguration(t *testing.T) {
	var method, path, auth, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		method, path, auth, body = r.Method, r.URL.Path, r.Header.Get("Authorization"), string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	host, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	n := wings.Node{Scheme: "http", FQDN: host, Port: p, Token: strings.Repeat("w", 64)}

	factory, err := wings.NewFactory(wings.ClientOptions{Timeout: 5 * time.Second, ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(factory)
	if err := d.UpdateConfiguration(context.Background(), n, []byte(`{"debug":false}`)); err != nil {
		t.Fatal(err)
	}
	if method != "POST" || path != "/api/update" || auth != "Bearer "+strings.Repeat("w", 64) || body != `{"debug":false}` {
		t.Errorf("the node saw %s %s %q %q", method, path, auth, body)
	}

	// A node that is set up wrongly is an error of the call.
	bad := n
	bad.Token = "short"
	if err := d.UpdateConfiguration(context.Background(), bad, []byte(`{}`)); err == nil {
		t.Error("no error for a node with a short token")
	}
}
