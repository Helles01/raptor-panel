package apierr

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return out
}

func TestWriteMatchesTheReferenceShape(t *testing.T) {
	tests := []struct {
		name       string
		err        *Error
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{"bad request", BadRequest("The request body is not valid JSON."), 400, "BadRequestHttpException", "The request body is not valid JSON."},
		{"unauthenticated", Unauthenticated(), 401, "AuthenticationException", "Unauthenticated."},
		{"forbidden", Forbidden("This action is unauthorized."), 403, "AccessDeniedHttpException", "This action is unauthorized."},
		{"not found", NotFound(), 404, "NotFoundHttpException", "The requested resource could not be found on the server."},
		{"method not allowed", MethodNotAllowed(), 405, "MethodNotAllowedHttpException", "The method is not allowed for the requested route."},
		{"conflict", Conflict("Already exists."), 409, "ConflictHttpException", "Already exists."},
		{"too large", TooLarge(), 413, "HttpException", "The request body is too large."},
		{"throttled", TooManyRequests(time.Second), 429, "ThrottleRequestsException", "Too Many Attempts."},
		{"internal", Internal(errors.New("db password is hunter2")), 500, "Exception", InternalDetail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()

			Write(rec, tt.err)

			if rec.Code != tt.wantStatus || rec.Header().Get("Content-Type") != "application/json" {
				t.Errorf("status/content type = %d/%q", rec.Code, rec.Header().Get("Content-Type"))
			}
			errs := decode(t, rec)["errors"].([]any)
			if len(errs) != 1 {
				t.Fatalf("errors = %v, want one entry", errs)
			}
			first := errs[0].(map[string]any)
			// The status is a string, as in the reference.
			if first["code"] != tt.wantCode || first["status"] != strconv.Itoa(tt.wantStatus) || first["detail"] != tt.wantDetail {
				t.Errorf("entry = %v", first)
			}
			if _, has := first["meta"]; has {
				t.Errorf("only validation entries carry meta: %v", first)
			}
		})
	}
}

func TestInternalNeverLeaksTheCause(t *testing.T) {
	err := Internal(errors.New("dial tcp 10.0.0.5:3306: password=hunter2"))
	rec := httptest.NewRecorder()

	Write(rec, err)

	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Errorf("the cause reached the client: %s", rec.Body)
	}
	if !strings.Contains(err.Error(), "hunter2") {
		t.Error("the cause must stay available for logs through Error()")
	}
	if !errors.Is(err, err.Unwrap()) || err.Unwrap() == nil {
		t.Error("Unwrap must return the cause")
	}
}

func TestValidationHasOneEntryPerField(t *testing.T) {
	err := Validation([]FieldError{
		{Field: "name", Rule: "required", Detail: "The name field is required."},
		{Field: "email", Rule: "email", Detail: "The email field must be a valid email address."},
	})
	rec := httptest.NewRecorder()

	Write(rec, err)

	if rec.Code != 422 {
		t.Errorf("status = %d", rec.Code)
	}
	errs := decode(t, rec)["errors"].([]any)
	if len(errs) != 2 {
		t.Fatalf("errors = %v", errs)
	}
	first := errs[0].(map[string]any)
	meta := first["meta"].(map[string]any)
	if first["code"] != "ValidationException" || first["status"] != "422" || first["detail"] != "The name field is required." ||
		meta["source_field"] != "name" || meta["rule"] != "required" {
		t.Errorf("first entry = %v", first)
	}
}

func TestValidationWithoutFieldsIsNil(t *testing.T) {
	if Validation(nil) != nil || Validation([]FieldError{}) != nil {
		t.Error("no failed fields must give a nil error")
	}
}

func TestRetryAfterIsRoundedUp(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{time.Second, "1"},
		{1500 * time.Millisecond, "2"},
		{59*time.Second + time.Millisecond, "60"},
		{time.Millisecond, "1"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		Write(rec, TooManyRequests(tt.in))
		if got := rec.Header().Get("Retry-After"); got != tt.want {
			t.Errorf("Retry-After for %v = %q, want %q", tt.in, got, tt.want)
		}
	}

	rec := httptest.NewRecorder()
	Write(rec, NotFound())
	if rec.Header().Get("Retry-After") != "" {
		t.Error("Retry-After must only be set for throttled responses")
	}
}
