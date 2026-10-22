// Package activity turns the events a node reports into the entries the panel stores. It touches no database: the handler looks
// up the servers (of the calling node only) and the users and stores what Normalize returns.
package activity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

const (
	// MaxBatch is the most events one request may hold. The node sends 100 at a time.
	MaxBatch         = 1000
	MaxEventLength   = 191
	MaxMetadataBytes = 16384
	// MaxFutureSkew is how far ahead of the panel's clock a time may be before it is not believed.
	MaxFutureSkew = 10 * time.Minute

	defaultIP         = "127.0.0.1"
	maxOriginalLength = 64
)

// ErrTooMany is returned for a request with more than MaxBatch events.
var ErrTooMany = fmt.Errorf("a request may hold at most %d events", MaxBatch)

var (
	uuidPattern  = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	eventPattern = regexp.MustCompile(`^server:[a-z0-9][a-z0-9_.:-]*$`)
)

// RawEvent is one event as the node sends it, in the body {"data": [...]}.
type RawEvent struct {
	User      string          `json:"user"`
	Server    string          `json:"server"`
	Event     string          `json:"event"`
	Metadata  json.RawMessage `json:"metadata"`
	IP        string          `json:"ip"`
	Timestamp string          `json:"timestamp"`
}

// Entry is an event ready to store. The UUIDs are lower case, as stored.
type Entry struct {
	ServerUUID string
	// UserUUID is empty for an event with no user.
	UserUUID string
	Event    string
	IP       string
	// Properties is a JSON object.
	Properties string
	When       time.Time
}

// Result is what a request turned into.
type Result struct {
	Entries []Entry
	// Skipped counts the events that were left out because they were not usable.
	Skipped int
}

// Normalize checks and cleans the events of one request. An event that cannot be used is skipped, not the whole request: the
// node keeps sending an unconfirmed batch from its oldest event on, so one bad event answered with an error would hold back every
// later one for good (C-034). The caller should log how many were skipped.
//
// Only events whose name starts with "server:" are taken, so a node cannot write the events of sign-ins or of administration.
// A time that cannot be read, or that is too far ahead, is replaced by now, with the original kept in the properties.
func Normalize(events []RawEvent, now time.Time) (Result, error) {
	if len(events) > MaxBatch {
		return Result{}, ErrTooMany
	}

	res := Result{Entries: make([]Entry, 0, len(events))}
	for _, e := range events {
		entry, ok := normalizeEvent(e, now)
		if !ok {
			res.Skipped++
			continue
		}
		res.Entries = append(res.Entries, entry)
	}
	return res, nil
}

func normalizeEvent(e RawEvent, now time.Time) (Entry, bool) {
	if !uuidPattern.MatchString(e.Server) || len(e.Event) > MaxEventLength || !eventPattern.MatchString(e.Event) {
		return Entry{}, false
	}
	if e.User != "" && !uuidPattern.MatchString(e.User) {
		return Entry{}, false
	}
	ip, ok := normalizeIP(e.IP)
	if !ok {
		return Entry{}, false
	}

	props := metadata(e.Metadata)
	when, err := time.Parse(time.RFC3339, e.Timestamp)
	if err != nil || when.After(now.Add(MaxFutureSkew)) {
		when = now
		props["original_timestamp"] = cut(e.Timestamp, maxOriginalLength)
	}

	raw, err := json.Marshal(props)
	if err != nil {
		return Entry{}, false
	}
	return Entry{
		ServerUUID: strings.ToLower(e.Server),
		UserUUID:   strings.ToLower(e.User),
		Event:      e.Event,
		IP:         ip,
		Properties: string(raw),
		When:       when.UTC(),
	}, true
}

// normalizeIP takes an address, with or without a port, and writes it in its short form. No address means the loopback one,
// as in the reference.
func normalizeIP(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultIP, true
	}
	if ap, err := netip.ParseAddrPort(value); err == nil {
		return ap.Addr().Unmap().WithZone("").String(), true
	}
	a, err := netip.ParseAddr(value)
	if err != nil {
		return "", false
	}
	return a.Unmap().WithZone("").String(), true
}

// metadata reads the properties of an event. Anything that is not a JSON object, or that is too big, is replaced by a note: the
// event itself is still worth keeping.
func metadata(raw json.RawMessage) map[string]any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return map[string]any{}
	}
	if len(trimmed) > MaxMetadataBytes {
		return map[string]any{"metadata_dropped": "too large"}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil || m == nil {
		return map[string]any{"metadata_dropped": "not an object"}
	}
	return m
}

func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// Process reads the body of a request, {"data": [events]}, and normalizes the events in it. An element that cannot even be read
// (a field of the wrong type, not an object) is skipped like any other unusable event. Only a body that is not of this form,
// or holds too many events, is an error.
func Process(body []byte, now time.Time) (Result, error) {
	var req struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Data == nil {
		return Result{}, errors.New("the body must be {\"data\": [events]}")
	}
	if len(req.Data) > MaxBatch {
		return Result{}, ErrTooMany
	}

	events := make([]RawEvent, 0, len(req.Data))
	unreadable := 0
	for _, item := range req.Data {
		var e RawEvent
		if err := json.Unmarshal(item, &e); err != nil {
			unreadable++
			continue
		}
		events = append(events, e)
	}

	res, err := Normalize(events, now)
	res.Skipped += unreadable
	return res, err
}
