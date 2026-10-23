package middleware

import (
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/auth/lockout"
)

func trackerAt(now *time.Time) *Tracker {
	t := NewTracker()
	t.now = func() time.Time { return *now }
	return t
}

func TestTrackerBlocksAfterTheLimit(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	tr := trackerAt(&now)
	c := []lockout.Counter{{Key: "a", Limit: 2, Window: time.Minute}}

	if b, _ := tr.Blocked(c); b {
		t.Fatal("blocked before any failure")
	}
	tr.Fail(c)
	if b, _ := tr.Blocked(c); b {
		t.Fatal("blocked after one failure of two")
	}
	tr.Fail(c)
	b, retry := tr.Blocked(c)
	if !b || retry != time.Minute {
		t.Errorf("Blocked = %v, %v; want blocked for the whole window", b, retry)
	}

	now = now.Add(20 * time.Second)
	if _, retry := tr.Blocked(c); retry != 40*time.Second {
		t.Errorf("retry = %v, want 40s", retry)
	}
	now = now.Add(40 * time.Second)
	if b, _ := tr.Blocked(c); b {
		t.Error("still blocked after the window")
	}
}

func TestTrackerKeysAreSeparate(t *testing.T) {
	now := time.Now()
	tr := trackerAt(&now)
	a := []lockout.Counter{{Key: "a", Limit: 1, Window: time.Minute}}
	b := []lockout.Counter{{Key: "b", Limit: 1, Window: time.Minute}}

	tr.Fail(a)

	if blocked, _ := tr.Blocked(a); !blocked {
		t.Error("a must be blocked")
	}
	if blocked, _ := tr.Blocked(b); blocked {
		t.Error("b must not be blocked by the failures of a")
	}
}

func TestTrackerSameKeyWithDifferentLimitsIsCountedApart(t *testing.T) {
	now := time.Now()
	tr := trackerAt(&now)
	strict := []lockout.Counter{{Key: "k", Limit: 1, Window: time.Minute}}
	loose := []lockout.Counter{{Key: "k", Limit: 5, Window: 10 * time.Minute}}

	tr.Fail(strict)

	if b, _ := tr.Blocked(loose); b {
		t.Error("a counter with another limit and window has its own count")
	}
}

func TestTrackerWithSeveralCounters(t *testing.T) {
	now := time.Now()
	tr := trackerAt(&now)
	cs := []lockout.Counter{
		{Key: "one", Limit: 1, Window: time.Minute},
		{Key: "two", Limit: 3, Window: 10 * time.Minute},
		{Key: "three", Limit: 1, Window: 5 * time.Minute},
	}

	tr.Fail(cs)

	b, retry := tr.Blocked(cs)
	if !b || retry != 5*time.Minute {
		t.Errorf("Blocked = %v, %v; want blocked for the longest of the blocked counters (5m), not the 10m one that is not blocked", b, retry)
	}
	for i, c := range cs {
		one := []lockout.Counter{c}
		if blocked, _ := tr.Blocked(one); blocked != (i != 1) {
			t.Errorf("counter %s blocked = %v", c.Key, blocked)
		}
	}
}

func TestTrackerCountsOnEveryCounterAtOnce(t *testing.T) {
	now := time.Now()
	tr := trackerAt(&now)
	cs := []lockout.Counter{{Key: "x", Limit: 2, Window: time.Minute}, {Key: "y", Limit: 2, Window: time.Minute}}

	tr.Fail(cs)
	tr.Fail(cs[:1])

	if b, _ := tr.Blocked(cs[:1]); !b {
		t.Error("x failed twice and must be blocked")
	}
	if b, _ := tr.Blocked(cs[1:]); b {
		t.Error("y failed once and must not be blocked")
	}
}

func TestTrackerFailsClosedForACounterThatCannotWork(t *testing.T) {
	tr := NewTracker()

	for _, c := range []lockout.Counter{{Key: "k"}, {Key: "k", Limit: 5}, {Key: "k", Window: time.Minute}, {Key: "k", Limit: -1, Window: time.Minute}} {
		if b, _ := tr.Blocked([]lockout.Counter{c}); !b {
			t.Errorf("%+v must count as blocked", c)
		}
	}
	tr.Fail([]lockout.Counter{{Key: "k"}}) // must not panic
}

func TestTrackerWithNoCounters(t *testing.T) {
	tr := NewTracker()

	if b, retry := tr.Blocked(nil); b || retry != 0 {
		t.Errorf("Blocked(nil) = %v, %v", b, retry)
	}
	tr.Fail(nil)
}
