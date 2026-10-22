// Package schedules holds the logic of server schedules that needs no database: the cron expressions that say when a
// schedule runs, and the rules for its tasks.
package schedules

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrInvalid is returned for cron fields that do not make an expression, or that never match a date. The reference
// answers both with "The cron data provided does not evaluate to a valid expression."
var ErrInvalid = errors.New("the cron data provided does not evaluate to a valid expression")

const (
	// searchYears is how far ahead Next looks for a run before it gives up (a 29 February needs up to eight years).
	searchYears    = 10
	maxFieldLength = 255
	maxNumberDigit = 3
)

// Cron is a parsed expression of the five fields of a schedule. The zero value is not usable; use Parse.
type Cron struct {
	minutes uint64
	hours   uint32
	months  uint16
	dom     dayOfMonth
	dow     dayOfWeek

	// A day field that is "*" (or "?") does not restrict. When both day fields restrict, a day matches if either does,
	// which is how cron has always worked and how the reference's library behaves.
	domAny, dowAny bool
}

type fieldSpec struct {
	name     string
	min, max int
	literals map[string]int
	// sunday makes 7 a second name of Sunday: ranges may start at 7 or end at 0.
	sunday bool
}

var (
	minuteField = fieldSpec{name: "minute", min: 0, max: 59}
	hourField   = fieldSpec{name: "hour", min: 0, max: 23}
	domField    = fieldSpec{name: "day of month", min: 1, max: 31}
	monthField  = fieldSpec{name: "month", min: 1, max: 12, literals: map[string]int{
		"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6, "JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
	}}
	dowField = fieldSpec{name: "day of week", min: 0, max: 7, sunday: true, literals: map[string]int{
		"MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6, "SUN": 7,
	}}
)

// Parse reads the five fields of a schedule, in the order the panel stores them. Each field takes numbers, names (months
// and weekdays), "*", lists ("1,5"), ranges ("1-5") and steps ("*/15", "10-30/5"). The day fields also take "?" (not
// both), the day of month "L" (last day) and "nW" (the weekday nearest day n), and the day of week "nL" (the last such
// weekday of the month) and "n#m" (the m-th such weekday).
//
// Parse is stricter than the reference's library where that library's behaviour is a quirk rather than a feature: a step
// must be smaller than the end of the field, a range must not run backwards, and numbers are plain digits.
func Parse(minute, hour, dayOfMonth, month, dayOfWeek string) (Cron, error) {
	type part struct {
		spec    fieldSpec
		text    string
		dayOnly bool // "?" is allowed
	}
	for _, p := range []part{
		{minuteField, minute, false}, {hourField, hour, false}, {domField, dayOfMonth, true},
		{monthField, month, false}, {dowField, dayOfWeek, true},
	} {
		if err := checkText(p.spec, p.text); err != nil {
			return Cron{}, err
		}
		if !p.dayOnly && strings.Contains(p.text, "?") {
			return Cron{}, p.spec.errorf("? is only allowed in the day fields")
		}
	}
	if dayOfMonth == "?" && dayOfWeek == "?" {
		return Cron{}, fmt.Errorf("%w: the day of month and the day of week cannot both be ?", ErrInvalid)
	}

	var c Cron
	var err error
	if c.minutes, err = minuteField.mask(minute); err != nil {
		return Cron{}, err
	}
	hours, err := hourField.mask(hour)
	if err != nil {
		return Cron{}, err
	}
	c.hours = uint32(hours)
	months, err := monthField.mask(month)
	if err != nil {
		return Cron{}, err
	}
	c.months = uint16(months)

	c.domAny, c.dowAny = isAny(dayOfMonth), isAny(dayOfWeek)
	if c.dom, err = parseDayOfMonth(dayOfMonth); err != nil {
		return Cron{}, err
	}
	if c.dow, err = parseDayOfWeek(dayOfWeek); err != nil {
		return Cron{}, err
	}
	return c, nil
}

func isAny(s string) bool { return s == "*" || s == "?" }

func (f fieldSpec) errorf(format string, a ...any) error {
	return fmt.Errorf("%w: %s: %s", ErrInvalid, f.name, fmt.Sprintf(format, a...))
}

// checkText is the allow-list for the text of a field: it is not empty, not long, and made only of letters, digits and
// the cron punctuation.
func checkText(f fieldSpec, s string) error {
	if s == "" || len(s) > maxFieldLength {
		return f.errorf("must be between 1 and %d characters", maxFieldLength)
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case strings.ContainsRune("*,/-#?", r):
		default:
			return f.errorf("contains a character that is not allowed")
		}
	}
	return nil
}

// number reads digits only, within the range of the field.
func (f fieldSpec) number(s string) (int, error) {
	if s == "" || len(s) > maxNumberDigit {
		return 0, f.errorf("%q is not a number", s)
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, f.errorf("%q is not a number", s)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, f.errorf("%q is not a number", s)
	}
	if n < f.min || n > f.max {
		return 0, f.errorf("%d is outside %d to %d", n, f.min, f.max)
	}
	return n, nil
}

// value is a number or a name.
func (f fieldSpec) value(s string) (int, error) {
	if n, ok := f.literals[strings.ToUpper(s)]; ok {
		return n, nil
	}
	return f.number(s)
}

// mask returns the values an expression selects, as a bit set (bit n for value n).
func (f fieldSpec) mask(expr string) (uint64, error) {
	var set uint64
	for _, item := range strings.Split(expr, ",") {
		m, err := f.itemMask(item)
		if err != nil {
			return 0, err
		}
		set |= m
	}
	return set, nil
}

func (f fieldSpec) all() uint64 {
	var set uint64
	for v := f.min; v <= f.max; v++ {
		set |= 1 << uint(v)
	}
	return set
}

func (f fieldSpec) itemMask(item string) (uint64, error) {
	if item == "" {
		return 0, f.errorf("a list item is empty")
	}

	if base, step, ok := strings.Cut(item, "/"); ok {
		return f.stepMask(base, step)
	}
	if item == "*" {
		return f.all(), nil
	}

	if from, to, ok := strings.Cut(item, "-"); ok {
		a, err := f.value(from)
		if err != nil {
			return 0, err
		}
		b, err := f.value(to)
		if err != nil {
			return 0, err
		}
		if f.sunday {
			// 7 and 0 are both Sunday, so a week can run "7-5" (Sunday to Friday) or "5-0" (Friday to Sunday).
			if a == 7 && b < 7 {
				a = 0
			} else if b == 0 && a > 0 {
				b = 7
			}
		}
		if a > b {
			return 0, f.errorf("the range %s runs backwards", item)
		}
		return f.rangeMask(a, b, 1), nil
	}

	v, err := f.value(item)
	if err != nil {
		return 0, err
	}
	return 1 << uint(v), nil
}

// stepMask is "*/n" or "a-b/n". A step must be 1 or more and below the end of the field: the reference's library gives
// larger steps a meaning that depends on the field ("*/60" selecting minute 0 only), which a schedule should not rely on.
func (f fieldSpec) stepMask(base, stepText string) (uint64, error) {
	step, err := f.step(stepText)
	if err != nil {
		return 0, err
	}

	lo, hi := f.min, f.max
	if base != "*" {
		from, to, ok := strings.Cut(base, "-")
		if !ok {
			return 0, f.errorf("a step needs * or a range in front of it, not %q", base)
		}
		// Names are not accepted here, as in the reference.
		if lo, err = f.number(from); err != nil {
			return 0, err
		}
		if hi, err = f.number(to); err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, f.errorf("the range %s runs backwards", base)
		}
	}
	return f.rangeMask(lo, hi, step), nil
}

func (f fieldSpec) step(s string) (int, error) {
	if s == "" || len(s) > maxNumberDigit {
		return 0, f.errorf("%q is not a valid step", s)
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, f.errorf("%q is not a valid step", s)
		}
	}
	n, _ := strconv.Atoi(s)
	if n < 1 || n >= f.max {
		return 0, f.errorf("a step must be from 1 to %d", f.max-1)
	}
	return n, nil
}

func (f fieldSpec) rangeMask(lo, hi, step int) uint64 {
	var set uint64
	for v := lo; v <= hi; v += step {
		set |= 1 << uint(v)
	}
	return set
}

// dayOfMonth is the day-of-month field. Besides numbers it can be "L" or "nW", each only on its own.
type dayOfMonth struct {
	days    uint32 // bit n for day n
	last    bool   // "L": the last day of the month
	nearest int    // "nW": the weekday nearest to day n, never leaving the month
}

func parseDayOfMonth(expr string) (dayOfMonth, error) {
	switch {
	case isAny(expr):
		return dayOfMonth{days: uint32(domField.all())}, nil
	case expr == "L":
		return dayOfMonth{last: true}, nil
	case strings.HasSuffix(expr, "W") && !strings.Contains(expr, ","):
		n, err := domField.number(strings.TrimSuffix(expr, "W"))
		if err != nil {
			return dayOfMonth{}, err
		}
		return dayOfMonth{nearest: n}, nil
	}

	set, err := domField.mask(expr)
	if err != nil {
		return dayOfMonth{}, err
	}
	return dayOfMonth{days: uint32(set)}, nil
}

func (d dayOfMonth) matches(y int, m time.Month, day, lastDay int) bool {
	switch {
	case d.last:
		return day == lastDay
	case d.nearest != 0:
		return d.nearest <= lastDay && day == nearestWeekday(y, m, d.nearest, lastDay)
	}
	return d.days&(1<<uint(day)) != 0
}

// nearestWeekday is the day of the month that "nW" stands for: n itself on a weekday, otherwise the closest weekday that
// is still in the month (a Saturday goes to the Friday before, except on the 1st where it goes to the Monday after; a
// Sunday goes to the Monday after, except on the last day where it goes to the Friday before).
func nearestWeekday(y int, m time.Month, n, lastDay int) int {
	switch weekday(y, m, n) {
	case time.Saturday:
		if n > 1 {
			return n - 1
		}
		return n + 2
	case time.Sunday:
		if n < lastDay {
			return n + 1
		}
		return n - 2
	}
	return n
}

// dayOfWeek is the day-of-week field. Sunday is bit 0.
type dayOfWeek struct {
	weekdays uint8    // plain values, ranges and steps
	last     uint8    // "nL": the last such weekday of the month
	nth      [6]uint8 // "n#m": nth[m] holds the weekdays whose m-th occurrence in the month matches
}

func parseDayOfWeek(expr string) (dayOfWeek, error) {
	if isAny(expr) {
		return dayOfWeek{weekdays: 0x7f}, nil
	}

	var d dayOfWeek
	for _, item := range strings.Split(expr, ",") {
		switch {
		case strings.Contains(item, "#"):
			name, nth, _ := strings.Cut(item, "#")
			wd, err := dowField.value(name)
			if err != nil {
				return dayOfWeek{}, err
			}
			n, err := strconv.Atoi(nth)
			if err != nil || len(nth) != 1 || n < 1 || n > 5 {
				return dayOfWeek{}, dowField.errorf("in %q the occurrence must be a digit from 1 to 5", item)
			}
			d.nth[n] |= 1 << uint(wd%7)

		case strings.HasSuffix(item, "L"):
			wd, err := dowField.value(strings.TrimSuffix(item, "L"))
			if err != nil {
				return dayOfWeek{}, err
			}
			d.last |= 1 << uint(wd%7)

		default:
			set, err := dowField.itemMask(item)
			if err != nil {
				return dayOfWeek{}, err
			}
			// 7 is Sunday too.
			set |= (set >> 7) & 1
			d.weekdays |= uint8(set & 0x7f)
		}
	}
	return d, nil
}

func (d dayOfWeek) matches(y int, m time.Month, day, lastDay int) bool {
	bit := uint8(1) << uint(weekday(y, m, day))
	return d.weekdays&bit != 0 ||
		(d.last&bit != 0 && lastDay-day < 7) ||
		d.nth[(day-1)/7+1]&bit != 0
}

// weekday is the day of the week of a calendar date.
func weekday(y int, m time.Month, d int) time.Weekday {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Weekday()
}

// Next returns the first run strictly after the given time, on a whole minute, in the location of that time. A local time
// that a clock change skips is not a run, and one that a clock change repeats runs once, at its first occurrence.
func (c Cron) Next(after time.Time) (time.Time, error) {
	loc := after.Location()
	// The moment to pass; compared as an instant so that the repeated hour of a clock change cannot give an earlier run.
	floor := after.Truncate(time.Minute)

	day := time.Date(after.Year(), after.Month(), after.Day(), 0, 0, 0, 0, time.UTC)
	end := day.AddDate(searchYears, 0, 0)
	for ; day.Before(end); day = day.AddDate(0, 0, 1) {
		y, m, d := day.Year(), day.Month(), day.Day()
		if c.months&(1<<uint(m)) == 0 || !c.dayMatches(y, m, d) {
			continue
		}

		for h := 0; h < 24; h++ {
			if c.hours&(1<<uint(h)) == 0 {
				continue
			}
			for min := 0; min < 60; min++ {
				if c.minutes&(1<<uint(min)) == 0 {
					continue
				}
				t := time.Date(y, m, d, h, min, 0, 0, loc)
				if t.Day() != d || t.Hour() != h || t.Minute() != min {
					continue // this local time does not exist
				}
				if t.After(floor) {
					return t, nil
				}
			}
		}
	}
	return time.Time{}, fmt.Errorf("%w: it matches no date in the next %d years", ErrInvalid, searchYears)
}

func (c Cron) dayMatches(y int, m time.Month, d int) bool {
	lastDay := time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
	dom := c.dom.matches(y, m, d, lastDay)
	dow := c.dow.matches(y, m, d, lastDay)
	switch {
	case c.domAny:
		return dow
	case c.dowAny:
		return dom
	}
	return dom || dow
}
