package validate

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
)

func fieldsOf(t *testing.T, err error) []apierr.FieldError {
	t.Helper()
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("error %v is not an *apierr.Error", err)
	}
	if ae.Status != 422 {
		t.Fatalf("status = %d, want 422", ae.Status)
	}
	return ae.Fields
}

func TestRules(t *testing.T) {
	re := regexp.MustCompile(`^[a-z]+$`)

	tests := []struct {
		name     string
		run      func(v *Validator)
		wantRule string // empty: must pass
		wantMsg  string
	}{
		{"required ok", func(v *Validator) { v.Required("name", "x") }, "", ""},
		{"required empty", func(v *Validator) { v.Required("first_name", "") }, "required", "The first name field is required."},
		{"length ok", func(v *Validator) { v.Length("name", "abc", 1, 3) }, "", ""},
		{"length counts characters", func(v *Validator) { v.Length("name", "ééé", 1, 3) }, "", ""},
		{"length too long", func(v *Validator) { v.Length("name", "abcd", 1, 3) }, "between", "The name field must be between 1 and 3 characters."},
		{"length too short", func(v *Validator) { v.Length("name", "", 1, 3) }, "between", ""},
		{"matches ok", func(v *Validator) { v.Matches("slug", "abc", re) }, "", ""},
		{"matches fails", func(v *Validator) { v.Matches("slug", "ab1", re) }, "regex", "The slug field format is invalid."},
		{"oneof ok", func(v *Validator) { v.OneOf("signal", "kill", "start", "stop", "kill") }, "", ""},
		{"oneof fails", func(v *Validator) { v.OneOf("signal", "reboot", "start", "stop") }, "in", "The selected signal is invalid."},
		{"oneof is case sensitive", func(v *Validator) { v.OneOf("signal", "START", "start") }, "in", ""},
		{"int ok", func(v *Validator) { v.IntRange("per_page", 50, 1, 100) }, "", ""},
		{"int at the limits", func(v *Validator) { v.IntRange("per_page", 100, 1, 100); v.IntRange("page", 1, 1, 100) }, "", ""},
		{"int too small", func(v *Validator) { v.IntRange("per_page", 0, 1, 100) }, "between", "The per page field must be between 1 and 100."},
		{"int too large", func(v *Validator) { v.IntRange("per_page", 101, 1, 100) }, "between", ""},
		{"uuid ok", func(v *Validator) { v.UUID("uuid", "9f4d2c10-0000-4000-8000-000000000001") }, "", ""},
		{"uuid upper case ok", func(v *Validator) { v.UUID("uuid", "9F4D2C10-0000-4000-8000-00000000000A") }, "", ""},
		{"uuid without dashes", func(v *Validator) { v.UUID("uuid", "9f4d2c1000004000800000000000001") }, "uuid", "The uuid field must be a valid UUID."},
		{"uuid with trailing newline", func(v *Validator) { v.UUID("uuid", "9f4d2c10-0000-4000-8000-000000000001\n") }, "uuid", ""},
		{"uuid path traversal", func(v *Validator) { v.UUID("uuid", "../../etc/passwd") }, "uuid", ""},
		{"email ok", func(v *Validator) { v.Email("email", "ada@example.com") }, "", ""},
		{"email with display name", func(v *Validator) { v.Email("email", "Ada <ada@example.com>") }, "email", "The email field must be a valid email address."},
		{"email without domain", func(v *Validator) { v.Email("email", "ada") }, "email", ""},
		{"email with space", func(v *Validator) { v.Email("email", "a da@example.com") }, "email", ""},
		{"email too long", func(v *Validator) { v.Email("email", strings.Repeat("a", 190)+"@x.io") }, "email", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := New()

			tt.run(v)

			if tt.wantRule == "" {
				if !v.OK() || v.Err() != nil {
					t.Errorf("unexpected failure: %v", v.Err())
				}
				return
			}
			fs := fieldsOf(t, v.Err())
			if len(fs) != 1 || fs[0].Rule != tt.wantRule {
				t.Fatalf("fields = %+v, want one failure of rule %q", fs, tt.wantRule)
			}
			if tt.wantMsg != "" && fs[0].Detail != tt.wantMsg {
				t.Errorf("detail = %q, want %q", fs[0].Detail, tt.wantMsg)
			}
		})
	}
}

func TestOnlyTheFirstFailurePerFieldIsKept(t *testing.T) {
	v := New()
	v.Required("name", "")
	v.Length("name", "", 1, 5)
	v.Matches("name", "", regexp.MustCompile(`^x$`))
	v.Required("email", "")

	fs := fieldsOf(t, v.Err())

	if len(fs) != 2 || fs[0].Field != "name" || fs[0].Rule != "required" || fs[1].Field != "email" {
		t.Errorf("fields = %+v", fs)
	}
}

func TestErrIsATrueNilWhenNothingFailed(t *testing.T) {
	var err error = New().Err()
	if err != nil {
		t.Errorf("Err() = %#v, want a nil interface", err)
	}
}

func TestDecodeJSON(t *testing.T) {
	type payload struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}

	tests := []struct {
		name       string
		body       string
		max        int64
		want       payload
		wantStatus int
		wantDetail string
	}{
		{"valid", `{"name":"a","count":2}`, 1024, payload{"a", 2}, 0, ""},
		{"unknown fields are ignored", `{"name":"a","extra":true}`, 1024, payload{Name: "a"}, 0, ""},
		{"surrounding whitespace", " \n{\"name\":\"a\"}\n ", 1024, payload{Name: "a"}, 0, ""},
		{"empty", ``, 1024, payload{}, 400, "must be a JSON document"},
		{"only whitespace", "  \n", 1024, payload{}, 400, "must be a JSON document"},
		{"malformed", `{"name":`, 1024, payload{}, 400, "not valid JSON"},
		{"not an object", `[1,2]`, 1024, payload{}, 400, "wrong type"},
		{"wrong type", `{"count":"two"}`, 1024, payload{}, 400, "wrong type"},
		{"trailing document", `{"name":"a"}{"name":"b"}`, 1024, payload{}, 400, "exactly one"},
		{"trailing garbage", `{"name":"a"} x`, 1024, payload{}, 400, "exactly one"},
		{"too large", `{"name":"` + strings.Repeat("a", 100) + `"}`, 50, payload{}, 413, "too large"},
		{"exactly at the limit", `{"name":"a"}`, 12, payload{Name: "a"}, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got payload

			err := DecodeJSON(strings.NewReader(tt.body), &got, tt.max)

			if tt.wantStatus == 0 {
				if err != nil || got != tt.want {
					t.Fatalf("got %+v, %v; want %+v", got, err, tt.want)
				}
				return
			}
			var ae *apierr.Error
			if !errors.As(err, &ae) || ae.Status != tt.wantStatus || !strings.Contains(ae.Detail, tt.wantDetail) {
				t.Errorf("error = %v, want status %d containing %q", err, tt.wantStatus, tt.wantDetail)
			}
		})
	}
}
