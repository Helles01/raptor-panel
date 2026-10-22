package backups

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func at(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }

func TestAdmit(t *testing.T) {
	three := []Existing{{ID: 1, CreatedAt: at(0)}, {ID: 2, CreatedAt: at(10)}, {ID: 3, CreatedAt: at(20)}}
	oldestLocked := []Existing{{ID: 1, CreatedAt: at(0), Locked: true}, {ID: 2, CreatedAt: at(10)}, {ID: 3, CreatedAt: at(20)}}
	allLocked := []Existing{{ID: 1, CreatedAt: at(0), Locked: true}, {ID: 2, CreatedAt: at(10), Locked: true}}
	unordered := []Existing{{ID: 3, CreatedAt: at(20)}, {ID: 1, CreatedAt: at(0)}, {ID: 2, CreatedAt: at(10)}}
	tied := []Existing{{ID: 9, CreatedAt: at(0)}, {ID: 4, CreatedAt: at(0)}, {ID: 7, CreatedAt: at(0)}}

	tests := []struct {
		name     string
		limit    *int64
		existing []Existing
		override bool
		want     Admission
		tooMany  int64 // the limit in the error, -1 for no error
	}{
		{"room left", ptr(int64(5)), three, false, Admission{}, -1},
		{"room left for a schedule too", ptr(int64(5)), three, true, Admission{}, -1},
		{"no backups yet", ptr(int64(1)), nil, false, Admission{}, -1},
		{"the last place", ptr(int64(4)), three, false, Admission{}, -1},

		{"at the limit", ptr(int64(3)), three, false, Admission{}, 3},
		{"at the limit, replacing the oldest", ptr(int64(3)), three, true, Admission{Prune: true, PruneID: 1}, -1},
		{"the oldest is locked", ptr(int64(3)), oldestLocked, true, Admission{Prune: true, PruneID: 2}, -1},
		{"everything is locked", ptr(int64(2)), allLocked, true, Admission{}, 2},
		{"the oldest by time, not by position", ptr(int64(3)), unordered, true, Admission{Prune: true, PruneID: 1}, -1},
		{"the lowest id wins a tie", ptr(int64(3)), tied, true, Admission{Prune: true, PruneID: 4}, -1},
		{"over the limit, one is replaced", ptr(int64(2)), three, true, Admission{Prune: true, PruneID: 1}, -1},
		{"over the limit, no override", ptr(int64(2)), three, false, Admission{}, 2},

		{"limit zero", ptr(int64(0)), nil, false, Admission{}, 0},
		{"limit zero, replacing", ptr(int64(0)), three, true, Admission{}, 0},
		{"no limit set is the same as zero", nil, nil, false, Admission{}, 0},
		{"no limit set, replacing", nil, three, true, Admission{}, 0},
		{"a negative limit is zero", ptr(int64(-3)), three, true, Admission{}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Admit(tt.limit, tt.existing, tt.override)
			var tooMany *TooManyError
			switch {
			case tt.tooMany < 0 && err != nil:
				t.Fatalf("error = %v", err)
			case tt.tooMany >= 0 && (!errors.As(err, &tooMany) || tooMany.Limit != tt.tooMany):
				t.Fatalf("error = %v, want a TooManyError with limit %d", err, tt.tooMany)
			}
			if got != tt.want {
				t.Errorf("Admit = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTooManyErrorText(t *testing.T) {
	if got := (&TooManyError{Limit: 3}).Error(); got != "Cannot create a new backup, this server has reached its limit of 3 backups." {
		t.Errorf("text = %q", got)
	}
}

func TestThrottle(t *testing.T) {
	period := 10 * time.Minute
	tests := []struct {
		name    string
		started []time.Time
		limit   int
		period  time.Duration
		limited bool
		retry   time.Duration
	}{
		// now is at(30), so the window starts at at(20).
		{"nothing started", nil, 2, period, false, 0},
		{"below the limit", []time.Time{at(25)}, 2, period, false, 0},
		{"old ones do not count", []time.Time{at(0), at(5), at(19)}, 2, period, false, 0},
		{"one old and one new", []time.Time{at(5), at(25)}, 2, period, false, 0},
		{"the start of the window counts", []time.Time{at(20), at(25)}, 2, period, true, 0},
		{"just before the window does not", []time.Time{at(19), at(25)}, 2, period, false, 0},
		{"at the limit: wait until the oldest leaves", []time.Time{at(22), at(25)}, 2, period, true, 2 * time.Minute},
		{"over the limit: wait until enough have left", []time.Time{at(21), at(24), at(27)}, 2, period, true, 4 * time.Minute},
		{"input order does not matter", []time.Time{at(27), at(21), at(24)}, 2, period, true, 4 * time.Minute},
		{"the throttle is off with period 0", []time.Time{at(29), at(29), at(29)}, 2, 0, false, 0},
		{"a negative period is off too", []time.Time{at(29), at(29), at(29)}, 2, -time.Minute, false, 0},
		{"limit zero blocks everything", nil, 0, period, true, period},
		{"limit one", []time.Time{at(28)}, 1, period, true, 8 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Throttle(tt.started, at(30), tt.limit, tt.period)
			var th *ThrottleError
			if !tt.limited {
				if err != nil {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if !errors.As(err, &th) {
				t.Fatalf("error = %v, want a ThrottleError", err)
			}
			if th.RetryAfter != tt.retry || th.Limit != tt.limit || th.Period != tt.period {
				t.Errorf("error = %+v, want retry %v", th, tt.retry)
			}
		})
	}
}

func TestThrottleErrorTextAndSeconds(t *testing.T) {
	e := &ThrottleError{Limit: 2, Period: 600 * time.Second, RetryAfter: 90*time.Second + time.Millisecond}
	if got := e.Error(); got != "Only 2 backups may be generated within a 600 second span of time." {
		t.Errorf("text = %q", got)
	}
	for in, want := range map[time.Duration]int{0: 1, time.Millisecond: 1, time.Second: 1, 1001 * time.Millisecond: 2, 2 * time.Minute: 120} {
		if got := (&ThrottleError{RetryAfter: in}).RetryAfterSeconds(); got != want {
			t.Errorf("RetryAfterSeconds(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"plain", "Before the update", "Before the update", true},
		{"trimmed", "  \t Nightly \n", "Nightly", true},
		{"empty gets the default", "", "Backup at 2026-03-10 12:00:00", true},
		{"spaces only get the default", " \t\n ", "Backup at 2026-03-10 12:00:00", true},
		{"at the limit", strings.Repeat("a", 191), strings.Repeat("a", 191), true},
		{"multi-byte characters count once", strings.Repeat("é", 191), strings.Repeat("é", 191), true},
		{"too long", strings.Repeat("a", 192), "", false},
		{"control character", "a\x01b", "", false},
		{"newline inside", "a\nb", "", false},
		{"invalid UTF-8", "a\xffb", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Name(tt.in, base)
			if (err == nil) != tt.ok || got != tt.want {
				t.Errorf("Name(%q) = %q, %v; want %q, ok=%v", tt.in, got, err, tt.want, tt.ok)
			}
		})
	}
}

func TestParseIgnored(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
		ok   bool
	}{
		{"empty", "", []string{}, true},
		{"one", "*.log", []string{"*.log"}, true},
		{"several", "*.log\ncache/\n!keep.txt", []string{"*.log", "cache/", "!keep.txt"}, true},
		{"empty lines are dropped", "\n\n*.log\n\n\ncache/\n", []string{"*.log", "cache/"}, true},
		{"carriage returns are not part of the pattern", "*.log\r\ncache/\r\n", []string{"*.log", "cache/"}, true},
		{"a line of only a carriage return is empty", "\r\n\r\n", []string{}, true},
		{"spaces are kept", "  my file.txt", []string{"  my file.txt"}, true},
		{"a pattern with a slash and wildcards", "world/**/region/*.mca", []string{"world/**/region/*.mca"}, true},

		{"NUL", "a\x00b", nil, false},
		{"control character", "a\x07b", nil, false},
		{"a carriage return inside a line", "a\rb", nil, false},
		{"invalid UTF-8", "\xff", nil, false},
		{"a pattern that is too long", strings.Repeat("a", 1025), nil, false},
		{"a pattern at the limit", strings.Repeat("a", 1024), []string{strings.Repeat("a", 1024)}, true},
		{"too many patterns", strings.Repeat("a\n", 501), nil, false},
		{"as many patterns as allowed", strings.Repeat("a\n", 500), strings.Split(strings.TrimSuffix(strings.Repeat("a\n", 500), "\n"), "\n"), true},
		{"too many bytes", strings.Repeat(strings.Repeat("a", 100)+"\n", 400), nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseIgnored(tt.in)
			if (err == nil) != tt.ok {
				t.Fatalf("error = %v, want ok=%v", err, tt.ok)
			}
			if tt.ok && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseIgnored = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidDisk(t *testing.T) {
	for disk, want := range map[string]bool{"wings": true, "s3": true, "": false, "S3": false, "local": false, "ftp": false} {
		if ValidDisk(disk) != want {
			t.Errorf("ValidDisk(%q) = %v", disk, !want)
		}
	}
}

func TestStateRules(t *testing.T) {
	done := State{Completed: true, Successful: true}
	lockedDone := State{Completed: true, Successful: true, Locked: true}
	running := State{}
	lockedRunning := State{Locked: true}
	failed := State{Completed: true}
	lockedFailed := State{Completed: true, Locked: true}

	del := []struct {
		name string
		s    State
		want error
	}{
		{"a finished backup", done, nil},
		{"a locked finished backup", lockedDone, ErrLocked},
		{"a running backup", running, nil},
		{"a locked running backup", lockedRunning, nil},
		{"a failed backup", failed, nil},
		{"a locked failed backup", lockedFailed, nil},
	}
	for _, tt := range del {
		if err := tt.s.CanDelete(); !errors.Is(err, tt.want) {
			t.Errorf("CanDelete, %s: %v, want %v", tt.name, err, tt.want)
		}
	}

	restore := []struct {
		name   string
		s      State
		status string
		want   error
	}{
		{"a finished backup on a normal server", done, "", nil},
		{"a locked finished backup on a normal server", lockedDone, "", nil},
		{"installing", done, "installing", ErrServerBusy},
		{"suspended", done, "suspended", ErrServerBusy},
		{"already restoring", done, "restoring_backup", ErrServerBusy},
		{"a running backup", running, "", ErrNotRestorable},
		{"a failed backup", failed, "", ErrNotRestorable},
		{"a backup marked successful but never completed", State{Successful: true}, "", ErrNotRestorable},
		{"the server state is checked first", failed, "installing", ErrServerBusy},
	}
	for _, tt := range restore {
		if err := tt.s.CanRestore(tt.status); !errors.Is(err, tt.want) {
			t.Errorf("CanRestore, %s: %v, want %v", tt.name, err, tt.want)
		}
	}

	download := []struct {
		name string
		s    State
		want error
	}{
		{"a finished backup", done, nil},
		{"a locked finished backup", lockedDone, nil},
		{"a running backup", running, ErrNotDownloadable},
		{"a failed backup", failed, ErrNotDownloadable},
		{"never completed", State{Successful: true}, ErrNotDownloadable},
	}
	for _, tt := range download {
		if err := tt.s.CanDownload(); !errors.Is(err, tt.want) {
			t.Errorf("CanDownload, %s: %v, want %v", tt.name, err, tt.want)
		}
	}
}

func TestComplete(t *testing.T) {
	good := Report{Successful: true, ChecksumType: "sha1", Checksum: "da39a3ee5e6b4b0d3255bfef95601890afd80709", Size: 1234}
	with := func(f func(*Report)) Report { r := good; f(&r); return r }

	tests := []struct {
		name    string
		current State
		report  Report
		want    Result
		err     error
		anyErr  bool
	}{
		{"success", State{}, good, Result{Successful: true, Checksum: "sha1:da39a3ee5e6b4b0d3255bfef95601890afd80709", Bytes: 1234}, nil, false},
		{"success keeps the lock", State{Locked: true}, good, Result{Successful: true, Locked: true, Checksum: "sha1:da39a3ee5e6b4b0d3255bfef95601890afd80709", Bytes: 1234}, nil, false},
		{"failure", State{}, Report{Successful: false}, Result{}, nil, false},
		{"failure removes the lock", State{Locked: true}, Report{Successful: false, Checksum: "ignored", Size: 9}, Result{}, nil, false},
		{"zero size is fine", State{}, with(func(r *Report) { r.Size = 0 }), Result{Successful: true, Checksum: "sha1:da39a3ee5e6b4b0d3255bfef95601890afd80709"}, nil, false},
		{"an S3 style checksum", State{}, with(func(r *Report) { r.ChecksumType = "etag"; r.Checksum = "9bb58f26192e4ba00f01e2e7b136bbd8-3" }), Result{Successful: true, Checksum: "etag:9bb58f26192e4ba00f01e2e7b136bbd8-3", Bytes: 1234}, nil, false},

		{"already successful", State{Completed: true, Successful: true}, good, Result{}, ErrAlreadyCompleted, false},
		{"already failed, then reported successful", State{Completed: true}, good, Result{}, ErrAlreadyCompleted, false},
		{"already failed, reported failed again", State{Completed: true}, Report{}, Result{}, ErrAlreadyCompleted, false},

		{"no checksum type", State{}, with(func(r *Report) { r.ChecksumType = "" }), Result{}, nil, true},
		{"a checksum type with a colon", State{}, with(func(r *Report) { r.ChecksumType = "sha1:x" }), Result{}, nil, true},
		{"a checksum type that is too long", State{}, with(func(r *Report) { r.ChecksumType = strings.Repeat("a", 17) }), Result{}, nil, true},
		{"no checksum", State{}, with(func(r *Report) { r.Checksum = "" }), Result{}, nil, true},
		{"a checksum with a space", State{}, with(func(r *Report) { r.Checksum = "ab cd" }), Result{}, nil, true},
		{"a checksum with a newline", State{}, with(func(r *Report) { r.Checksum = "abcd\n" }), Result{}, nil, true},
		{"a checksum with a quote", State{}, with(func(r *Report) { r.Checksum = `"abcd"` }), Result{}, nil, true},
		{"a checksum that is too long", State{}, with(func(r *Report) { r.Checksum = strings.Repeat("a", 201) }), Result{}, nil, true},
		{"a negative size", State{}, with(func(r *Report) { r.Size = -1 }), Result{}, nil, true},
		{"a size beyond a petabyte", State{}, with(func(r *Report) { r.Size = MaxSize + 1 }), Result{}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Complete(tt.current, tt.report)
			switch {
			case tt.err != nil && !errors.Is(err, tt.err):
				t.Fatalf("error = %v, want %v", err, tt.err)
			case tt.anyErr && err == nil:
				t.Fatal("want an error")
			case tt.err == nil && !tt.anyErr && err != nil:
				t.Fatalf("error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Complete = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestChecksumColumnLimit(t *testing.T) {
	// 16 + 1 + 200 is 217, within the 255 of the column, so the column check cannot trigger with these two patterns; it is
	// there for the day a pattern is widened.
	r := Report{Successful: true, ChecksumType: strings.Repeat("a", 16), Checksum: strings.Repeat("b", 200), Size: 1}
	got, err := Complete(State{}, r)
	if err != nil || len(got.Checksum) != 217 {
		t.Errorf("a longest checksum: %v, %d", err, len(got.Checksum))
	}
}
