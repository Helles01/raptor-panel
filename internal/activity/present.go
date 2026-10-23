package activity

import (
	"crypto/sha1" //nolint:gosec // only a stable key for the screen, as in the reference
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// hiddenEvents are not shown in any list: events whose data was never used.
var hiddenEvents = []string{"server:file.upload"}

// HiddenEvents returns the events that the lists leave out (NOT IN in the query).
func HiddenEvents() []string { return append([]string(nil), hiddenEvents...) }

// Stored is an entry as it is kept.
type Stored struct {
	ID    uint64
	Batch string // empty for none
	Event string
	// APIKeyID is set when the entry was made through an API key.
	APIKeyID *uint64
	// ActorID is the user who did it; nil when nobody did, or something other than a user.
	ActorID     *uint64
	IP          string
	Description string
	Properties  map[string]any
	Timestamp   time.Time
}

// Viewer is the user who reads a list.
type Viewer struct {
	ID    uint64
	Admin bool
}

// View is an entry as the API shows it (attributes of the object "activity_log").
type View struct {
	// ID is only a key for the screen: the same for the same entry, and not the number of the row.
	ID                    string         `json:"id"`
	Batch                 *string        `json:"batch"`
	Event                 string         `json:"event"`
	IsAPI                 bool           `json:"is_api"`
	IP                    *string        `json:"ip"`
	Description           string         `json:"description"`
	Properties            map[string]any `json:"properties"`
	HasAdditionalMetadata bool           `json:"has_additional_metadata"`
	Timestamp             string         `json:"timestamp"`
}

// hiddenValue replaces what a reader may not see.
const hiddenValue = "[hidden]"

// Present turns a stored entry into what viewer may see of it. holders says, for an event, which properties its text on the
// screen already shows (see ParseTexts); loc is the time zone of the panel.
//
// The address of the one who did it is shown to that user and to an administrator, and to nobody else: a subuser who reads the
// log of a server does not learn where the others connect from. An address kept among the properties is hidden from everybody
// but the user it belongs to, administrators included, as in the reference.
func Present(s Stored, v Viewer, holders func(event string) []string, loc *time.Location) View {
	own := s.ActorID != nil && *s.ActorID == v.ID
	if loc == nil {
		loc = time.UTC
	}
	var shown []string
	if holders != nil {
		shown = holders(s.Event)
	}

	view := View{
		ID:                    entryKey(s.ID),
		Event:                 s.Event,
		IsAPI:                 s.APIKeyID != nil,
		Description:           s.Description,
		Properties:            properties(s.Properties, own),
		HasAdditionalMetadata: hasAdditionalMetadata(s.Properties, shown),
		Timestamp:             s.Timestamp.In(loc).Format("2006-01-02T15:04:05-07:00"),
	}
	if s.Batch != "" {
		view.Batch = &s.Batch
	}
	if own || v.Admin {
		ip := s.IP
		view.IP = &ip
	}
	return view
}

func entryKey(id uint64) string {
	sum := sha1.Sum([]byte(strconv.FormatUint(id, 10))) //nolint:gosec // see the import
	return hex.EncodeToString(sum[:])
}

// properties is the object the screen fills its texts from: the stored properties, with a list or an object also counted under
// "<name>_count" (so that a text can say how many files), and when exactly one such count exists under "count" instead.
func properties(stored map[string]any, own bool) map[string]any {
	out := map[string]any{}
	for key, value := range stored {
		if key == "ip" && !own {
			out[key] = hiddenValue
			continue
		}
		switch x := value.(type) {
		case []any:
			out[key] = x
			out[key+"_count"] = len(x)
		case map[string]any:
			out[key] = x
			out[key+"_count"] = len(x)
		case string:
			if key == "directory" {
				x = strings.ReplaceAll("/"+strings.Trim(x, "/")+"/", "//", "/")
			}
			out[key] = x
		default:
			out[key] = value
		}
	}

	var counts []string
	for key := range out {
		if strings.HasSuffix(key, "_count") {
			counts = append(counts, key)
		}
	}
	if len(counts) == 1 {
		out["count"] = out[counts[0]]
		delete(out, counts[0])
	}
	return out
}

// hasAdditionalMetadata says whether the entry has a property that its text does not show and that is not only the address,
// the browser or the use of SFTP, so that the screen offers the button for the rest. A list of files is not shown whole by a
// text, so an entry that has one always has more to see.
func hasAdditionalMetadata(stored map[string]any, shown []string) bool {
	for key := range stored {
		switch key {
		case "ip", "useragent", "using_sftp":
			continue
		}
		if !contains(shown, key) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.-]*[A-Za-z0-9_])\s*\}\}`)

// ParseTexts reads the activity texts of the frontend (the file activity.json) and returns, for every event, the names its
// text fills in, written as in the text ("files.0.from"). An event whose text exists only in a singular and a plural form
// (compress_one and compress_other) has none: the reference finds no text for it either, so it treats every property of such an
// entry as one the text does not show.
func ParseTexts(raw []byte) (map[string][]string, error) {
	var tree map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, fmt.Errorf("activity texts: %w", err)
	}

	out := map[string][]string{}
	var walk func(path []string, node any)
	walk = func(path []string, node any) {
		switch v := node.(type) {
		case map[string]any:
			for k, child := range v {
				walk(append(append([]string(nil), path...), k), child)
			}
		case string:
			if len(path) < 2 {
				return
			}
			last := path[len(path)-1]
			plural := strings.HasSuffix(last, "_one") || strings.HasSuffix(last, "_other")
			base := strings.TrimSuffix(strings.TrimSuffix(last, "_one"), "_other")
			event := path[0] + ":" + strings.Join(append(append([]string(nil), path[1:len(path)-1]...), base), ".")
			if _, ok := out[event]; !ok {
				out[event] = nil
			}
			if plural {
				return
			}
			for _, m := range placeholder.FindAllStringSubmatch(v, -1) {
				if !contains(out[event], m[1]) {
					out[event] = append(out[event], m[1])
				}
			}
		}
	}
	walk(nil, tree)
	return out, nil
}
