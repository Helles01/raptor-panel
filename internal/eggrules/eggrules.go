// Package eggrules checks the value of an egg variable against the validation rules the egg gives it, such as
// "required|string|max:20" or "nullable|regex:/^\d+$/". The rules are written in the reference panel's framework syntax; a
// Go reading of the part of it that eggs use. A rule that is not understood is refused when the rules are parsed, and a
// value is never let through by a rule that could not be applied.
package eggrules

import (
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrUnsupported is wrapped by the errors of Parse for a rule, a parameter or a pattern that cannot be used.
var ErrUnsupported = errors.New("unsupported validation rule")

const maxPatternLength = 1024

type rule struct {
	name   string
	params []string
	re     *regexp.Regexp
	nums   []float64
}

// Rules is a parsed rule string.
type Rules struct {
	rules    []rule
	nullable bool
	// numeric is set by an integer or numeric rule, and makes max, min, between and size compare numbers instead of lengths.
	numeric bool
}

var (
	letters        = regexp.MustCompile(`^[\p{L}\p{M}]+$`)
	lettersNumbers = regexp.MustCompile(`^[\p{L}\p{M}\p{N}]+$`)
	lettersDash    = regexp.MustCompile(`^[\p{L}\p{M}\p{N}_-]+$`)
	integerForm    = regexp.MustCompile(`^[+-]?(0|[1-9][0-9]*)$`)
	numericForm    = regexp.MustCompile(`^[ \t\n\r\v\f]*[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][+-]?[0-9]+)?[ \t\n\r\v\f]*$`)
)

// Parse reads a rule string, rules separated by "|" (as the framework does, so a pattern cannot contain a bar). An empty
// string is no rules. Supported: required, filled, nullable, sometimes, present, string, integer, numeric, boolean, alpha,
// alpha_num, alpha_dash, regex, max, min, between, size, digits, digits_between, in, not_in and url.
func Parse(text string) (Rules, error) {
	var r Rules
	if strings.TrimSpace(text) == "" {
		return r, nil
	}

	for _, part := range strings.Split(text, "|") {
		// An empty rule is skipped, as the framework does.
		if strings.TrimSpace(part) == "" {
			continue
		}
		ru, err := parseRule(strings.TrimSpace(part))
		if err != nil {
			return Rules{}, err
		}
		switch ru.name {
		case "nullable":
			r.nullable = true
		case "integer", "numeric":
			r.numeric = true
		}
		r.rules = append(r.rules, ru)
	}
	return r, nil
}

func unsupported(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(format, a...))
}

func parseRule(part string) (rule, error) {
	name, param, hasParam := strings.Cut(part, ":")
	ru := rule{name: name}

	switch name {
	case "required", "filled", "nullable", "sometimes", "present", "string", "integer", "numeric", "boolean", "alpha", "alpha_num", "alpha_dash", "url":
		if hasParam {
			return rule{}, unsupported("%s takes no parameter", name)
		}
		return ru, nil

	case "regex":
		re, err := compilePattern(param)
		if err != nil {
			return rule{}, err
		}
		ru.re = re
		return ru, nil

	case "max", "min", "size", "digits":
		return withNumbers(ru, param, 1)
	case "between", "digits_between":
		return withNumbers(ru, param, 2)

	case "in", "not_in":
		params, err := splitParams(param)
		if err != nil || !hasParam {
			return rule{}, unsupported("%s needs a list of values", name)
		}
		ru.params = params
		return ru, nil
	}
	return rule{}, unsupported("%q is not a rule this panel knows", name)
}

func withNumbers(ru rule, param string, count int) (rule, error) {
	params, err := splitParams(param)
	if err != nil || len(params) != count {
		return rule{}, unsupported("%s needs %d number(s)", ru.name, count)
	}
	for _, p := range params {
		n, err := strconv.ParseFloat(p, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return rule{}, unsupported("%s: %q is not a number", ru.name, p)
		}
		ru.nums = append(ru.nums, n)
	}
	ru.params = params
	return ru, nil
}

func splitParams(param string) ([]string, error) {
	if param == "" {
		return nil, errors.New("no parameters")
	}
	r := csv.NewReader(strings.NewReader(param))
	r.LazyQuotes = true
	return r.Read()
}

// compilePattern reads a pattern in the delimited form "/…/flags". Only the flags i, m, s and D are understood. The flag u
// would also change what \w and \d mean, which a Go pattern does not do, so it is refused rather than read differently.
func compilePattern(spec string) (*regexp.Regexp, error) {
	if len(spec) < 3 || len(spec) > maxPatternLength {
		return nil, unsupported("a pattern must look like /…/ and be at most %d characters", maxPatternLength)
	}
	delim := spec[0]
	if delim != '/' && delim != '#' && delim != '~' && delim != '@' && delim != '!' && delim != '%' {
		return nil, unsupported("the pattern delimiter %q is not supported", string(delim))
	}
	end := strings.LastIndexByte(spec, delim)
	if end < 1 {
		return nil, unsupported("the pattern has no closing delimiter")
	}

	body, flags := spec[1:end], spec[end+1:]
	prefix := ""
	for _, f := range flags {
		switch f {
		case 'i', 'm', 's':
			prefix += string(f)
		case 'D':
		default:
			return nil, unsupported("the pattern flag %q is not supported", string(f))
		}
	}
	if prefix != "" {
		body = "(?" + prefix + ")" + body
	}

	re, err := regexp.Compile(body)
	if err != nil {
		return nil, unsupported("the pattern cannot be used: %v", err)
	}
	return re, nil
}

// Normalize prepares what a user typed the way the framework does before the rules see it: white space at both ends is
// removed, and nothing at all becomes null. What is stored is the result, or an empty string for null.
func Normalize(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.Trim(*value, " \t\n\r\x00\x0b")
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// Validate checks a value, already normalized, and returns a message for every rule that it fails, in the order of the rules,
// as the framework lists them. attribute is how the value is called in the messages ("value", or "Server Jar variable").
// No messages means the value is valid. A null value passes only if the rules say nullable; "required" and "filled" always
// refuse it.
func (r Rules) Validate(value *string, attribute string) []string {
	var msgs []string
	for _, ru := range r.rules {
		if value == nil && r.nullable && !implicit(ru.name) {
			continue
		}
		if ok, msg := r.check(ru, value); !ok {
			msgs = append(msgs, strings.ReplaceAll(msg, ":attribute", attribute))
		}
	}
	return msgs
}

func implicit(name string) bool {
	return name == "required" || name == "filled" || name == "present"
}

// check applies one rule. A null value counts as an empty string where a rule only measures or matches text, and fails the
// rules that need a string or a number.
func (r Rules) check(ru rule, v *string) (bool, string) {
	s := ""
	if v != nil {
		s = *v
	}

	switch ru.name {
	case "sometimes", "nullable", "present":
		return true, ""

	case "required", "filled":
		return v != nil, "The :attribute field is required."
	case "string":
		return v != nil, "The :attribute must be a string."
	case "integer":
		return v != nil && isInteger(s), "The :attribute must be an integer."
	case "numeric":
		return v != nil && isNumeric(s), "The :attribute must be a number."
	case "boolean":
		return v != nil && (s == "0" || s == "1"), "The :attribute field must be true or false."
	case "alpha":
		return v != nil && letters.MatchString(s), "The :attribute may only contain letters."
	case "alpha_num":
		return v != nil && lettersNumbers.MatchString(s), "The :attribute may only contain letters and numbers."
	case "alpha_dash":
		return v != nil && lettersDash.MatchString(s), "The :attribute may only contain letters, numbers, and dashes."
	case "regex":
		return v != nil && ru.re.MatchString(s), "The :attribute format is invalid."
	case "url":
		return v != nil && isURL(s), "The :attribute format is invalid."

	case "in":
		return contains(ru.params, s), "The selected :attribute is invalid."
	case "not_in":
		return !contains(ru.params, s), "The selected :attribute is invalid."

	case "digits":
		return allDigits(s) && float64(len(s)) == ru.nums[0], fmt.Sprintf("The :attribute must be %s digits.", ru.params[0])
	case "digits_between":
		n := float64(len(s))
		return allDigits(s) && n >= ru.nums[0] && n <= ru.nums[1], fmt.Sprintf("The :attribute must be between %s and %s digits.", ru.params[0], ru.params[1])
	}

	size, kind := r.size(s, v != nil)
	switch ru.name {
	case "max":
		return size <= ru.nums[0], fmt.Sprintf("The :attribute may not be greater than %s%s.", ru.params[0], kind)
	case "min":
		return size >= ru.nums[0], fmt.Sprintf("The :attribute must be at least %s%s.", ru.params[0], kind)
	case "size":
		return size == ru.nums[0], fmt.Sprintf("The :attribute must be %s%s.", ru.params[0], kind)
	case "between":
		return size >= ru.nums[0] && size <= ru.nums[1], fmt.Sprintf("The :attribute must be between %s and %s%s.", ru.params[0], ru.params[1], kind)
	}
	return false, "The :attribute is invalid."
}

// size measures a value for max, min, between and size: as a number when the rules include integer or numeric and the value is
// one, otherwise in characters. kind is the ending of the message for it.
func (r Rules) size(s string, present bool) (float64, string) {
	if r.numeric {
		if present && isNumeric(s) {
			n, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
			return n, ""
		}
		return float64(utf8.RuneCountInString(s)), ""
	}
	return float64(utf8.RuneCountInString(s)), " characters"
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// isInteger is PHP's FILTER_VALIDATE_INT: an optional sign and digits without leading zeros, within the range of 64 bits.
func isInteger(s string) bool {
	s = strings.Trim(s, " \t\n\r\v\f")
	if !integerForm.MatchString(s) {
		return false
	}
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

// isNumeric is PHP's is_numeric for text: an optional sign, digits with an optional fraction, an optional exponent.
func isNumeric(s string) bool { return numericForm.MatchString(s) }

// isURL accepts an http or https address with a host and nothing a browser would read differently from other software.
func isURL(s string) bool {
	if s == "" || strings.ContainsAny(s, " \\") || strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.Opaque == "" && u.User == nil
}
