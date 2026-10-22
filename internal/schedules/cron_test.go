package schedules

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const layout = "2006-01-02 15:04:05"

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.ParseInLocation(layout, s, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// parse reads "minute hour day-of-month month day-of-week".
func parse(t *testing.T, expr string) Cron {
	t.Helper()
	f := strings.Split(expr, " ")
	if len(f) != 5 {
		t.Fatalf("%q does not have five fields", expr)
	}
	c, err := Parse(f[0], f[1], f[2], f[3], f[4])
	if err != nil {
		t.Fatalf("Parse(%q): %v", expr, err)
	}
	return c
}

func TestNext(t *testing.T) {
	tests := []struct {
		name  string
		expr  string
		after string
		want  string
	}{
		{"every minute", "* * * * *", "2026-03-10 12:00:30", "2026-03-10 12:01:00"},
		{"strictly after, to the second", "* * * * *", "2026-03-10 12:00:00", "2026-03-10 12:01:00"},
		{"steps", "*/15 * * * *", "2026-03-10 12:07:00", "2026-03-10 12:15:00"},
		{"steps wrap to the next hour", "*/15 * * * *", "2026-03-10 12:45:00", "2026-03-10 13:00:00"},
		{"a run in the current minute is not a next run", "0 12 * * *", "2026-03-10 12:00:30", "2026-03-11 12:00:00"},
		{"just before", "0 12 * * *", "2026-03-10 11:59:59", "2026-03-10 12:00:00"},
		{"day of month", "30 4 1 * *", "2026-03-10 00:00:00", "2026-04-01 04:30:00"},
		{"day of week", "0 0 * * 1", "2026-03-10 00:00:00", "2026-03-16 00:00:00"},
		{"next year", "0 0 1 1 *", "2026-03-10 00:00:00", "2027-01-01 00:00:00"},

		// Both day fields given: a day matches if either does (15 March 2026 is a Sunday, 16 March a Monday).
		{"day of month or day of week", "0 0 15 * 1", "2026-03-10 00:00:00", "2026-03-15 00:00:00"},
		{"or: then the weekday", "0 0 15 * 1", "2026-03-15 00:00:00", "2026-03-16 00:00:00"},
		{"or: and on", "0 0 15 * 1", "2026-03-16 00:00:00", "2026-03-23 00:00:00"},
		{"? is no restriction", "0 0 ? * 1", "2026-03-10 00:00:00", "2026-03-16 00:00:00"},
		{"? in the other field", "0 0 15 * ?", "2026-03-10 00:00:00", "2026-03-15 00:00:00"},

		{"names and ranges", "0 9 * JAN-MAR MON-FRI", "2026-03-27 10:00:00", "2026-03-30 09:00:00"},
		{"names wrap to next year", "0 9 * jan-mar mon-fri", "2026-03-31 10:00:00", "2027-01-01 09:00:00"},
		{"lists and ranges", "5,35 8-10 * * *", "2026-03-10 08:36:00", "2026-03-10 09:05:00"},
		{"steps in a range", "10-30/10 * * * *", "2026-03-10 12:10:00", "2026-03-10 12:20:00"},
		{"steps in a range, next hour", "10-30/10 * * * *", "2026-03-10 12:30:00", "2026-03-10 13:10:00"},
		{"day of month steps start at 1", "0 0 */10 * *", "2026-03-02 00:00:00", "2026-03-11 00:00:00"},

		{"last day", "0 0 L * *", "2026-02-10 00:00:00", "2026-02-28 00:00:00"},
		{"last day, next month", "0 0 L * *", "2026-02-28 00:00:00", "2026-03-31 00:00:00"},
		{"weekday nearest, Saturday goes back", "0 0 15W * *", "2026-08-01 00:00:00", "2026-08-14 00:00:00"},
		{"weekday nearest, Sunday goes forward", "0 0 15W * *", "2026-11-01 00:00:00", "2026-11-16 00:00:00"},
		{"weekday nearest, 1st on a Saturday", "0 0 1W * *", "2026-07-02 00:00:00", "2026-08-03 00:00:00"},
		{"weekday nearest, 1st on a Sunday", "0 0 1W * *", "2026-01-31 00:00:00", "2026-02-02 00:00:00"},
		{"weekday nearest, last day on a Sunday", "0 0 31W * *", "2026-05-01 00:00:00", "2026-05-29 00:00:00"},
		{"weekday nearest, no such day is no match", "0 0 31W * *", "2026-05-30 00:00:00", "2026-07-31 00:00:00"},
		{"last weekday of the month", "0 0 * * 5L", "2026-03-01 00:00:00", "2026-03-27 00:00:00"},
		// 24 and 31 July 2026 are both Fridays: only the 31st is the last one.
		{"a weekday a week before the end is not the last", "0 0 * * 5L", "2026-07-01 00:00:00", "2026-07-31 00:00:00"},
		{"nth weekday", "0 0 * * 1#2", "2026-03-01 00:00:00", "2026-03-09 00:00:00"},
		// September 2026 has its Mondays on the 7th, 14th, 21st and 28th: the edges between the occurrences.
		{"first weekday on the 7th", "0 0 * * 1#1", "2026-08-04 00:00:00", "2026-09-07 00:00:00"},
		{"fourth weekday on the 28th", "0 0 * * 1#4", "2026-09-08 00:00:00", "2026-09-28 00:00:00"},
		{"a fifth weekday skips months without one", "0 0 * * 5#5", "2026-02-01 00:00:00", "2026-05-29 00:00:00"},
		{"leap day", "0 0 29 2 *", "2026-03-01 00:00:00", "2028-02-29 00:00:00"},

		{"Sunday as 0", "0 0 * * 0", "2026-03-10 00:00:00", "2026-03-15 00:00:00"},
		{"Sunday as 7", "0 0 * * 7", "2026-03-10 00:00:00", "2026-03-15 00:00:00"},
		{"Sunday by name", "0 0 * * SUN", "2026-03-10 00:00:00", "2026-03-15 00:00:00"},
		{"range to 7", "0 0 * * 5-7", "2026-03-10 00:00:00", "2026-03-13 00:00:00"},
		{"range to 7, reaches Sunday", "0 0 * * 5-7", "2026-03-14 00:00:00", "2026-03-15 00:00:00"},
		{"range from 7", "0 0 * * 7-1", "2026-03-10 00:00:00", "2026-03-15 00:00:00"},
		{"range to 0", "0 0 * * 5-0", "2026-03-10 00:00:00", "2026-03-13 00:00:00"},
		{"Monday to Sunday includes Sunday", "0 0 * * MON-SUN", "2026-03-14 00:00:00", "2026-03-15 00:00:00"},
		{"weekday steps", "0 0 * * */2", "2026-03-09 00:00:00", "2026-03-10 00:00:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parse(t, tt.expr).Next(at(t, tt.after))
			if err != nil {
				t.Fatal(err)
			}
			if want := at(t, tt.want); !got.Equal(want) {
				t.Errorf("Next(%s) = %s, want %s", tt.after, got.Format(layout), tt.want)
			}
		})
	}
}

func TestNextNeverMatches(t *testing.T) {
	// Valid expressions with no date to run on: the error says so instead of the search looping on.
	for _, expr := range []string{"0 0 31 2 *", "0 0 30 2 *", "0 0 31 4,6,9,11 *", "0 0 31W 2 *", "0 0 31 4 *"} {
		if _, err := parse(t, expr).Next(at(t, "2026-03-10 00:00:00")); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: error = %v, want ErrInvalid", expr, err)
		}
	}
}

func TestNextKeepsTheLocation(t *testing.T) {
	loc := time.FixedZone("test", 5*3600+1800)
	after := time.Date(2026, 3, 10, 12, 0, 0, 0, loc)
	got, err := parse(t, "30 12 * * *").Next(after)
	if err != nil {
		t.Fatal(err)
	}
	if got.Location() != loc || !got.Equal(time.Date(2026, 3, 10, 12, 30, 0, 0, loc)) {
		t.Errorf("got %v", got)
	}
}

func TestNextAcrossClockChanges(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone database")
	}

	tests := []struct {
		name  string
		expr  string
		after time.Time
		want  time.Time
	}{
		{"a skipped local time is not a run", "30 2 * * *",
			time.Date(2026, 3, 7, 12, 0, 0, 0, ny), time.Date(2026, 3, 9, 2, 30, 0, 0, ny)},
		{"a repeated local time runs at its first occurrence", "30 1 * * *",
			time.Date(2026, 10, 31, 12, 0, 0, 0, ny), time.Date(2026, 11, 1, 1, 30, 0, 0, ny)},
		{"and only once", "30 1 * * *",
			time.Date(2026, 11, 1, 1, 30, 0, 0, ny), time.Date(2026, 11, 2, 1, 30, 0, 0, ny)},
		// 06:30 UTC is 01:30 for the second time that night, in standard time.
		{"never earlier than the given time", "45 1 * * *",
			time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC).In(ny), time.Date(2026, 11, 2, 1, 45, 0, 0, ny)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parse(t, tt.expr).Next(tt.after)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("Next = %v, want %v", got, tt.want)
			}
			if !got.After(tt.after) {
				t.Errorf("the run %v is not after %v", got, tt.after)
			}
		})
	}
}

func TestParseAccepts(t *testing.T) {
	tests := [][5]string{
		{"*", "*", "*", "*", "*"},
		{"*/5", "*/2", "*/3", "*/4", "*/2"},
		{"0", "0", "1", "1", "0"},
		{"59", "23", "31", "12", "7"},
		{"00", "005", "01", "01", "00"},
		{"1,2,3", "1-5", "1,15", "1,6,12", "1,3,5"},
		{"10-50/10", "0-12/6", "1-31/5", "1-12/3", "1-7/2"},
		{"5,10-20/2,40", "*", "*", "*", "*"},
		{"*", "*", "?", "*", "1"},
		{"*", "*", "1", "*", "?"},
		{"*", "*", "L", "*", "*"},
		{"*", "*", "15W", "*", "*"},
		{"*", "*", "31W", "*", "*"},
		{"*", "*", "*", "jan", "mon"},
		{"*", "*", "*", "Jan-Mar,JUN", "Mon-Fri"},
		{"*", "*", "*", "DEC", "SUN"},
		{"*", "*", "*", "*", "5L"},
		{"*", "*", "*", "*", "FRI#2"},
		{"*", "*", "*", "*", "1#1,5L,MON-WED"},
		{"*", "*", "*", "*", "7-1"},
		{"*", "*", "*", "*", "5-0"},
		{"*/58", "*/22", "*/30", "*/11", "*/6"},
	}
	for _, f := range tests {
		if _, err := Parse(f[0], f[1], f[2], f[3], f[4]); err != nil {
			t.Errorf("Parse(%q) = %v", f, err)
		}
	}
}

func TestParseRejects(t *testing.T) {
	long := strings.Repeat("1,", 200) + "1"
	tests := []struct {
		name string
		f    [5]string
	}{
		{"minute out of range", [5]string{"60", "*", "*", "*", "*"}},
		{"hour out of range", [5]string{"*", "24", "*", "*", "*"}},
		{"day of month zero", [5]string{"*", "*", "0", "*", "*"}},
		{"day of month out of range", [5]string{"*", "*", "32", "*", "*"}},
		{"month zero", [5]string{"*", "*", "*", "0", "*"}},
		{"month out of range", [5]string{"*", "*", "*", "13", "*"}},
		{"weekday out of range", [5]string{"*", "*", "*", "*", "8"}},
		{"negative", [5]string{"-1", "*", "*", "*", "*"}},
		{"decimal", [5]string{"1.5", "*", "*", "*", "*"}},
		{"exponent", [5]string{"1e1", "*", "*", "*", "*"}},
		{"plus sign", [5]string{"+5", "*", "*", "*", "*"}},
		{"empty", [5]string{"", "*", "*", "*", "*"}},
		{"space", [5]string{"1 2", "*", "*", "*", "*"}},
		{"leading space", [5]string{" 5", "*", "*", "*", "*"}},
		{"newline", [5]string{"5\n", "*", "*", "*", "*"}},
		{"semicolon", [5]string{"5;", "*", "*", "*", "*"}},
		{"non-ASCII letter", [5]string{"*", "*", "*", "*", "hétfő"}},
		{"too long", [5]string{long, "*", "*", "*", "*"}},
		{"empty list item", [5]string{"1,,2", "*", "*", "*", "*"}},
		{"trailing comma", [5]string{"1,", "*", "*", "*", "*"}},
		{"leading comma", [5]string{",1", "*", "*", "*", "*"}},
		{"zero step", [5]string{"*/0", "*", "*", "*", "*"}},
		{"step as large as the field", [5]string{"*/59", "*", "*", "*", "*"}},
		{"step larger than the field", [5]string{"*/60", "*", "*", "*", "*"}},
		{"hour step too large", [5]string{"*", "*/24", "*", "*", "*"}},
		{"month step too large", [5]string{"*", "*", "*", "*/12", "*"}},
		{"weekday step too large", [5]string{"*", "*", "*", "*", "*/7"}},
		{"empty step", [5]string{"*/", "*", "*", "*", "*"}},
		{"no base", [5]string{"/5", "*", "*", "*", "*"}},
		{"negative step", [5]string{"*/-2", "*", "*", "*", "*"}},
		{"two steps", [5]string{"*/2/3", "*", "*", "*", "*"}},
		{"number with a step", [5]string{"5/15", "*", "*", "*", "*"}},
		{"names with a step", [5]string{"*", "*", "*", "JAN-JUN/2", "*"}},
		{"backwards range", [5]string{"5-1", "*", "*", "*", "*"}},
		{"backwards range in a list", [5]string{"3,22-2", "*", "*", "*", "*"}},
		{"backwards range with a step", [5]string{"20-10/2", "*", "*", "*", "*"}},
		{"star in a range", [5]string{"*-5", "*", "*", "*", "*"}},
		{"star at the end of a range", [5]string{"1-*", "*", "*", "*", "*"}},
		{"three-part range", [5]string{"1-5-7", "*", "*", "*", "*"}},
		{"unknown month name", [5]string{"*", "*", "*", "FOO", "*"}},
		{"unknown weekday name", [5]string{"*", "*", "*", "*", "FUNDAY"}},
		{"month name in the weekday", [5]string{"*", "*", "*", "*", "JAN"}},
		{"L in the minute", [5]string{"L", "*", "*", "*", "*"}},
		{"? in the minute", [5]string{"?", "*", "*", "*", "*"}},
		{"? in the hour", [5]string{"*", "?", "*", "*", "*"}},
		{"? in the month", [5]string{"*", "*", "*", "?", "*"}},
		{"? in a list", [5]string{"*", "*", "?,5", "*", "*"}},
		{"? in both day fields", [5]string{"*", "*", "?", "*", "?"}},
		{"L in a list", [5]string{"*", "*", "L,5", "*", "*"}},
		{"W in a list", [5]string{"*", "*", "15W,20", "*", "*"}},
		{"L with a number", [5]string{"*", "*", "5L", "*", "*"}},
		{"W on its own", [5]string{"*", "*", "W", "*", "*"}},
		{"W with zero", [5]string{"*", "*", "0W", "*", "*"}},
		{"W out of range", [5]string{"*", "*", "32W", "*", "*"}},
		{"W on a range", [5]string{"*", "*", "1-5W", "*", "*"}},
		{"W on a star", [5]string{"*", "*", "*W", "*", "*"}},
		{"lower-case w", [5]string{"*", "*", "15w", "*", "*"}},
		{"lower-case l", [5]string{"*", "*", "*", "*", "5l"}},
		{"weekday L alone", [5]string{"*", "*", "*", "*", "L"}},
		{"weekday L with a range", [5]string{"*", "*", "*", "*", "1-5L"}},
		{"occurrence 0", [5]string{"*", "*", "*", "*", "5#0"}},
		{"occurrence 6", [5]string{"*", "*", "*", "*", "5#6"}},
		{"occurrence missing", [5]string{"*", "*", "*", "*", "5#"}},
		{"occurrence without weekday", [5]string{"*", "*", "*", "*", "#2"}},
		{"occurrence with a second hash", [5]string{"*", "*", "*", "*", "1#2#3"}},
		{"occurrence on a bad weekday", [5]string{"*", "*", "*", "*", "8#2"}},
		{"too many digits", [5]string{"*", "*", "*", "*", "0005"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.f[0], tt.f[1], tt.f[2], tt.f[3], tt.f[4])
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("Parse(%q) error = %v, want ErrInvalid", tt.f, err)
			}
		})
	}
}

func TestParseErrorsNameTheField(t *testing.T) {
	tests := []struct {
		f    [5]string
		want string
	}{
		{[5]string{"60", "*", "*", "*", "*"}, "minute"},
		{[5]string{"*", "24", "*", "*", "*"}, "hour"},
		{[5]string{"*", "*", "32", "*", "*"}, "day of month"},
		{[5]string{"*", "*", "*", "13", "*"}, "month"},
		{[5]string{"*", "*", "*", "*", "8"}, "day of week"},
	}
	for _, tt := range tests {
		_, err := Parse(tt.f[0], tt.f[1], tt.f[2], tt.f[3], tt.f[4])
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("error = %v, want it to name the %s field", err, tt.want)
		}
	}
}

func TestErrInvalidMessage(t *testing.T) {
	if got := ErrInvalid.Error(); got != "the cron data provided does not evaluate to a valid expression" {
		t.Errorf("ErrInvalid = %q: it is the sentence the reference shows", got)
	}
}
