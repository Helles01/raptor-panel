// Package field decodes the scalar values of a request body the way the reference panel's framework reads them, so
// that existing API clients keep working: a key may be missing, null or blank, numbers may arrive as numeric strings
// (billing modules often send them so) and booleans as 0 and 1. A value is never rejected while decoding; it is marked
// Invalid, and the validator reports it for its field in the usual 422 format.
package field

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// State tells what a request said about a field.
type State uint8

const (
	// Absent: the key was not sent. It is the zero value, so a field the body leaves out needs no handling.
	Absent State = iota
	// Null: sent as null, or as a blank string, which the framework turns into null before it validates.
	Null
	// Valid: sent with a value of the right kind.
	Valid
	// Invalid: sent with a value of another kind, or one that does not read as the kind.
	Invalid
)

// Sent reports whether the key was in the body at all.
func (s State) Sent() bool { return s != Absent }

// maxExactInt is the largest integer a JSON number carries without losing digits.
const maxExactInt = 1 << 53

// String is a text field. The value is trimmed and a blank one counts as null, as in the reference.
type String struct {
	State State
	V     string
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *String) UnmarshalJSON(b []byte) error {
	*s = readString(b, true)
	return nil
}

// Secret is a text field that is kept as sent: a password may start or end with a space. Only an empty string counts
// as null.
type Secret struct {
	State State
	V     string
}

// UnmarshalJSON implements json.Unmarshaler.
func (s *Secret) UnmarshalJSON(b []byte) error {
	r := readString(b, false)
	*s = Secret(r)
	return nil
}

func readString(b []byte, trim bool) String {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		return String{State: Null}
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return String{State: Invalid}
	}
	if trim {
		v = strings.TrimFunc(v, isBlank)
	}
	if v == "" {
		return String{State: Null}
	}
	return String{State: Valid, V: v}
}

// isBlank is what the framework's trimming removes: white space and the zero-width marks that look like none.
func isBlank(r rune) bool {
	return unicode.IsSpace(r) || r == 0xFEFF || r == 0x200B || r == 0x200E
}

// Int is a whole number. It reads a JSON number without a fraction, or a string of decimal digits with an optional
// sign; a number with a fraction is Invalid, where the reference would let the database round it.
type Int struct {
	State State
	V     int64
}

// UnmarshalJSON implements json.Unmarshaler.
func (i *Int) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case bytes.Equal(b, []byte("null")):
		*i = Int{State: Null}
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			*i = Int{State: Invalid}
			return nil
		}
		*i = intFromText(strings.TrimFunc(s, isBlank))
	case len(b) > 0 && (b[0] == '-' || (b[0] >= '0' && b[0] <= '9')):
		*i = intFromNumber(string(b))
	default:
		*i = Int{State: Invalid}
	}
	return nil
}

func intFromText(s string) Int {
	if s == "" {
		return Int{State: Null}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return Int{State: Invalid}
	}
	return Int{State: Valid, V: n}
}

func intFromNumber(s string) Int {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return Int{State: Valid, V: n}
	}
	// 5.0 and 1e3 are whole numbers to PHP too.
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != math.Trunc(f) || math.Abs(f) > maxExactInt {
		return Int{State: Invalid}
	}
	return Int{State: Valid, V: int64(f)}
}

// Bool is a flag. It reads true and false, the numbers 1 and 0 and the strings "1" and "0", which are what the
// reference's boolean rule accepts. Any other string, such as "true", is Invalid.
type Bool struct {
	State State
	V     bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *Bool) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch string(b) {
	case "null":
		*f = Bool{State: Null}
	case "true", "1":
		*f = Bool{State: Valid, V: true}
	case "false", "0":
		*f = Bool{State: Valid}
	default:
		*f = Bool{State: Invalid}
		if len(b) > 0 && b[0] == '"' {
			var s string
			if err := json.Unmarshal(b, &s); err == nil {
				switch strings.TrimFunc(s, isBlank) {
				case "":
					*f = Bool{State: Null}
				case "1":
					*f = Bool{State: Valid, V: true}
				case "0":
					*f = Bool{State: Valid}
				}
			}
		}
	}
	return nil
}

// Text is a value that is kept as text but may be sent as a number, such as the value of a startup variable ("25565" or
// 25565). A number is written as its digits (a whole one without a fraction or exponent). Anything else that is not a string, a
// flag included, is Invalid: the reference would turn true into "1" and false into "", which no rule can make sense of.
type Text struct {
	State State
	V     string
}

// UnmarshalJSON implements json.Unmarshaler.
func (t *Text) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && (b[0] == '-' || (b[0] >= '0' && b[0] <= '9')) {
		f, err := strconv.ParseFloat(string(b), 64)
		if err != nil {
			*t = Text{State: Invalid}
			return nil
		}
		if n, err := strconv.ParseInt(string(b), 10, 64); err == nil {
			*t = Text{State: Valid, V: strconv.FormatInt(n, 10)}
			return nil
		}
		*t = Text{State: Valid, V: strconv.FormatFloat(f, 'f', -1, 64)}
		return nil
	}
	*t = Text(readString(b, true))
	return nil
}
