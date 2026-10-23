// Package fielderr is a refusal that belongs to one field of a request, so that the API can say which field it is.
// The domain packages return it; the handlers turn it into the 422 response.
package fielderr

import (
	"errors"
	"fmt"
)

// Error names the field and says what is wrong with it, in the form "must be ..." or "is required".
type Error struct{ Field, Message string }

// Error implements the error interface.
func (e *Error) Error() string { return e.Field + ": " + e.Message }

// New returns an *Error for field with a formatted message.
func New(field, format string, a ...any) error {
	return &Error{Field: field, Message: fmt.Sprintf(format, a...)}
}

// Of returns the *Error inside err, if there is one.
func Of(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
