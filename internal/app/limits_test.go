package app

import (
	"reflect"
	"testing"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/middleware"
)

// The numbers of the reference and of the plan, by field, with how long the window is. A limiter that is changed has to be changed here too,
// on purpose.
var wantLimits = map[string]struct {
	limit  int
	window time.Duration
}{
	"ClientAPI": {240, time.Minute}, "ApplicationAPI": {240, time.Minute},
	"Login": {10, time.Minute}, "Request": {2, time.Minute}, "Reset": {10, time.Minute}, "Email": {5, time.Hour},
	"AllocationAdd": {2, time.Minute}, "BackupRestore": {3, 15 * time.Minute}, "Websocket": {5, time.Minute},
	"DatabaseCreate": {2, time.Minute}, "FilePull": {5, 10 * time.Minute}, "ScheduleCreate": {2, time.Minute}, "SubuserCreate": {10, 15 * time.Minute},
	"Locale": {60, time.Minute}, "Health": {120, time.Minute},
}

func TestEveryLimiterIsThereAndHasItsNumber(t *testing.T) {
	l, err := NewLimits()
	if err != nil {
		t.Fatal(err)
	}
	v := reflect.ValueOf(l)
	if v.NumField() != len(wantLimits) {
		t.Fatalf("%d limiters, %d expected", v.NumField(), len(wantLimits))
	}
	seen := map[*middleware.Limiter]string{}
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		want, ok := wantLimits[name]
		if !ok {
			t.Errorf("%s is not in the table of expected limits", name)
			continue
		}
		limiter := v.Field(i).Interface().(*middleware.Limiter)
		if limiter == nil {
			t.Errorf("%s was not made", name)
			continue
		}
		if other, dup := seen[limiter]; dup {
			t.Errorf("%s and %s are the same limiter, so one's use is counted against the other", name, other)
		}
		seen[limiter] = name

		for n := 1; n <= want.limit; n++ {
			if ok, _ := limiter.Allow("key"); !ok {
				t.Errorf("%s refused request %d of %d", name, n, want.limit)
				break
			}
		}
		ok, retry := limiter.Allow("key")
		if ok {
			t.Errorf("%s allowed request %d", name, want.limit+1)
		}
		if retry <= 0 || retry > want.window {
			t.Errorf("%s: retry after %v for a window of %v", name, retry, want.window)
		}
		// Another key has its own share.
		if ok, _ := limiter.Allow("another key"); !ok {
			t.Errorf("%s: a second key was refused", name)
		}
	}
}

func TestLimitersAreIndependent(t *testing.T) {
	l, err := NewLimits()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < LoginLimit+5; i++ {
		l.Login.Allow("203.0.113.9")
	}
	if ok, _ := l.Reset.Allow("203.0.113.9"); !ok {
		t.Error("using the login limit up used the reset limit up")
	}
	if ok, _ := l.ClientAPI.Allow("203.0.113.9"); !ok {
		t.Error("using the login limit up used the API limit up")
	}
}
