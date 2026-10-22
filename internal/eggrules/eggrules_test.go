package eggrules

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func s(v string) *string { return &v }

func must(t *testing.T, rules string) Rules {
	t.Helper()
	r, err := Parse(rules)
	if err != nil {
		t.Fatalf("Parse(%q): %v", rules, err)
	}
	return r
}

const (
	required = "The value field is required."
	mustStr  = "The value must be a string."
	invalid  = "The value format is invalid."
)

func TestValidate(t *testing.T) {
	long := func(n int) *string { return s(strings.Repeat("a", n)) }
	tests := []struct {
		name  string
		rules string
		value *string
		want  []string
	}{
		{"nothing to check", "", s("anything"), nil},
		{"nothing to check, no value", "", nil, nil},
		{"a string", "required|string", s("abc"), nil},
		{"no value, a required string", "required|string", nil, []string{required, mustStr}},
		{"a nullable string with no value", "nullable|string", nil, nil},
		{"a nullable string", "nullable|string", s("x"), nil},
		{"nullable does not excuse required", "nullable|required", nil, []string{required}},
		{"required before nullable", "required|nullable|string", nil, []string{required}},
		{"no value and no nullable fails the type", "string", nil, []string{mustStr}},

		{"a string at the limit", "required|string|max:20", long(20), nil},
		{"a string over the limit", "required|string|max:20", long(21), []string{"The value may not be greater than 20 characters."}},
		{"characters are counted, not bytes", "required|string|max:3", s("ééé"), nil},
		{"characters, not bytes, over", "required|string|max:3", s("éééé"), []string{"The value may not be greater than 3 characters."}},
		{"no value is of length zero for max", "nullable|string|max:5", nil, nil},

		{"an integer", "required|integer", s("5"), nil},
		{"a negative integer", "required|integer", s("-5"), nil},
		{"an integer with a plus", "required|integer", s("+5"), nil},
		{"zero", "required|integer", s("0"), nil},
		{"a leading zero", "required|integer", s("05"), []string{"The value must be an integer."}},
		{"a fraction", "required|integer", s("5.0"), []string{"The value must be an integer."}},
		{"an exponent", "required|integer", s("1e3"), []string{"The value must be an integer."}},
		{"the largest 64-bit integer", "required|integer", s("9223372036854775807"), nil},
		{"one more", "required|integer", s("9223372036854775808"), []string{"The value must be an integer."}},
		{"text", "required|integer", s("abc"), []string{"The value must be an integer."}},
		{"a hex number", "required|integer", s("0x10"), []string{"The value must be an integer."}},

		{"a port in range", "required|integer|between:1025,65535", s("25565"), nil},
		{"the lower end", "required|integer|between:1025,65535", s("1025"), nil},
		{"the upper end", "required|integer|between:1025,65535", s("65535"), nil},
		{"below the range", "required|integer|between:1025,65535", s("1024"), []string{"The value must be between 1025 and 65535."}},
		{"above the range", "required|integer|between:1025,65535", s("70000"), []string{"The value must be between 1025 and 65535."}},
		{"text measured by length against a numeric range", "required|integer|between:1025,65535", s("abc"), []string{"The value must be an integer.", "The value must be between 1025 and 65535."}},
		{"a number at the limit", "required|integer|max:128", s("128"), nil},
		{"a number over the limit", "required|integer|max:128", s("129"), []string{"The value may not be greater than 128."}},
		{"a negative number under max", "required|integer|max:128", s("-5"), nil},
		{"min of a number", "required|integer|min:3", s("2"), []string{"The value must be at least 3."}},
		{"size of a number", "required|integer|size:3", s("4"), []string{"The value must be 3."}},
		{"size of a number, met", "required|integer|size:3", s("3"), nil},

		{"a number", "required|numeric", s("1.5"), nil},
		{"a number with an exponent", "required|numeric", s("1e3"), nil},
		{"a number with a leading dot", "required|numeric", s(".5"), nil},
		{"a number with a trailing dot", "required|numeric", s("5."), nil},
		{"a signed number", "required|numeric", s("-0.5"), nil},
		{"not a number", "required|numeric", s("abc"), []string{"The value must be a number."}},
		{"a hex number", "required|numeric", s("0x1A"), []string{"The value must be a number."}},
		{"a decimal comma", "required|numeric", s("1,5"), []string{"The value must be a number."}},
		{"a number alone, no value", "numeric", nil, []string{"The value must be a number."}},

		{"a boolean 0", "required|boolean", s("0"), nil},
		{"a boolean 1", "required|boolean", s("1"), nil},
		{"the word true is not accepted", "required|boolean", s("true"), []string{"The value field must be true or false."}},
		{"2 is not a boolean", "required|boolean", s("2"), []string{"The value field must be true or false."}},

		{"letters, digits and dashes", "required|string|alpha_dash", s("my-world_1"), nil},
		{"a space", "required|string|alpha_dash", s("my world"), []string{"The value may only contain letters, numbers, and dashes."}},
		{"a Unicode letter", "required|string|alpha_dash", s("café"), nil},
		{"a dot", "required|string|alpha_dash", s("a.b"), []string{"The value may only contain letters, numbers, and dashes."}},
		{"no value for alpha_dash", "required|string|alpha_dash", nil, []string{required, mustStr, "The value may only contain letters, numbers, and dashes."}},
		{"letters and digits", "required|alpha_num|between:1,6", s("abc123"), nil},
		{"a dash is not alpha_num", "required|alpha_num|between:1,6", s("ab-1"), []string{"The value may only contain letters and numbers."}},
		{"too long for between", "required|alpha_num|between:1,6", s("abcdefg"), []string{"The value must be between 1 and 6 characters."}},
		{"letters only", "required|alpha", s("abc"), nil},
		{"a digit is not alpha", "required|alpha", s("ab1"), []string{"The value may only contain letters."}},

		{"a 32 character token", "required|string|alpha_num|size:32", long(32), nil},
		{"a short token", "required|string|alpha_num|size:32", long(31), []string{"The value must be 32 characters."}},
		{"a long token", "required|string|alpha_num|size:32", long(33), []string{"The value must be 32 characters."}},
		{"a nullable token with no value", "nullable|string|alpha_num|size:32", nil, nil},
		{"between for text", "required|string|between:3,15", s("ab"), []string{"The value must be between 3 and 15 characters."}},
		{"between for text, in range", "required|string|between:3,15", s("abc"), nil},
		{"min for text", "required|string|min:3", s("ab"), []string{"The value must be at least 3 characters."}},

		{"a jar file", `required|regex:/^([\w\d._-]+)(\.jar)$/`, s("server.jar"), nil},
		{"not a jar file", `required|regex:/^([\w\d._-]+)(\.jar)$/`, s("server.zip"), []string{invalid}},
		{"a jar file in a folder", `required|regex:/^([\w\d._-]+)(\.jar)$/`, s("sub/dir.jar"), []string{invalid}},
		{"a jar file with a space", `required|regex:/^([\w\d._-]+)(\.jar)$/`, s("my server.jar"), []string{invalid}},
		{"only the extension", `required|regex:/^([\w\d._-]+)(\.jar)$/`, s(".jar"), []string{invalid}},
		{"a jar file followed by a command", `required|regex:/^([\w\d._-]+)(\.jar)$/`, s("a.jar\n--evil"), []string{invalid}},
		{"a word of 20", `required|regex:/^(\w{1,20})$/`, long(20), nil},
		{"a word of 21", `required|regex:/^(\w{1,20})$/`, long(21), []string{invalid}},
		{"a pattern and a length", `required|regex:/^[\w.-]*$/|max:64`, s("abc.def"), nil},
		{"a pattern and a length, too long", `required|regex:/^[\w.-]*$/|max:64`, long(65), []string{"The value may not be greater than 64 characters."}},
		{"a nullable pattern", `nullable|regex:/^[0-9\.\-]+$/`, nil, nil},
		{"a nullable pattern, matched", `nullable|regex:/^[0-9\.\-]+$/`, s("1.5-2"), nil},
		{"a nullable pattern, not matched", `nullable|regex:/^[0-9\.\-]+$/`, s("abc"), []string{invalid}},
		{"the pattern is searched, not anchored", `required|regex:/ab/`, s("xxabxx"), nil},
		{"the i flag", `required|regex:/^abc$/i`, s("ABC"), nil},
		{"no flag, case matters", `required|regex:/^abc$/`, s("ABC"), []string{invalid}},
		{"the m flag", `required|regex:/^a$/m`, s("x\na"), nil},
		{"no m flag", `required|regex:/^a$/`, s("x\na"), []string{invalid}},
		{"the s flag", `required|regex:/^a.b$/s`, s("a\nb"), nil},
		{"a dot does not match a newline", `required|regex:/^a.b$/`, s("a\nb"), []string{invalid}},
		{"another delimiter", `required|regex:#^a/b$#`, s("a/b"), nil},
		{"no value for a pattern", `required|regex:/^a*$/`, nil, []string{required, invalid}},

		{"a listed value", "required|string|in:recommended,latest", s("latest"), nil},
		{"an unlisted value", "required|string|in:recommended,latest", s("other"), []string{"The selected value is invalid."}},
		{"a list is exact", "required|in:vanilla,oxide,carbon", s("Oxide"), []string{"The selected value is invalid."}},
		{"a quoted list item", `required|in:"a,b",c`, s("a,b"), nil},
		{"a quoted list item split", `required|in:"a,b",c`, s("a"), []string{"The selected value is invalid."}},
		{"not in a list", "required|not_in:root,admin", s("user"), nil},
		{"in the forbidden list", "required|not_in:root,admin", s("root"), []string{"The selected value is invalid."}},

		{"digits", "required|numeric|digits_between:1,6", s("123456"), nil},
		{"too many digits", "required|numeric|digits_between:1,6", s("1234567"), []string{"The value must be between 1 and 6 digits."}},
		{"a fraction is not digits", "required|numeric|digits_between:1,6", s("12.5"), []string{"The value must be between 1 and 6 digits."}},
		{"an exact number of digits", "required|digits:3", s("123"), nil},
		{"the wrong number of digits", "required|digits:3", s("12"), []string{"The value must be 3 digits."}},
		{"the right number of characters that are not digits", "required|digits:3", s("abc"), []string{"The value must be 3 digits."}},

		{"a URL", "nullable|url", s("https://example.com/file.zip"), nil},
		{"a URL with a port and query", "nullable|url", s("http://example.com:8080/a?b=c"), nil},
		{"no value for a nullable URL", "nullable|url", nil, nil},
		{"no scheme", "nullable|url", s("example.com/file.zip"), []string{invalid}},
		{"another scheme", "nullable|url", s("ftp://example.com/a"), []string{invalid}},
		{"a file URL", "nullable|url", s("file:///etc/passwd"), []string{invalid}},
		{"credentials in a URL", "nullable|url", s("https://user:pw@example.com/a"), []string{invalid}},
		{"a space in a URL", "nullable|url", s("https://example.com/a b"), []string{invalid}},
		{"no host", "nullable|url", s("https:///a"), []string{invalid}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := must(t, tt.rules).Validate(tt.value, "value")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Validate(%q, %v) = %q\nwant %q", tt.rules, tt.value, got, tt.want)
			}
		})
	}
}

func TestMessagesUseTheAttributeName(t *testing.T) {
	got := must(t, "required|integer").Validate(s("x"), "Server Jar File variable")
	if want := []string{"The Server Jar File variable must be an integer."}; !reflect.DeepEqual(got, want) {
		t.Errorf("messages = %q, want %q", got, want)
	}
}

func TestParseRefuses(t *testing.T) {
	for _, rules := range []string{
		"unknown", "required|unknown", "Required", "required:x", "nullable:x", "string:5",
		"max", "max:", "max:abc", "max:1,2", "max:NaN", "max:Inf", "between:1", "between:a,b", "between:1,2,3", "size", "digits", "digits_between:1",
		"in", "in:", "not_in",
		"regex", "regex:", "regex:abc", "regex:/abc", "regex:/", "regex://", "regex:{abc}", "regex:/abc/u", "regex:/abc/x", "regex:/abc/U",
		"regex:/(?=a)b/", "regex:/(?<!a)b/", `regex:/(a)\1/`, "regex:/(unclosed/", "regex:/a{2,1}/",
		"regex:/" + strings.Repeat("a", 1030) + "/",
		"email", "ip", "uuid", "starts_with:a", "date", "confirmed", "exists:t,c", "unique:t", "required_if:a,b", "array", "file",
	} {
		_, err := Parse(rules)
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("Parse(%q) error = %v, want ErrUnsupported", rules, err)
		}
	}
}

func TestParseAccepts(t *testing.T) {
	for _, rules := range []string{
		"", "   ", "required||string", "required| string", " required |string", "required|string|", "|required",
		"sometimes|required", "present", "filled", "nullable|alpha",
		"regex:/a/", "regex:/a/i", "regex:/a/ims", "regex:/a/D", `regex:/^[a-z]+$/|max:5`, "regex:#a#", "regex:~a~", "regex:@a@",
		"max:5", "max:-1", "max:1.5", "min:0", "between:1,5", "between:-5,5", "digits:6",
	} {
		if _, err := Parse(rules); err != nil {
			t.Errorf("Parse(%q) = %v", rules, err)
		}
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		in   *string
		want *string
	}{
		{nil, nil},
		{s(""), nil},
		{s("   "), nil},
		{s(" \t\r\n"), nil},
		{s("abc"), s("abc")},
		{s("  abc  "), s("abc")},
		{s("\n abc \t"), s("abc")},
		{s("a b"), s("a b")},
		{s("\x00abc\x0b"), s("abc")},
	}
	for _, tt := range tests {
		got := Normalize(tt.in)
		switch {
		case tt.want == nil && got != nil, tt.want != nil && (got == nil || *got != *tt.want):
			t.Errorf("Normalize(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}

	// A value is judged after it is normalized: spaces around a number do not make it text, and an empty one is null.
	r := must(t, "required|integer")
	if msgs := r.Validate(Normalize(s("  42 ")), "value"); msgs != nil {
		t.Errorf("a padded number: %q", msgs)
	}
	if msgs := r.Validate(Normalize(s("   ")), "value"); len(msgs) == 0 || msgs[0] != required {
		t.Errorf("spaces only: %q", msgs)
	}
	if msgs := must(t, "nullable|integer").Validate(Normalize(s("")), "value"); msgs != nil {
		t.Errorf("an empty value for a nullable rule: %q", msgs)
	}
}

func TestParseKeepsAPatternWithBackslashes(t *testing.T) {
	// The rules arrive as text with the backslashes of the pattern in them.
	r := must(t, `required|regex:/^(\d{1,3})\.(\d{1,3})$/`)
	if msgs := r.Validate(s("12.345"), "v"); msgs != nil {
		t.Errorf("messages = %q", msgs)
	}
	if msgs := r.Validate(s("1a.5"), "v"); len(msgs) != 1 {
		t.Errorf("messages = %q", msgs)
	}
}
