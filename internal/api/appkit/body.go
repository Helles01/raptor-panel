package appkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/field"
	"github.com/Helles01/raptor-panel/internal/api/validate"
	"github.com/Helles01/raptor-panel/internal/servers"
)

// EnvironmentError turns the refusal of the values of the variables of an egg into the 422 for them, with an entry on
// environment.<NAME> for each message. It is nil for any other error.
func EnvironmentError(err error) error {
	var ee *servers.EnvironmentError
	if !errors.As(err, &ee) {
		return nil
	}
	var fields []apierr.FieldError
	for _, p := range ee.Problems {
		for _, m := range p.Messages {
			fields = append(fields, apierr.FieldError{Field: p.Field, Rule: "invalid", Detail: m})
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return apierr.Validation(fields)
}

func label(name string) string { return strings.ReplaceAll(name, "_", " ") }

// Elements reads a field that must be a JSON array. A field that is not there, or is null, has no elements and is no error: a rule that
// needs it present says so itself.
func Elements(raw json.RawMessage, name string, v *validate.Validator) []json.RawMessage {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		return nil
	}
	var elements []json.RawMessage
	if t[0] != '[' || json.Unmarshal(raw, &elements) != nil {
		v.Custom(name, "array", fmt.Sprintf("The %s must be an array.", label(name)))
		return nil
	}
	return elements
}

// IDs reads a list of whole numbers, which a client may write as numbers or as text. A number below one is no identifier and is read
// as 0, which the rules then refuse as a record that is not there.
func IDs(raw json.RawMessage, name string, v *validate.Validator) []uint64 {
	var out []uint64
	for i, e := range Elements(raw, name, v) {
		var n field.Int
		if json.Unmarshal(e, &n) != nil || n.State != field.Valid {
			v.Custom(name, "integer", fmt.Sprintf("The %s.%d must be an integer.", label(name), i))
			return nil
		}
		if n.V < 1 {
			out = append(out, 0)
			continue
		}
		out = append(out, uint64(n.V))
	}
	return out
}

// Strings reads a list of texts.
func Strings(raw json.RawMessage, name string, v *validate.Validator) []string {
	var out []string
	for i, e := range Elements(raw, name, v) {
		var s string
		if json.Unmarshal(e, &s) != nil {
			v.Custom(name, "string", fmt.Sprintf("The %s.%d must be a string.", label(name), i))
			return nil
		}
		out = append(out, s)
	}
	return out
}

// Environment reads the values of the variables of an egg: an object, or an empty list as a client that writes PHP arrays sends one.
// A value is null, text, or a number written as its digits; anything else is refused on environment.<NAME>.
func Environment(raw json.RawMessage, v *validate.Validator) map[string]*string {
	t := strings.TrimSpace(string(raw))
	if t == "" || t == "null" {
		v.Custom("environment", "present", "The environment field must be present.")
		return nil
	}
	if t == "[]" {
		return map[string]*string{}
	}
	var values map[string]field.Text
	if t[0] != '{' || json.Unmarshal(raw, &values) != nil {
		v.Custom("environment", "array", "The environment must be an array.")
		return nil
	}
	out := make(map[string]*string, len(values))
	for name, x := range values {
		switch x.State {
		case field.Valid:
			val := x.V
			out[name] = &val
		case field.Null, field.Absent:
			out[name] = nil
		default:
			v.Custom("environment."+name, "string", fmt.Sprintf("The environment.%s must be a string.", name))
		}
	}
	return out
}
