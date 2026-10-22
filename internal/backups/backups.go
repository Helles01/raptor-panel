// Package backups holds the rules of server backups that need no database: how many a server may have and when the oldest
// is replaced, the creation throttle, names and ignored files, and what each state of a backup allows.
package backups

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// MaxNameLength is the longest a backup name may be, in characters.
	MaxNameLength = 191

	// The limits of the list of files a backup leaves out. Wings reads each line as a pattern.
	MaxIgnoredEntries     = 500
	MaxIgnoredEntryLength = 1024
	MaxIgnoredBytes       = 32768

	// MaxSize is the largest archive size the node may report, in bytes (1 PiB).
	MaxSize = 1 << 50

	maxChecksumColumn = 255
)

// The refusals. The texts a user is shown are those of the reference panel, except where noted.
var (
	// ErrLocked is returned when a locked backup is deleted.
	ErrLocked = errors.New("Cannot delete a backup that is marked as locked.") //nolint:staticcheck // the reference's text
	// ErrServerBusy is returned when a backup is restored on a server that is installing, suspended or restoring.
	ErrServerBusy = errors.New("This server is not currently in a state that allows for a backup to be restored.") //nolint:staticcheck // the reference's text
	// ErrNotRestorable is returned when a backup that is unfinished or failed is restored.
	ErrNotRestorable = errors.New("This backup cannot be restored at this time: not completed or failed.") //nolint:staticcheck // the reference's text
	// ErrNotDownloadable is returned when a backup that is unfinished or failed is downloaded (C-026, Raptor's own text).
	ErrNotDownloadable = errors.New("This backup cannot be downloaded at this time: not completed or failed.") //nolint:staticcheck // same style as the reference's texts
	// ErrAlreadyCompleted is returned when a node reports on a backup that is already finished (C-026).
	ErrAlreadyCompleted = errors.New("Cannot update the status of a backup that is already marked as completed.") //nolint:staticcheck // the reference's text
)

// TooManyError is returned when a server is at its backup limit.
type TooManyError struct{ Limit int64 }

func (e *TooManyError) Error() string {
	return fmt.Sprintf("Cannot create a new backup, this server has reached its limit of %d backups.", e.Limit) //nolint:staticcheck // the reference's text
}

// Existing is a backup that counts towards the limit: one that is still running or finished successfully. Failed backups
// are not passed in.
type Existing struct {
	ID        uint64
	CreatedAt time.Time
	Locked    bool
}

// Admission is the decision to let a new backup start.
type Admission struct {
	// Prune is true when the oldest unlocked backup, PruneID, must be deleted first to make room.
	Prune   bool
	PruneID uint64
}

// Admit decides whether a server may start another backup. limit is the server's backup limit; a server with no limit set,
// like one with 0, may not have backups at all. At the limit, a backup that is meant to replace the oldest (override, used
// by schedules) deletes the oldest unlocked one; locked backups are never replaced. As in the reference only one is
// deleted, even when a server has more than its limit.
//
// The caller must count and create in one transaction with the server's backups locked, or two requests can both see room.
func Admit(limit *int64, existing []Existing, override bool) (Admission, error) {
	var allowed int64
	if limit != nil && *limit > 0 {
		allowed = *limit
	}
	if allowed > 0 && int64(len(existing)) < allowed {
		return Admission{}, nil
	}
	if !override || allowed <= 0 {
		return Admission{}, &TooManyError{Limit: allowed}
	}

	var oldest *Existing
	for i := range existing {
		e := &existing[i]
		if e.Locked {
			continue
		}
		if oldest == nil || e.CreatedAt.Before(oldest.CreatedAt) || (e.CreatedAt.Equal(oldest.CreatedAt) && e.ID < oldest.ID) {
			oldest = e
		}
	}
	if oldest == nil {
		return Admission{}, &TooManyError{Limit: allowed}
	}
	return Admission{Prune: true, PruneID: oldest.ID}, nil
}

// ThrottleError is returned when too many backups were started in a short time.
type ThrottleError struct {
	Limit      int
	Period     time.Duration
	RetryAfter time.Duration
}

func (e *ThrottleError) Error() string {
	return fmt.Sprintf("Only %d backups may be generated within a %d second span of time.", e.Limit, int64(e.Period/time.Second)) //nolint:staticcheck // the reference's text
}

// RetryAfterSeconds is the wait for the Retry-After header: whole seconds, at least 1.
func (e *ThrottleError) RetryAfterSeconds() int {
	s := int((e.RetryAfter + time.Second - 1) / time.Second)
	return max(s, 1)
}

// Throttle checks how often backups are started: at most limit within any period. started holds the creation times of the
// server's backups, deleted and failed ones included, so that deleting and starting again does not get round the limit. A
// period of 0 turns the throttle off. The error says how long to wait; the reference counts from the newest backup in the
// window, Raptor from the one whose expiry makes room (C-027).
func Throttle(started []time.Time, now time.Time, limit int, period time.Duration) error {
	if period <= 0 {
		return nil
	}

	var inWindow []time.Time
	for _, t := range started {
		if !t.Before(now.Add(-period)) {
			inWindow = append(inWindow, t)
		}
	}
	if len(inWindow) < limit {
		return nil
	}

	sort.Slice(inWindow, func(i, j int) bool { return inWindow[i].Before(inWindow[j]) })
	retry := period
	if i := len(inWindow) - limit; limit > 0 && i < len(inWindow) {
		retry = inWindow[i].Add(period).Sub(now)
	}
	return &ThrottleError{Limit: limit, Period: period, RetryAfter: max(retry, 0)}
}

// Name returns the name for a new backup: the given one without surrounding white space, or "Backup at <time>" when it is
// empty. now is the time in the panel's time zone.
func Name(name string, now time.Time) (string, error) {
	name = strings.Trim(name, " \t\n\r\x00\x0b")
	if name == "" {
		return "Backup at " + now.Format("2006-01-02 15:04:05"), nil
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		return "", fmt.Errorf("the name must be at most %d characters", MaxNameLength)
	}
	if !utf8.ValidString(name) || strings.ContainsFunc(name, isControl) {
		return "", errors.New("the name must not contain control characters")
	}
	return name, nil
}

func isControl(r rune) bool { return r < ' ' || r == 0x7f }

// ParseIgnored turns the text of the files to leave out, one pattern per line, into a list: empty lines are dropped and a
// carriage return at the end of a line (text typed on Windows) is not part of the pattern. The same text is the payload of
// a schedule's backup task.
func ParseIgnored(text string) ([]string, error) {
	if len(text) > MaxIgnoredBytes || !utf8.ValidString(text) {
		return nil, fmt.Errorf("the list of ignored files must be valid text of at most %d bytes", MaxIgnoredBytes)
	}

	list := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		if len(line) > MaxIgnoredEntryLength || strings.ContainsFunc(line, isControl) {
			return nil, fmt.Errorf("an ignored file pattern must be at most %d bytes and contain no control characters", MaxIgnoredEntryLength)
		}
		list = append(list, line)
	}
	if len(list) > MaxIgnoredEntries {
		return nil, fmt.Errorf("at most %d ignored file patterns are allowed", MaxIgnoredEntries)
	}
	return list, nil
}

// Disks a backup can be stored on.
const (
	DiskWings = "wings"
	DiskS3    = "s3"
)

// ValidDisk reports whether a stored disk name is one Raptor knows how to download from.
func ValidDisk(disk string) bool { return disk == DiskWings || disk == DiskS3 }

// State is what the rules need to know about a stored backup.
type State struct {
	Completed  bool // it has a completion time: it finished, successfully or not
	Successful bool
	Locked     bool
}

// finished is a backup that completed successfully.
func (s State) finished() bool { return s.Completed && s.Successful }

// CanDelete checks that a backup may be deleted. A locked backup may not, unless it failed: a failed backup cannot be
// unlocked in the interface.
func (s State) CanDelete() error {
	if s.Locked && s.finished() {
		return ErrLocked
	}
	return nil
}

// CanRestore checks that a backup may be restored on a server whose status is serverStatus (empty when it is in its
// normal state).
func (s State) CanRestore(serverStatus string) error {
	if serverStatus != "" {
		return ErrServerBusy
	}
	if !s.finished() {
		return ErrNotRestorable
	}
	return nil
}

// CanDownload checks that a backup may be downloaded. The reference also issues links for backups that are unfinished or
// failed, which the node can only answer with an error (C-026).
func (s State) CanDownload() error {
	if !s.finished() {
		return ErrNotDownloadable
	}
	return nil
}

// Report is what a node tells the panel when a backup ends.
type Report struct {
	Successful   bool
	ChecksumType string
	Checksum     string
	Size         int64
}

// Result is what the panel stores for a finished backup.
type Result struct {
	Successful bool
	// Locked is the lock after the report: a failed backup is always unlocked.
	Locked bool
	// Checksum is "type:value", empty (stored as NULL) for a failed backup.
	Checksum string
	Bytes    int64
}

var (
	checksumTypePattern  = regexp.MustCompile(`^[A-Za-z0-9]{1,16}$`)
	checksumValuePattern = regexp.MustCompile(`^[A-Za-z0-9+/=_.-]{1,200}$`)
)

// Complete applies a node's report to a backup. A backup that already finished, successfully or not, cannot be reported on
// again (C-026; the reference refuses only successful ones, so a failed backup could later be turned into a successful one
// with any checksum). A successful report needs a checksum type, a checksum and a size.
func Complete(current State, r Report) (Result, error) {
	if current.Completed {
		return Result{}, ErrAlreadyCompleted
	}
	if !r.Successful {
		return Result{Successful: false, Locked: false, Bytes: 0}, nil
	}

	if !checksumTypePattern.MatchString(r.ChecksumType) {
		return Result{}, errors.New("checksum_type must be 1 to 16 letters or digits")
	}
	if !checksumValuePattern.MatchString(r.Checksum) {
		return Result{}, errors.New("checksum must be 1 to 200 characters from letters, digits and + / = _ . -")
	}
	if checksum := r.ChecksumType + ":" + r.Checksum; len(checksum) > maxChecksumColumn {
		return Result{}, errors.New("the checksum is too long")
	}
	if r.Size < 0 || r.Size > MaxSize {
		return Result{}, errors.New("size must be between 0 and 1 PiB")
	}
	return Result{Successful: true, Locked: current.Locked, Checksum: r.ChecksumType + ":" + r.Checksum, Bytes: r.Size}, nil
}
