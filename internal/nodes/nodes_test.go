package nodes

import (
	"bytes"
	"crypto/rand"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func validNode() NewNode {
	n := DefaultNewNode()
	n.Name = "Node 01"
	n.LocationID = 1
	n.FQDN = "node1.example.com"
	n.Memory = 16384
	n.Disk = 512000
	return n
}

func TestNewNodeValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*NewNode)
		wantErr string
	}{
		{"valid defaults plus required fields", func(*NewNode) {}, ""},
		{"ip address host", func(n *NewNode) { n.FQDN = "203.0.113.7" }, ""},
		{"ipv6 host", func(n *NewNode) { n.FQDN = "2001:db8::1" }, ""},
		{"http scheme", func(n *NewNode) { n.Scheme = "http" }, ""},
		{"unlimited overallocation", func(n *NewNode) { n.MemoryOverallocate, n.DiskOverallocate = -1, -1 }, ""},
		{"daemon base with dots and dashes", func(n *NewNode) { n.DaemonBase = "/srv/pterodactyl-1.0/volumes" }, ""},

		{"empty name", func(n *NewNode) { n.Name = "" }, "name"},
		{"name with slash", func(n *NewNode) { n.Name = "node/1" }, "name"},
		{"name too long", func(n *NewNode) { n.Name = strings.Repeat("a", 101) }, "name"},
		{"name with newline", func(n *NewNode) { n.Name = "node\n1" }, "name"},
		{"description with NUL", func(n *NewNode) { n.Description = "a\x00b" }, "description"},
		{"no location", func(n *NewNode) { n.LocationID = 0 }, "location"},
		{"empty fqdn", func(n *NewNode) { n.FQDN = "" }, "fqdn"},
		{"fqdn with scheme", func(n *NewNode) { n.FQDN = "https://node1.example.com" }, "fqdn"},
		{"fqdn with port", func(n *NewNode) { n.FQDN = "node1.example.com:8080" }, "fqdn"},
		{"fqdn with space", func(n *NewNode) { n.FQDN = "node 1.example.com" }, "fqdn"},
		{"fqdn with underscore", func(n *NewNode) { n.FQDN = "node_1.example.com" }, "fqdn"},
		{"fqdn label starting with a dash", func(n *NewNode) { n.FQDN = "-node.example.com" }, "fqdn"},
		{"fqdn too long", func(n *NewNode) { n.FQDN = strings.Repeat("a.", 130) + "com" }, "fqdn"},
		{"unknown scheme", func(n *NewNode) { n.Scheme = "ftp" }, "scheme"},
		{"empty scheme", func(n *NewNode) { n.Scheme = "" }, "scheme"},
		{"zero memory", func(n *NewNode) { n.Memory = 0 }, "memory must"},
		{"memory overallocation below -1", func(n *NewNode) { n.MemoryOverallocate = -2 }, "memory overallocation"},
		{"zero disk", func(n *NewNode) { n.Disk = 0 }, "disk must"},
		{"disk overallocation below -1", func(n *NewNode) { n.DiskOverallocate = -5 }, "disk overallocation"},
		{"relative daemon base", func(n *NewNode) { n.DaemonBase = "var/lib/volumes" }, "daemon base"},
		{"daemon base with parent segment", func(n *NewNode) { n.DaemonBase = "/var/lib/../../etc" }, "daemon base"},
		{"daemon base with space", func(n *NewNode) { n.DaemonBase = "/var/lib/my volumes" }, "daemon base"},
		{"daemon base is only a slash", func(n *NewNode) { n.DaemonBase = "/" }, "daemon base"},
		{"sftp port zero", func(n *NewNode) { n.DaemonSFTP = 0 }, "SFTP port"},
		{"daemon port too large", func(n *NewNode) { n.DaemonListen = 65536 }, "daemon port"},
		{"zero upload size", func(n *NewNode) { n.UploadSize = 0 }, "upload size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := validNode()
			tt.mutate(&n)

			err := n.Validate()

			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultNewNodeMatchesTheReference(t *testing.T) {
	d := DefaultNewNode()
	if d.Scheme != "https" || !d.Public || d.DaemonBase != "/var/lib/pterodactyl/volumes" || d.DaemonSFTP != 2022 || d.DaemonListen != 8080 || d.UploadSize != 100 {
		t.Errorf("defaults = %+v", d)
	}
}

func TestValidateLocation(t *testing.T) {
	tests := []struct {
		name        string
		short, long string
		wantErr     bool
	}{
		{"short only", "eu-west", "", false},
		{"both", "us", "United States, East", false},
		{"unicode", "Győr", "Magyarország", false},
		{"empty short", "", "x", true},
		{"short too long", strings.Repeat("a", 61), "", true},
		{"short at the limit", strings.Repeat("a", 60), "", false},
		{"long too long", "x", strings.Repeat("a", 192), true},
		{"control character", "us\x07", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateLocation(tt.short, tt.long); (err != nil) != tt.wantErr {
				t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNewCredentials(t *testing.T) {
	alnum := regexp.MustCompile(`^[A-Za-z0-9]+$`)

	c, err := NewCredentials(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TokenID) != 16 || len(c.Token) != 64 || !alnum.MatchString(c.TokenID) || !alnum.MatchString(c.Token) {
		t.Errorf("credentials = %+v", c)
	}

	again, _ := NewCredentials(rand.Reader)
	if again == c {
		t.Error("two credential pairs are equal")
	}

	if _, err := NewCredentials(bytes.NewReader([]byte{1, 2, 3})); err == nil {
		t.Error("a failing random source must be an error")
	}
}

func wingsNode() WingsNode {
	return WingsNode{
		UUID: "9f4d2c10-0000-4000-8000-000000000001", FQDN: "Node1.Example.com", Scheme: "https",
		DaemonBase: "/var/lib/pterodactyl/volumes", DaemonSFTP: 2022, DaemonListen: 8080, UploadSize: 100,
	}
}

func TestWingsConfig(t *testing.T) {
	creds := Credentials{TokenID: "AbCdEfGhIjKlMnOp", Token: strings.Repeat("t", 64)}

	t.Run("matches the reference structure", func(t *testing.T) {
		out, err := WingsConfig(wingsNode(), creds, "https://panel.example.com")
		if err != nil {
			t.Fatal(err)
		}

		var got map[string]any
		if err := yaml.Unmarshal(out, &got); err != nil {
			t.Fatalf("output is not YAML: %v", err)
		}
		want := map[string]any{
			"debug":    false,
			"uuid":     "9f4d2c10-0000-4000-8000-000000000001",
			"token_id": "AbCdEfGhIjKlMnOp",
			"token":    strings.Repeat("t", 64),
			"api": map[string]any{
				"host": "0.0.0.0",
				"port": 8080,
				"ssl": map[string]any{
					"enabled": true,
					"cert":    "/etc/letsencrypt/live/node1.example.com/fullchain.pem",
					"key":     "/etc/letsencrypt/live/node1.example.com/privkey.pem",
				},
				"upload_limit": 100,
			},
			"system": map[string]any{
				"data": "/var/lib/pterodactyl/volumes",
				"sftp": map[string]any{"bind_port": 2022},
			},
			"allowed_mounts": []any{},
			"remote":         "https://panel.example.com",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("config = %#v\nwant   %#v", got, want)
		}
		if !strings.Contains(string(out), "allowed_mounts: []") {
			t.Errorf("an empty mount list must be written as []:\n%s", out)
		}
	})

	t.Run("ssl is off behind a proxy or on http", func(t *testing.T) {
		for name, mutate := range map[string]func(*WingsNode){
			"behind proxy": func(n *WingsNode) { n.BehindProxy = true },
			"http":         func(n *WingsNode) { n.Scheme = "http" },
		} {
			n := wingsNode()
			mutate(&n)
			out, err := WingsConfig(n, creds, "https://panel.example.com")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), "enabled: false") {
				t.Errorf("%s: ssl must be disabled:\n%s", name, out)
			}
		}
	})

	t.Run("mounts and a trailing slash on the remote", func(t *testing.T) {
		n := wingsNode()
		n.AllowedMounts = []string{"/mnt/a", "/mnt/b"}
		out, err := WingsConfig(n, creds, "https://panel.example.com/")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "- /mnt/a") || !strings.Contains(string(out), "remote: https://panel.example.com\n") {
			t.Errorf("unexpected output:\n%s", out)
		}
	})

	t.Run("rejects a bad remote or missing credentials", func(t *testing.T) {
		for _, remote := range []string{"", "panel.example.com", "ftp://panel.example.com", "https://user:pw@panel.example.com", "https://"} {
			if _, err := WingsConfig(wingsNode(), creds, remote); err == nil {
				t.Errorf("remote %q accepted", remote)
			}
		}
		if _, err := WingsConfig(wingsNode(), Credentials{}, "https://panel.example.com"); err == nil {
			t.Error("missing credentials accepted")
		}
	})
}
