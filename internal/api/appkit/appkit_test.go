package appkit

import (
	"encoding/json"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/listquery"
	"github.com/Helles01/raptor-panel/internal/auth"
	"github.com/Helles01/raptor-panel/internal/fielderr"
)

func TestTimeIsAlwaysUTC(t *testing.T) {
	zone := time.FixedZone("CEST", 2*3600)
	got := Time(time.Date(2026, 10, 1, 12, 30, 0, 0, zone))
	if got != "2026-10-01T10:30:00+00:00" {
		t.Errorf("Time = %q", got)
	}
}

func TestResourceURL(t *testing.T) {
	for _, base := range []string{"https://panel.example.com", "https://panel.example.com/", "https://panel.example.com//"} {
		if got := ResourceURL(base, "/api/application/users/3"); got != "https://panel.example.com/api/application/users/3" {
			t.Errorf("%q: %q", base, got)
		}
	}
}

func TestStoreError(t *testing.T) {
	var e *apierr.Error
	if err := StoreError(ErrNotFound); !errors.As(err, &e) || e.Status != 404 {
		t.Errorf("not found: %v", err)
	}
	if err := StoreError(&TakenError{Field: "external_id"}); !errors.As(err, &e) || e.Status != 422 {
		t.Errorf("taken: %v", err)
	}
	if err := StoreError(errors.New("boom password=hunter2")); !errors.As(err, &e) || e.Status != 500 {
		t.Errorf("other: %v", err)
	}
	if Taken(errors.New("x")) != nil || Taken(nil) != nil {
		t.Error("an error that is not a taken value was taken for one")
	}
}

func TestFieldError(t *testing.T) {
	var e *apierr.Error
	if err := FieldError(fielderr.New("username", "must be short")); !errors.As(err, &e) || e.Status != 422 {
		t.Errorf("field: %v", err)
	}
	if err := FieldError(errors.New("hash failed")); !errors.As(err, &e) || e.Status != 500 {
		t.Errorf("other: %v", err)
	}
}

func TestViewReadsTheLevelsOfTheKey(t *testing.T) {
	p := authz.Principal{Key: &authz.Key{Access: map[string]auth.Access{"nodes": auth.AccessRead, "users": auth.AccessReadWrite, "eggs": auth.AccessNone}}}
	v := NewView(p)
	for resource, want := range map[string]bool{"nodes": true, "users": true, "eggs": false, "nests": false} {
		if v.CanRead(resource) != want {
			t.Errorf("CanRead(%s) = %v", resource, !want)
		}
	}
	if NewView(authz.Principal{}).CanRead("nodes") {
		t.Error("a request without a key reads")
	}
}

func TestIncludes(t *testing.T) {
	q := url.Values{"include": {"allocations,location,servers,nothing,eggs"}}
	got := Wanted(q, listquery.Nodes)
	// Only what the resource offers: an unknown name is left out, and so is what Nodes does not offer.
	if len(got) != 3 || !got["allocations"] || !got["location"] || !got["servers"] {
		t.Errorf("Wanted = %v", got)
	}
	p := authz.Principal{Key: &authz.Key{Access: map[string]auth.Access{"allocations": auth.AccessRead}}}
	load := got.Load(NewView(p), listquery.Nodes)
	if len(load) != 1 || !load["allocations"] {
		t.Errorf("Load = %v", load)
	}
	// An egg's own configuration and script need no right.
	eggs := Wanted(url.Values{"include": {"config,script,variables,nest"}}, listquery.Eggs)
	if l := eggs.Load(NewView(authz.Principal{Key: &authz.Key{}}), listquery.Eggs); len(l) != 2 || !l["config"] || !l["script"] {
		t.Errorf("egg Load = %v", l)
	}
}

func TestDockerImagesKeepTheirOrder(t *testing.T) {
	out, err := json.Marshal(images{{"Z last name", "img:z"}, {"A first name", "img:a"}, {`Quote"d`, "i"}})
	if err != nil || string(out) != `{"Z last name":"img:z","A first name":"img:a","Quote\"d":"i"}` {
		t.Errorf("images = %s %v", out, err)
	}
	if out, _ := json.Marshal(images(nil)); string(out) != "{}" {
		t.Errorf("no images = %s", out)
	}
}

func TestJSONHelpers(t *testing.T) {
	for in, want := range map[string]string{"": "{}", "null": "{}", "[]": "{}", "{}": "{}", ` {"a":1} `: `{"a":1}`, "[1]": "[1]"} {
		if got := string(emptyFiles(json.RawMessage(in))); got != want {
			t.Errorf("emptyFiles(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"": "null", "null": "null", `{"a":1}`: `{"a":1}`, "[]": "[]"} {
		if got := string(jsonOr(json.RawMessage(in), "null")); got != want {
			t.Errorf("jsonOr(%q) = %q, want %q", in, got, want)
		}
	}
}
