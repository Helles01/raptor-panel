package resp

import (
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
)

func toMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s is not a JSON object: %v", raw, err)
	}
	return out
}

func TestItem(t *testing.T) {
	item := NewItem("server", map[string]any{"name": "Survival"})

	got := toMap(t, item)
	if !reflect.DeepEqual(got, map[string]any{"object": "server", "attributes": map[string]any{"name": "Survival"}}) {
		t.Errorf("item = %v (relationships must be omitted when there are none)", got)
	}

	nested := item.With("allocations", NewList([]Item{NewItem("allocation", map[string]any{"port": 25565})}))
	got = toMap(t, nested)
	rel := got["relationships"].(map[string]any)["allocations"].(map[string]any)
	if rel["object"] != "list" || len(rel["data"].([]any)) != 1 {
		t.Errorf("relationship = %v", rel)
	}

	if item.Relationships != nil {
		t.Error("With must not modify the original item")
	}
}

func TestListShape(t *testing.T) {
	list := NewList([]Item{NewItem("user", map[string]any{"id": 1}), NewItem("user", map[string]any{"id": 2})})

	got := toMap(t, list)

	if got["object"] != "list" {
		t.Errorf("object = %v", got["object"])
	}
	data := got["data"].([]any)
	if len(data) != 2 || data[0].(map[string]any)["object"] != "user" {
		t.Errorf("data = %v", data)
	}
	if _, has := got["meta"]; has {
		t.Error("meta must be omitted when empty")
	}
}

func TestEmptyListIsAnEmptyArray(t *testing.T) {
	for name, list := range map[string]List{"nil": NewList(nil), "empty": NewList([]Item{})} {
		raw, err := json.Marshal(list)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != `{"object":"list","data":[]}` {
			t.Errorf("%s list = %s", name, raw)
		}
	}
}

func TestListMetaCarriesExtraKeys(t *testing.T) {
	list := NewList(nil).WithMeta("startup_command", "java -jar server.jar").WithMeta("docker_images", map[string]string{"Java 21": "img"})

	meta := toMap(t, list)["meta"].(map[string]any)

	if meta["startup_command"] != "java -jar server.jar" || meta["docker_images"].(map[string]any)["Java 21"] != "img" {
		t.Errorf("meta = %v", meta)
	}
}

func TestPaginationBlock(t *testing.T) {
	u, _ := url.Parse("/api/application/users?filter=ada&page=2")

	tests := []struct {
		name                      string
		total, perPage, page, n   int
		wantPages                 int
		wantPrevious, wantNext    string
		wantLinksAsEmptyJSONArray bool
	}{
		{"middle page", 250, 50, 2, 50, 5, "/api/application/users?filter=ada&page=1", "/api/application/users?filter=ada&page=3", false},
		{"first page", 250, 50, 1, 50, 5, "", "/api/application/users?filter=ada&page=2", false},
		{"last page", 250, 50, 5, 50, 5, "/api/application/users?filter=ada&page=4", "", false},
		{"partial last page", 101, 50, 3, 1, 3, "/api/application/users?filter=ada&page=2", "", false},
		{"single page has no links", 10, 50, 1, 10, 1, "", "", true},
		{"no items at all", 0, 50, 1, 0, 1, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPagination(u, tt.total, tt.perPage, tt.page, tt.n)

			if p.TotalPages != tt.wantPages || p.Total != tt.total || p.PerPage != tt.perPage || p.CurrentPage != tt.page || p.Count != tt.n {
				t.Errorf("pagination = %+v", p)
			}
			if p.Links.Previous != tt.wantPrevious || p.Links.Next != tt.wantNext {
				t.Errorf("links = %+v, want previous %q next %q", p.Links, tt.wantPrevious, tt.wantNext)
			}

			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantLinksAsEmptyJSONArray != strings.Contains(string(raw), `"links":[]`) {
				t.Errorf("links JSON in %s", raw)
			}
		})
	}
}

func TestPaginationJSONKeys(t *testing.T) {
	u, _ := url.Parse("/x")
	raw, _ := json.Marshal(NewList(nil).WithPagination(NewPagination(u, 3, 50, 1, 3)))

	want := `{"object":"list","data":[],"meta":{"pagination":{"total":3,"count":3,"per_page":50,"current_page":1,"total_pages":1,"links":[]}}}`
	if string(raw) != want {
		t.Errorf("got  %s\nwant %s", raw, want)
	}
}

func TestPaginationLinksOrderAndPagePreservation(t *testing.T) {
	u, _ := url.Parse("/x?per_page=10&page=2")
	raw, _ := json.Marshal(NewPagination(u, 30, 10, 2, 10).Links)

	// Compare the decoded values: the JSON encoder writes "&" as an escape sequence, which is equivalent.
	var got map[string]string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"next": "/x?page=3&per_page=10", "previous": "/x?page=1&per_page=10"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("links = %v, want %v (other query parameters must be kept, only page changes)", got, want)
	}
}

func TestPageParams(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		wantPage    int
		wantPerPage int
		wantField   string
	}{
		{"defaults", "", 1, 50, ""},
		{"explicit", "page=3&per_page=25", 3, 25, ""},
		{"max per page", "per_page=100", 1, 100, ""},
		{"per page too large", "per_page=101", 0, 0, "per_page"},
		{"per page zero", "per_page=0", 0, 0, "per_page"},
		{"per page negative", "per_page=-5", 0, 0, "per_page"},
		{"page zero", "page=0", 0, 0, "page"},
		{"page not a number", "page=abc", 0, 0, "page"},
		{"page with decimals", "page=1.5", 0, 0, "page"},
		{"page overflow", "page=99999999999999999999", 0, 0, "page"},
		{"empty values use the defaults", "page=&per_page=", 1, 50, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, _ := url.ParseQuery(tt.query)

			page, perPage, err := PageParams(q)

			if tt.wantField == "" {
				if err != nil || page != tt.wantPage || perPage != tt.wantPerPage {
					t.Errorf("got page %d per_page %d err %v; want %d/%d", page, perPage, err, tt.wantPage, tt.wantPerPage)
				}
				return
			}
			var ae *apierr.Error
			if !errors.As(err, &ae) || ae.Status != 422 || len(ae.Fields) != 1 || ae.Fields[0].Field != tt.wantField {
				t.Errorf("error = %v, want a 422 for %q", err, tt.wantField)
			}
			if page != 1 || perPage != 50 {
				t.Errorf("on error the defaults must be returned, got %d/%d", page, perPage)
			}
		})
	}
}
