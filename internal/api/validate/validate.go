// Package validate checks request input against simple allow-lists and reports every failed field in the reference
// panel's 422 format. Messages and rule names follow Laravel's, which clients may display or match on.
package validate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Validator collects failures. Only the first failure of a field is kept.
type Validator struct {
	fields []apierr.FieldError
	failed map[string]bool
}

// New returns an empty Validator.
func New() *Validator { return &Validator{failed: map[string]bool{}} }

// OK reports whether nothing has failed so far.
func (v *Validator) OK() bool { return len(v.fields) == 0 }

// Err returns nil, or a 422 *apierr.Error listing every failed field. The return type is error, so a nil result is a
// true nil that is safe to compare.
func (v *Validator) Err() error {
	if e := apierr.Validation(v.fields); e != nil {
		return e
	}
	return nil
}

func name(field string) string { return strings.ReplaceAll(field, "_", " ") }

func (v *Validator) fail(field, rule, detail string) {
	if v.failed[field] {
		return
	}
	v.failed[field] = true
	v.fields = append(v.fields, apierr.FieldError{Field: field, Rule: rule, Detail: detail})
}

// Required fails for an empty string.
func (v *Validator) Required(field, value string) {
	if value == "" {
		v.fail(field, "required", fmt.Sprintf("The %s field is required.", name(field)))
	}
}

// Length fails unless the value has between min and max characters (not bytes).
func (v *Validator) Length(field, value string, min, max int) {
	if n := utf8.RuneCountInString(value); n < min || n > max {
		v.fail(field, "between", fmt.Sprintf("The %s field must be between %d and %d characters.", name(field), min, max))
	}
}

// Matches fails unless the whole value matches re.
func (v *Validator) Matches(field, value string, re *regexp.Regexp) {
	if !re.MatchString(value) {
		v.fail(field, "regex", fmt.Sprintf("The %s field format is invalid.", name(field)))
	}
}

// OneOf fails unless the value is one of the allowed ones.
func (v *Validator) OneOf(field, value string, allowed ...string) {
	for _, a := range allowed {
		if value == a {
			return
		}
	}
	v.fail(field, "in", fmt.Sprintf("The selected %s is invalid.", name(field)))
}

// IntRange fails unless min <= n <= max.
func (v *Validator) IntRange(field string, n, min, max int64) {
	if n < min || n > max {
		v.fail(field, "between", fmt.Sprintf("The %s field must be between %d and %d.", name(field), min, max))
	}
}

// UUID fails unless the value is a UUID in its canonical text form.
func (v *Validator) UUID(field, value string) {
	if !uuidPattern.MatchString(value) {
		v.fail(field, "uuid", fmt.Sprintf("The %s field must be a valid UUID.", name(field)))
	}
}

// Email fails unless the value is a bare e-mail address (no display name) of at most 191 characters.
func (v *Validator) Email(field, value string) {
	addr, err := mail.ParseAddress(value)
	if err != nil || addr.Address != value || utf8.RuneCountInString(value) > 191 || strings.ContainsAny(value, " \t\r\n<>(),;:\"") {
		v.fail(field, "email", fmt.Sprintf("The %s field must be a valid email address.", name(field)))
	}
}

// DecodeJSON reads one JSON value of at most maxBytes from r into dst. Anything else is a 400 (malformed JSON, an empty
// body, trailing data) or a 413 (too large) in the API's error format. Unknown fields are ignored, as in the reference.
func DecodeJSON(r io.Reader, dst any, maxBytes int64) error {
	raw, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return apierr.BadRequest("The request body could not be read.")
	}
	if int64(len(raw)) > maxBytes {
		return apierr.TooLarge()
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return apierr.BadRequest("The request body must be a JSON document.")
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(dst); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return apierr.BadRequest("The request body has a value of the wrong type.")
		}
		return apierr.BadRequest("The request body is not valid JSON.")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return apierr.BadRequest("The request body must hold exactly one JSON document.")
	}
	return nil
}
