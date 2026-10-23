package schedules

import (
	"errors"
	"strings"
	"time"

	"github.com/Helles01/raptor-panel/internal/fielderr"
)

// ErrCron is a schedule whose five fields are not a cron expression that ever runs. The text is the reference's, and the API
// answers it with 400.
var ErrCron = errors.New("The cron data provided does not evaluate to a valid expression.") //nolint:staticcheck // the reference's text

// Input is the body of a request to create or change a schedule. The name and the five cron fields are needed. The two flags are
// nil when they were not sent.
type Input struct {
	Name           string
	Minute         string
	Hour           string
	DayOfMonth     string
	Month          string
	DayOfWeek      string
	Active         *bool
	OnlyWhenOnline *bool
}

// Current is what a change needs to know of the stored schedule.
type Current struct {
	Active         bool
	OnlyWhenOnline bool
}

// Plan is a schedule that may be stored.
type Plan struct {
	Name           string
	Minute         string
	Hour           string
	DayOfMonth     string
	Month          string
	DayOfWeek      string
	Active         bool
	OnlyWhenOnline bool
	// NextRunAt is the next time the expression runs after now, in the panel's time zone. It is set also for a schedule that is
	// switched off, as in the reference.
	NextRunAt time.Time
	// ResetProcessing clears the running mark: switching a schedule on or off is how an owner clears one that is stuck.
	ResetProcessing bool
}

// PlanCreate checks a new schedule. A flag that was not sent is off, as in the reference: the schedule is made but does not run
// until it is switched on, so a request that forgets the flag does not start something by surprise. now is the time of the
// request and loc the panel's time zone, which the expression is read in.
func PlanCreate(in Input, now time.Time, loc *time.Location) (Plan, error) {
	return plan(in, Current{}, now, loc)
}

// PlanUpdate checks a change of a schedule. A flag that was not sent keeps its value; the reference switches the schedule off
// and the online setting off when they are left out (C-052).
func PlanUpdate(cur Current, in Input, now time.Time, loc *time.Location) (Plan, error) {
	p, err := plan(in, cur, now, loc)
	if err != nil {
		return Plan{}, err
	}
	p.ResetProcessing = p.Active != cur.Active
	return p, nil
}

func plan(in Input, cur Current, now time.Time, loc *time.Location) (Plan, error) {
	if err := ValidateName(in.Name); err != nil {
		return Plan{}, err
	}
	for _, f := range []struct{ name, value string }{
		{"minute", in.Minute}, {"hour", in.Hour}, {"day_of_month", in.DayOfMonth}, {"month", in.Month}, {"day_of_week", in.DayOfWeek},
	} {
		if strings.TrimSpace(f.value) == "" {
			return Plan{}, fielderr.New(f.name, "is required")
		}
	}

	c, err := Parse(in.Minute, in.Hour, in.DayOfMonth, in.Month, in.DayOfWeek)
	if err != nil {
		return Plan{}, ErrCron
	}
	if loc == nil {
		loc = time.UTC
	}
	next, err := c.Next(now.In(loc))
	if err != nil {
		return Plan{}, ErrCron
	}

	p := Plan{
		Name: in.Name, Minute: in.Minute, Hour: in.Hour, DayOfMonth: in.DayOfMonth, Month: in.Month, DayOfWeek: in.DayOfWeek,
		Active: cur.Active, OnlyWhenOnline: cur.OnlyWhenOnline, NextRunAt: next,
	}
	if in.Active != nil {
		p.Active = *in.Active
	}
	if in.OnlyWhenOnline != nil {
		p.OnlyWhenOnline = *in.OnlyWhenOnline
	}
	return p, nil
}
