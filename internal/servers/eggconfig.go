package servers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// EggProcess is the process configuration of a server's egg as the node reads it (GET /api/remote/servers/{uuid}, the
// "process_configuration" block): how to tell the server has started, how to stop it, and which files to edit.
type EggProcess struct {
	Startup EggStartup      `json:"startup"`
	Stop    EggStop         `json:"stop"`
	Configs []EggConfigFile `json:"configs"`
}

// EggStartup says how the node recognises a started server.
type EggStartup struct {
	Done            []string `json:"done"`
	UserInteraction []string `json:"user_interaction"`
	StripANSI       bool     `json:"strip_ansi"`
}

// EggStop is how to stop a server: a console command, or a signal.
type EggStop struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// EggConfigFile is one file the node edits before the server starts.
type EggConfigFile struct {
	Parser  string        `json:"parser"`
	File    string        `json:"file"`
	Replace []Replacement `json:"replace"`
}

// Replacement is one edit of a file. ReplaceWith is a string, a number, a boolean, or null.
type Replacement struct {
	Match       string `json:"match"`
	IfValue     string `json:"if_value,omitempty"`
	ReplaceWith any    `json:"replace_with"`
}

const maxEggJSONDepth = 32

// EggProcessConfiguration builds the process configuration of the server's egg, with the {{server.…}} and {{env.…}}
// placeholders in the replacement values filled in for this server. {{config.…}} placeholders are left for the node,
// which is the only one that knows its own configuration.
//
// An egg that has no JSON in a field gets the empty configuration for it. Malformed JSON is an error, so one bad egg is
// reported for its own servers instead of breaking what the node reads.
func EggProcessConfiguration(s Server) (EggProcess, error) {
	startup, err := eggStartup(s.Egg.ConfigStartup)
	if err != nil {
		return EggProcess{}, fmt.Errorf("egg startup configuration: %w", err)
	}
	configs, err := eggConfigFiles(s)
	if err != nil {
		return EggProcess{}, fmt.Errorf("egg file configuration: %w", err)
	}
	return EggProcess{Startup: startup, Stop: eggStop(s.Egg.ConfigStop), Configs: configs}, nil
}

func eggStartup(raw string) (EggStartup, error) {
	out := EggStartup{Done: []string{}, UserInteraction: []string{}}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}

	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return EggStartup{}, err
	}
	switch done := doc["done"].(type) {
	case string:
		out.Done = []string{done}
	case []any:
		for _, d := range done {
			if text, ok := d.(string); ok {
				out.Done = append(out.Done, text)
			}
		}
	}
	out.StripANSI, _ = doc["strip_ansi"].(bool)
	return out, nil
}

// eggStop turns the stop setting into a console command, or into a signal when it starts with a caret ("^SIGINT").
func eggStop(stop string) EggStop {
	if !strings.HasPrefix(stop, "^") {
		return EggStop{Type: "command", Value: stop}
	}
	return EggStop{Type: "signal", Value: upperASCII(stop[1:])}
}

func upperASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}, s)
}

func eggConfigFiles(s Server) ([]EggConfigFile, error) {
	out := []EggConfigFile{}
	if strings.TrimSpace(s.Egg.ConfigFiles) == "" {
		return out, nil
	}

	doc, err := parseOrdered(s.Egg.ConfigFiles)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return out, nil
	}
	files, ok := doc.(*object)
	if !ok {
		return nil, errors.New("must be a JSON object keyed by file name")
	}

	x, err := newExpander(s)
	if err != nil {
		return nil, err
	}

	for _, name := range files.keys {
		entry, ok := files.vals[name].(*object)
		if !ok {
			continue
		}
		parser, _ := entry.vals["parser"].(string)
		find, hasFind := entry.vals["find"]
		// An entry the node could not read would stop it from starting, so it is left out. This is also the reference's
		// answer for an entry without "find".
		if parser == "" || !hasFind || find == nil {
			continue
		}

		file := EggConfigFile{Parser: parser, File: name, Replace: []Replacement{}}
		if findObj, ok := find.(*object); ok {
			for _, match := range findObj.keys {
				file.Replace = append(file.Replace, x.replacements(match, findObj.vals[match])...)
			}
		}
		out = append(out, file)
	}
	return out, nil
}

// legacyModifiers maps the placeholders old eggs use, which the old daemon filled in itself, to what the node resolves.
var legacyModifiers = map[string]string{
	"config.docker.interface":        "config.docker.network.interface",
	"server.build.env.SERVER_MEMORY": "server.build.memory",
	"env.SERVER_MEMORY":              "server.build.memory",
	"server.build.env.SERVER_IP":     "server.build.default.ip",
	"env.SERVER_IP":                  "server.build.default.ip",
	"server.build.env.SERVER_PORT":   "server.build.default.port",
	"env.SERVER_PORT":                "server.build.default.port",
}

var placeholder = regexp.MustCompile(`\{\{([\w.-]*)\}\}`)

// expander fills in placeholders from the legacy configuration of one server.
type expander struct {
	structure map[string]any
}

func newExpander(s Server) (*expander, error) {
	raw, err := json.Marshal(Legacy(s))
	if err != nil {
		return nil, fmt.Errorf("encode the server configuration: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var structure map[string]any
	if err := dec.Decode(&structure); err != nil {
		return nil, fmt.Errorf("decode the server configuration: %w", err)
	}
	return &expander{structure: structure}, nil
}

// replacements makes the entries for one key of an egg's "find" object.
func (x *expander) replacements(match string, value any) []Replacement {
	switch v := value.(type) {
	case *object:
		// {"match": {"if value": "replacement"}}: replace only where the line has the first value.
		var out []Replacement
		for _, ifValue := range v.keys {
			if with, ok := x.scalar(v.vals[ifValue]); ok {
				out = append(out, Replacement{Match: match, IfValue: ifValue, ReplaceWith: with})
			}
		}
		return out
	default:
		if with, ok := x.scalar(value); ok {
			return []Replacement{{Match: match, ReplaceWith: with}}
		}
		return nil
	}
}

// scalar prepares a replacement value. Only strings, numbers, booleans and null are values a file can be given: the node
// writes anything else as the text "<invalid>", so such entries are left out.
func (x *expander) scalar(value any) (any, bool) {
	switch v := value.(type) {
	case string:
		return x.expand(v), true
	case json.Number, bool, nil:
		return v, true
	default:
		return nil, false
	}
}

// expand fills in the placeholders of a string in one pass, so a value that contains a placeholder is not expanded again.
//   - {{server.X}} is the value at path X of the legacy configuration, {{env.X}} the environment variable X;
//     anything that has no value becomes empty.
//   - Placeholders of the old daemon are rewritten to the ones the node resolves, and {{config.X}} is left as it is.
//   - Anything else, such as {{SOMETHING}}, is not touched.
func (x *expander) expand(s string) string {
	return placeholder.ReplaceAllStringFunc(s, func(m string) string {
		key := m[2 : len(m)-2]
		if to, ok := legacyModifiers[key]; ok {
			return "{{" + to + "}}"
		}
		switch {
		case strings.HasPrefix(key, "server."):
			return phpString(lookup(x.structure, strings.TrimPrefix(key, "server.")))
		case strings.HasPrefix(key, "env."):
			return phpString(lookup(x.structure, "build.env."+strings.TrimPrefix(key, "env.")))
		}
		return m
	})
}

// lookup follows a dotted path through nested objects and arrays, and gives nil when there is nothing at the path.
func lookup(v any, path string) any {
	for _, segment := range strings.Split(path, ".") {
		switch node := v.(type) {
		case map[string]any:
			next, ok := node[segment]
			if !ok {
				return nil
			}
			v = next
		case []any:
			i, err := strconv.Atoi(segment)
			if err != nil || i < 0 || i >= len(node) {
				return nil
			}
			v = node[i]
		default:
			return nil
		}
	}
	return v
}

// phpString is the text a value takes inside a string: true is "1", false and null are empty, as in the reference.
// Objects and arrays have no text and are empty (the reference writes the word "Array").
func phpString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "1"
		}
	}
	return ""
}

// object is a JSON object that keeps the order of its keys, which is the order of the replacements.
type object struct {
	keys []string
	vals map[string]any
}

// parseOrdered decodes one JSON document. Objects become *object, arrays []any, numbers json.Number. A repeated key
// keeps the position of its first appearance and the value of its last, as PHP does.
func parseOrdered(raw string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	v, err := parseValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after the JSON document")
	}
	return v, nil
}

func parseValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxEggJSONDepth {
		return nil, errors.New("JSON is nested too deeply")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return tok, nil
	}

	switch delim {
	case '{':
		o := &object{vals: map[string]any{}}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := keyTok.(string)
			v, err := parseValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			if _, seen := o.vals[key]; !seen {
				o.keys = append(o.keys, key)
			}
			o.vals[key] = v
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return o, nil
	case '[':
		list := []any{}
		for dec.More() {
			v, err := parseValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return list, nil
	}
	return nil, fmt.Errorf("unexpected %q", delim)
}
