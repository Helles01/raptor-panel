// Package resp builds the JSON envelopes of the API, which follow the reference panel's Fractal serializer:
//
//	item:  {"object":"server","attributes":{...},"relationships":{...}}
//	list:  {"object":"list","data":[<items>],"meta":{"pagination":{...}}}
//
// The React frontend relies on these shapes, including extra meta keys such as startup_command.
package resp

import (
	"encoding/json"
	"net/url"
	"strconv"

	"github.com/Helles01/raptor-panel/internal/api/validate"
)

const (
	// DefaultPerPage and MaxPerPage bound the page size.
	DefaultPerPage = 50
	MaxPerPage     = 100
)

// Item is one resource.
type Item struct {
	Object        string         `json:"object"`
	Attributes    any            `json:"attributes"`
	Relationships map[string]any `json:"relationships,omitempty"`
}

// NewItem returns an item of the given object type.
func NewItem(object string, attributes any) Item {
	return Item{Object: object, Attributes: attributes}
}

// With returns a copy of the item with one more relationship (a nested Item or List).
func (i Item) With(name string, relationship any) Item {
	rels := make(map[string]any, len(i.Relationships)+1)
	for k, v := range i.Relationships {
		rels[k] = v
	}
	rels[name] = relationship
	i.Relationships = rels
	return i
}

// List is a collection of items.
type List struct {
	Data []Item
	Meta map[string]any
}

// NewList returns a list of items.
func NewList(items []Item) List { return List{Data: items} }

// WithMeta returns a copy of the list with one more meta key.
func (l List) WithMeta(key string, value any) List {
	meta := make(map[string]any, len(l.Meta)+1)
	for k, v := range l.Meta {
		meta[k] = v
	}
	meta[key] = value
	l.Meta = meta
	return l
}

// WithPagination returns a copy of the list with the pagination meta set.
func (l List) WithPagination(p Pagination) List { return l.WithMeta("pagination", p) }

// MarshalJSON writes the list envelope. An empty list is [], never null.
func (l List) MarshalJSON() ([]byte, error) {
	data := l.Data
	if data == nil {
		data = []Item{}
	}
	out := struct {
		Object string         `json:"object"`
		Data   []Item         `json:"data"`
		Meta   map[string]any `json:"meta,omitempty"`
	}{Object: "list", Data: data, Meta: l.Meta}
	return json.Marshal(out)
}

// Pagination is the meta.pagination block.
type Pagination struct {
	Total       int   `json:"total"`
	Count       int   `json:"count"`
	PerPage     int   `json:"per_page"`
	CurrentPage int   `json:"current_page"`
	TotalPages  int   `json:"total_pages"`
	Links       Links `json:"links"`
}

// Links holds the relative URLs of the neighbouring pages. Like the reference it is [] when there are none, otherwise
// an object with "previous" and/or "next".
type Links struct {
	Previous string
	Next     string
}

// MarshalJSON writes [] for no links and an object otherwise.
func (l Links) MarshalJSON() ([]byte, error) {
	if l.Previous == "" && l.Next == "" {
		return []byte("[]"), nil
	}
	out := map[string]string{}
	if l.Previous != "" {
		out["previous"] = l.Previous
	}
	if l.Next != "" {
		out["next"] = l.Next
	}
	return json.Marshal(out)
}

// NewPagination describes one page: total items, page size, the 1-based page and how many items it holds. u is the
// request URL; the links keep its path and query and change only page.
func NewPagination(u *url.URL, total, perPage, page, count int) Pagination {
	pages := 1
	if total > 0 && perPage > 0 {
		pages = (total + perPage - 1) / perPage
	}

	p := Pagination{Total: total, Count: count, PerPage: perPage, CurrentPage: page, TotalPages: pages}
	if page > 1 {
		p.Links.Previous = pageURL(u, page-1)
	}
	if page < pages {
		p.Links.Next = pageURL(u, page+1)
	}
	return p
}

func pageURL(u *url.URL, page int) string {
	q := u.Query()
	q.Set("page", strconv.Itoa(page))
	return u.Path + "?" + q.Encode()
}

// PageParams reads the page and per_page query parameters. Missing values default to page 1 and DefaultPerPage; a bad
// value is a 422 for that field.
func PageParams(q url.Values) (page, perPage int, err error) {
	page, perPage = 1, DefaultPerPage

	v := validate.New()
	read := func(field string, dst *int, min, max int64) {
		raw := q.Get(field)
		if raw == "" {
			return
		}
		n, convErr := strconv.ParseInt(raw, 10, 64)
		if convErr != nil {
			v.IntRange(field, min-1, min, max) // reports the standard "between" message
			return
		}
		v.IntRange(field, n, min, max)
		if v.OK() {
			*dst = int(n)
		}
	}
	read("page", &page, 1, 1<<31-1)
	read("per_page", &perPage, 1, MaxPerPage)

	if verr := v.Err(); verr != nil {
		return 1, DefaultPerPage, verr
	}
	return page, perPage, nil
}
