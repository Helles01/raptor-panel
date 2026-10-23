package validate

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/Helles01/raptor-panel/internal/api/apierr"
	"github.com/Helles01/raptor-panel/internal/api/field"
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
		{"length too long", func(v *Validator) { v.Length("name", "abcd", 1, 3) }, "between", "The name must be between 1 and 3 characters."},
		{"length too short", func(v *Validator) { v.Length("name", "", 1, 3) }, "between", ""},
		{"bytes ok", func(v *Validator) { v.Bytes("command", "abc", 1, 3) }, "", ""},
		{"bytes count bytes", func(v *Validator) { v.Bytes("command", "ééé", 1, 3) }, "between", "The command must be between 1 and 3 bytes."},
		{"bytes too short", func(v *Validator) { v.Bytes("command", "", 1, 3) }, "between", ""},
		{"bytes too long", func(v *Validator) { v.Bytes("command", "abcd", 1, 3) }, "between", ""},
		{"matches ok", func(v *Validator) { v.Matches("slug", "abc", re) }, "", ""},
		{"matches fails", func(v *Validator) { v.Matches("slug", "ab1", re) }, "regex", "The slug format is invalid."},
		{"oneof ok", func(v *Validator) { v.OneOf("signal", "kill", "start", "stop", "kill") }, "", ""},
		{"oneof fails", func(v *Validator) { v.OneOf("signal", "reboot", "start", "stop") }, "in", "The selected signal is invalid."},
		{"oneof is case sensitive", func(v *Validator) { v.OneOf("signal", "START", "start") }, "in", ""},
		{"int ok", func(v *Validator) { v.IntRange("per_page", 50, 1, 100) }, "", ""},
		{"int at the limits", func(v *Validator) { v.IntRange("per_page", 100, 1, 100); v.IntRange("page", 1, 1, 100) }, "", ""},
		{"int too small", func(v *Validator) { v.IntRange("per_page", 0, 1, 100) }, "between", "The per page must be between 1 and 100."},
		{"int too large", func(v *Validator) { v.IntRange("per_page", 101, 1, 100) }, "between", ""},
		{"uuid ok", func(v *Validator) { v.UUID("uuid", "9f4d2c10-0000-4000-8000-000000000001") }, "", ""},
		{"uuid upper case ok", func(v *Validator) { v.UUID("uuid", "9F4D2C10-0000-4000-8000-00000000000A") }, "", ""},
		{"uuid without dashes", func(v *Validator) { v.UUID("uuid", "9f4d2c1000004000800000000000001") }, "uuid", "The uuid field must be a valid UUID."},
		{"uuid with trailing newline", func(v *Validator) { v.UUID("uuid", "9f4d2c10-0000-4000-8000-000000000001\n") }, "uuid", ""},
		{"uuid path traversal", func(v *Validator) { v.UUID("uuid", "../../etc/passwd") }, "uuid", ""},
		{"email ok", func(v *Validator) { v.Email("email", "ada@example.com") }, "", ""},
		{"email with display name", func(v *Validator) { v.Email("email", "Ada <ada@example.com>") }, "email", "The email must be a valid email address."},
		{"email without domain", func(v *Validator) { v.Email("email", "ada") }, "email", ""},
		{"email with space", func(v *Validator) { v.Email("email", "a da@example.com") }, "email", ""},
		{"email too long", func(v *Validator) { v.Email("email", strings.Repeat("a", 190)+"@x.io") }, "email", ""},
		{"sent ok", func(v *Validator) { v.Sent("name", field.Valid) }, "", ""},
		{"sent absent", func(v *Validator) { v.Sent("first_name", field.Absent) }, "required", "The first name field is required."},
		{"sent null", func(v *Validator) { v.Sent("name", field.Null) }, "required", ""},
		{"sent invalid is left to the type check", func(v *Validator) { v.Sent("name", field.Invalid) }, "", ""},
		{"string ok", func(v *Validator) { v.Type("name", field.Valid, KindString, false) }, "", ""},
		{"string absent", func(v *Validator) { v.Type("name", field.Absent, KindString, false) }, "", ""},
		{"string null not nullable", func(v *Validator) { v.Type("language", field.Null, KindString, false) }, "string", "The language must be a string."},
		{"string null nullable", func(v *Validator) { v.Type("password", field.Null, KindString, true) }, "", ""},
		{"string invalid even if nullable", func(v *Validator) { v.Type("password", field.Invalid, KindString, true) }, "string", ""},
		{"integer invalid", func(v *Validator) { v.Type("per_page", field.Invalid, KindInteger, false) }, "integer", "The per page must be an integer."},
		{"number invalid", func(v *Validator) { v.Type("memory", field.Invalid, KindNumber, false) }, "numeric", "The memory must be a number."},
		{"boolean invalid", func(v *Validator) { v.Type("root_admin", field.Invalid, KindBoolean, false) }, "boolean", "The root admin field must be true or false."},
		{"boolean null not nullable", func(v *Validator) { v.Type("public", field.Null, KindBoolean, false) }, "boolean", ""},
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
