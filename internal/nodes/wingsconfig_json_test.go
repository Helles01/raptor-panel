package nodes

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The two forms are one configuration: what Wings is sent while it runs is what config.yml holds.
func TestWingsConfigJSONIsTheSameConfiguration(t *testing.T) {
	creds := Credentials{TokenID: "AbCdEfGhIjKlMnOp", Token: strings.Repeat("t", 64)}
	n := wingsNode()
	n.BehindProxy = true
	n.AllowedMounts = []string{"/mnt/a"}

	asYAML, err := WingsConfig(n, creds, "https://panel.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	asJSON, err := WingsConfigJSON(n, creds, "https://panel.example.com/")
	if err != nil {
		t.Fatal(err)
	}
	var fromYAML, fromJSON map[string]any
	if err := yaml.Unmarshal(asYAML, &fromYAML); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(asJSON, &fromJSON); err != nil {
		t.Fatalf("not JSON: %v: %s", err, asJSON)
	}
	// JSON numbers are floats and YAML's are ints; compare the texts they give.
	norm := func(m map[string]any) string { b, _ := json.Marshal(m); return string(b) }
	if norm(fromYAML) != norm(fromJSON) {
		t.Errorf("YAML %s, JSON %s", norm(fromYAML), norm(fromJSON))
	}
	if !reflect.DeepEqual(fromJSON["allowed_mounts"], []any{"/mnt/a"}) || fromJSON["remote"] != "https://panel.example.com" || fromJSON["token"] != strings.Repeat("t", 64) {
		t.Errorf("config = %v", fromJSON)
	}

	// Nothing to mount is an empty list, which Wings reads, and not null.
	n.AllowedMounts = nil
	empty, err := WingsConfigJSON(n, creds, "https://panel.example.com")
	if err != nil || !strings.Contains(string(empty), `"allowed_mounts":[]`) {
		t.Errorf("empty mounts: %s %v", empty, err)
	}

	for _, remote := range []string{"", "ftp://panel", "https://u:p@panel.example.com", "panel.example.com"} {
		if _, err := WingsConfigJSON(wingsNode(), creds, remote); err == nil {
			t.Errorf("remote %q accepted", remote)
		}
	}
	if _, err := WingsConfigJSON(wingsNode(), Credentials{}, "https://panel.example.com"); err == nil {
		t.Error("missing credentials accepted")
	}
}
