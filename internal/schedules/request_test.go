package schedules

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/fielderr"
)

func ptrBool(b bool) *bool { return &b }

func input() Input {
	return Input{Name: "Nightly restart", Minute: "30", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*"}
}

func TestPlanCreate(t *testing.T) {
	in := input()
	in.Active = ptrBool(true)
	in.OnlyWhenOnline = ptrBool(true)

	got, err := PlanCreate(in, t0, time.UTC)
	if err != nil {
		t.Fatalf("PlanCreate: %v", err)
	}

	want := Plan{
		Name: "Nightly restart", Minute: "30", Hour: "3", DayOfMonth: "*", Month: "*", DayOfWeek: "*",
		Active: true, OnlyWhenOnline: true, NextRunAt: time.Date(2026, 10, 24, 3, 30, 0, 0, time.UTC),
	}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestPlanCreateFlagsDefaultToOff(t *testing.T) {
	got, err := PlanCreate(input(), t0, time.UTC)

	if err != nil || got.Active || got.OnlyWhenOnline {
		t.Errorf("got %+v, %v; a flag that was not sent is off", got, err)
	}
	if got.ResetProcessing {
		t.Error("a new schedule has no running mark to clear")
	}
}

func TestPlanCreateReadsTheExpressionInTheZoneOfThePanel(t *testing.T) {
	budapest, err := time.LoadLocation("Europe/Budapest")
	if err != nil {
		t.Skipf("no time zone data: %v", err)
	}
	// 12:00 UTC is 14:00 in Budapest in October (UTC+2), so 03:30 local is the next day, 01:30 UTC.
	got, err := PlanCreate(input(), t0, budapest)

	want := time.Date(2026, 10, 24, 3, 30, 0, 0, budapest)
	if err != nil || !got.NextRunAt.Equal(want) || got.NextRunAt.Location() != budapest {
		t.Errorf("NextRunAt = %v, %v; want %v in the panel's zone", got.NextRunAt, err, want)
	}
}

func TestPlanCreateWithoutAZone(t *testing.T) {
	got, err := PlanCreate(input(), t0, nil)

	if err != nil || got.NextRunAt.Location() != time.UTC {
		t.Errorf("NextRunAt = %v, %v; want UTC", got.NextRunAt, err)
	}
}

func TestPlanCreateRefusals(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Input)
		field  string
	}{
		{"no name", func(in *Input) { in.Name = "" }, "name"},
		{"name too long", func(in *Input) { in.Name = strings.Repeat("a", 192) }, "name"},
		{"name with a control character", func(in *Input) { in.Name = "a\x07" }, "name"},
		{"no minute", func(in *Input) { in.Minute = "" }, "minute"},
		{"blank hour", func(in *Input) { in.Hour = "  " }, "hour"},
		{"no day of month", func(in *Input) { in.DayOfMonth = "" }, "day_of_month"},
		{"no month", func(in *Input) { in.Month = "" }, "month"},
		{"no day of week", func(in *Input) { in.DayOfWeek = "" }, "day_of_week"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := input()
			tt.mutate(&in)

			got, err := PlanCreate(in, t0, time.UTC)

			if fe, ok := fielderr.Of(err); !ok || fe.Field != tt.field {
				t.Fatalf("error = %v, want one about %q", err, tt.field)
			}
			if got != (Plan{}) {
				t.Errorf("a refused schedule must plan nothing: %+v", got)
			}
		})
	}
}

func TestPlanCreateWithAnExpressionThatIsNotOne(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Input)
	}{
		{"minute out of range", func(in *Input) { in.Minute = "60" }},
		{"hour out of range", func(in *Input) { in.Hour = "24" }},
		{"a word", func(in *Input) { in.Minute = "often" }},
		{"a backwards range", func(in *Input) { in.Hour = "5-2" }},
		{"a date that does not exist", func(in *Input) { in.DayOfMonth, in.Month = "31", "2" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := input()
			tt.mutate(&in)

			got, err := PlanCreate(in, t0, time.UTC)

			if !errors.Is(err, ErrCron) || got != (Plan{}) {
				t.Errorf("got %+v, %v, want ErrCron", got, err)
			}
		})
	}
}

func TestPlanUpdateKeepsTheFlagsThatWereNotSent(t *testing.T) {
	got, err := PlanUpdate(Current{Active: true, OnlyWhenOnline: true}, input(), t0, time.UTC)
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}

	if !got.Active || !got.OnlyWhenOnline || got.ResetProcessing {
		t.Errorf("got %+v, want both flags kept and no reset", got)
	}
}

func TestPlanUpdateChangesFlags(t *testing.T) {
	tests := []struct {
		name      string
		cur       Current
		active    *bool
		online    *bool
		wantA     bool
		wantO     bool
		wantReset bool
	}{
		{"switched off", Current{Active: true}, ptrBool(false), nil, false, false, true},
		{"switched on", Current{Active: false}, ptrBool(true), nil, true, false, true},
		{"on stays on", Current{Active: true}, ptrBool(true), nil, true, false, false},
		{"off stays off", Current{Active: false}, ptrBool(false), nil, false, false, false},
		{"online setting on", Current{Active: true}, nil, ptrBool(true), true, true, false},
		{"online setting off", Current{Active: true, OnlyWhenOnline: true}, nil, ptrBool(false), true, false, false},
		{"both", Current{}, ptrBool(true), ptrBool(true), true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := input()
			in.Active, in.OnlyWhenOnline = tt.active, tt.online

			got, err := PlanUpdate(tt.cur, in, t0, time.UTC)
			if err != nil {
				t.Fatalf("PlanUpdate: %v", err)
			}

			if got.Active != tt.wantA || got.OnlyWhenOnline != tt.wantO || got.ResetProcessing != tt.wantReset {
				t.Errorf("got active %v online %v reset %v; want %v %v %v", got.Active, got.OnlyWhenOnline, got.ResetProcessing, tt.wantA, tt.wantO, tt.wantReset)
			}
		})
	}
}

func TestPlanUpdateSetsTheNextTimeAlsoForASwitchedOffSchedule(t *testing.T) {
	in := input()
	in.Active = ptrBool(false)

	got, err := PlanUpdate(Current{Active: true}, in, t0, time.UTC)

	if err != nil || got.NextRunAt.IsZero() {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestPlanUpdateRefusals(t *testing.T) {
	in := input()
	in.Minute = "99"
	if got, err := PlanUpdate(Current{Active: true}, in, t0, time.UTC); !errors.Is(err, ErrCron) || got != (Plan{}) {
		t.Errorf("got %+v, %v, want ErrCron and nothing planned", got, err)
	}

	in = input()
	in.Name = ""
	if got, err := PlanUpdate(Current{Active: true}, in, t0, time.UTC); err == nil || got.ResetProcessing || got != (Plan{}) {
		t.Errorf("got %+v, %v, want the name refused", got, err)
	}
}

func TestErrCronKeepsTheReferencesText(t *testing.T) {
	if !strings.Contains(ErrCron.Error(), "does not evaluate to a valid expression") {
		t.Errorf("ErrCron = %q", ErrCron)
	}
}
