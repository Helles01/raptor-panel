// Package apierr is the error type of the HTTP API and its JSON form, which follows the reference panel:
//
//	{"errors":[{"code":"NotFoundHttpException","status":"404","detail":"..."}]}
//
// The React frontend reads errors[0].detail and, for one case, errors[0].code ("TwoFactorAuthRequiredException"), so
// the codes keep the names the reference uses. Validation errors carry one entry per failed field, with
// meta.source_field and meta.rule.
package apierr

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// Codes that clients depend on or that mirror the reference panel's exception names.
const (
	CodeBadRequest       = "BadRequestHttpException"
	CodeUnauthenticated  = "AuthenticationException"
	CodeForbidden        = "AccessDeniedHttpException"
	CodeNotFound         = "NotFoundHttpException"
	CodeMethodNotAllowed = "MethodNotAllowedHttpException"
	CodeConflict         = "ConflictHttpException"
	CodeHTTP             = "HttpException" // any other HTTP error status
	CodeThrottled        = "ThrottleRequestsException"
	CodeValidation       = "ValidationException"
	CodeInternal         = "Exception"
	CodeTwoFactor        = "TwoFactorAuthRequiredException"

	// InternalDetail is the only thing a client ever learns about an unexpected failure.
	InternalDetail = "An unexpected error was encountered while processing this request, please try again."
)

// Error is an API error. The zero value is not useful; use the constructors.
type Error struct {
	Status int
	Code   string
	Detail string
	// Fields is set for validation errors.
	Fields []FieldError
	// RetryAfter is sent as the Retry-After header when set.
	RetryAfter time.Duration

	cause error
}

// FieldError is one failed validation rule.
type FieldError struct {
	Field  string
	Rule   string
	Detail string
}

func (e *Error) Error() string {
	if e.cause != nil {
		return e.Code + ": " + e.Detail + ": " + e.cause.Error()
	}
	return e.Code + ": " + e.Detail
}

// Unwrap returns the internal cause, which is for logs only and never reaches the client.
func (e *Error) Unwrap() error { return e.cause }

func BadRequest(detail string) *Error {
	return &Error{Status: http.StatusBadRequest, Code: CodeBadRequest, Detail: detail}
}

func Unauthenticated() *Error {
	return &Error{Status: http.StatusUnauthorized, Code: CodeUnauthenticated, Detail: "Unauthenticated."}
}

func Forbidden(detail string) *Error {
	return &Error{Status: http.StatusForbidden, Code: CodeForbidden, Detail: detail}
}

func NotFound() *Error {
	return &Error{Status: http.StatusNotFound, Code: CodeNotFound, Detail: "The requested resource could not be found on the server."}
}

func MethodNotAllowed() *Error {
	return &Error{Status: http.StatusMethodNotAllowed, Code: CodeMethodNotAllowed, Detail: "The method is not allowed for the requested route."}
}

func Conflict(detail string) *Error {
	return &Error{Status: http.StatusConflict, Code: CodeConflict, Detail: detail}
}

func TooLarge() *Error {
	return &Error{Status: http.StatusRequestEntityTooLarge, Code: CodeHTTP, Detail: "The request body is too large."}
}

// TooManyRequests is the answer to a throttled request. retryAfter is rounded up to whole seconds.
func TooManyRequests(retryAfter time.Duration) *Error {
	return &Error{Status: http.StatusTooManyRequests, Code: CodeThrottled, Detail: "Too Many Attempts.", RetryAfter: retryAfter}
}

// Validation returns a 422 for the failed fields. It returns nil when there are none.
func Validation(fields []FieldError) *Error {
	if len(fields) == 0 {
		return nil
	}
	return &Error{Status: http.StatusUnprocessableEntity, Code: CodeValidation, Detail: "The given data was invalid.", Fields: fields}
}

// Internal is a 500 with the generic message. cause is kept for the logs only.
func Internal(cause error) *Error {
	return &Error{Status: http.StatusInternalServerError, Code: CodeInternal, Detail: InternalDetail, cause: cause}
}

type entry struct {
	Code   string     `json:"code"`
	Status string     `json:"status"`
	Detail string     `json:"detail"`
	Meta   *entryMeta `json:"meta,omitempty"`
}

type entryMeta struct {
	SourceField string `json:"source_field"`
	Rule        string `json:"rule"`
}

type body struct {
	Errors []entry `json:"errors"`
}

// Body returns the value that is encoded as the response body.
func (e *Error) Body() any {
	status := strconv.Itoa(e.Status)
	if len(e.Fields) == 0 {
		return body{Errors: []entry{{Code: e.Code, Status: status, Detail: e.Detail}}}
	}

	entries := make([]entry, 0, len(e.Fields))
	for _, f := range e.Fields {
		entries = append(entries, entry{
			Code:   e.Code,
			Status: status,
			Detail: f.Detail,
			Meta:   &entryMeta{SourceField: f.Field, Rule: f.Rule},
		})
	}
	return body{Errors: entries}
}

// Write sends the error as a JSON response.
func Write(w http.ResponseWriter, e *Error) {
	payload, err := json.Marshal(e.Body())
	if err != nil {
		// The body only holds strings, so this cannot happen; fall back to a fixed body rather than an empty one.
		payload = []byte(`{"errors":[{"code":"Exception","status":"500","detail":"` + InternalDetail + `"}]}`)
		e = &Error{Status: http.StatusInternalServerError}
	}

	h := w.Header()
	h.Set("Content-Type", "application/json")
	if e.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(int((e.RetryAfter+time.Second-1)/time.Second)))
	}
	w.WriteHeader(e.Status)
	_, _ = w.Write(payload)
}
