package app

import (
	"fmt"
	"time"

	"github.com/Helles01/raptor-panel/internal/api/allocapi"
	"github.com/Helles01/raptor-panel/internal/api/backupapi"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/databaseapi"
	"github.com/Helles01/raptor-panel/internal/api/fileapi"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/api/scheduleapi"
	"github.com/Helles01/raptor-panel/internal/api/subuserapi"
	"github.com/Helles01/raptor-panel/internal/auth/passwordreset"
)

// The limits that are not the business of one endpoint package. The reference limits both APIs to 240 requests a minute for each user.
const (
	APILimit      = 240
	APIWindow     = time.Minute
	LoginLimit    = 10
	LoginWindow   = time.Minute
	RequestLimit  = 2
	RequestWindow = time.Minute
	ResetLimit    = 10
	ResetWindow   = time.Minute
	EmailLimit    = passwordreset.RequestPerEmailPerHour
	EmailWindow   = time.Hour
	LocaleLimit   = 60
	LocaleWindow  = time.Minute
	HealthLimit   = 120
	HealthWindow  = time.Minute
)

// Limits are all the limiters of the panel, made in one place so that the numbers can be read together. Each is memory of this process
// only (C-058); a panel run as several processes allows each its own share.
type Limits struct {
	// The two APIs, by user (authz.RateKey), applied after the authorizer.
	ClientAPI, ApplicationAPI *middleware.Limiter
	// The sign-in pages, by client address.
	Login, Request, Reset, Email *middleware.Limiter
	// Routes of one server (each counts by server).
	AllocationAdd, BackupRestore, Websocket, DatabaseCreate, FilePull, ScheduleCreate, SubuserCreate *middleware.Limiter
	// Open pages, by client address.
	Locale, Health *middleware.Limiter
}

// NewLimits makes the limiters.
func NewLimits() (Limits, error) {
	var l Limits
	for _, n := range []struct {
		dst    **middleware.Limiter
		name   string
		limit  int
		window time.Duration
	}{
		{&l.ClientAPI, "client API", APILimit, APIWindow},
		{&l.ApplicationAPI, "application API", APILimit, APIWindow},
		{&l.Login, "login", LoginLimit, LoginWindow},
		{&l.Request, "password link", RequestLimit, RequestWindow},
		{&l.Reset, "password reset", ResetLimit, ResetWindow},
		{&l.Email, "password link per address", EmailLimit, EmailWindow},
		{&l.AllocationAdd, "allocation add", allocapi.AddLimit, allocapi.AddWindow},
		{&l.BackupRestore, "backup restore", backupapi.RestoreLimit, backupapi.RestoreWindow},
		{&l.Websocket, "websocket", controlapi.WebsocketLimit, controlapi.WebsocketWindow},
		{&l.DatabaseCreate, "database create", databaseapi.CreateLimit, databaseapi.CreateWindow},
		{&l.FilePull, "file pull", fileapi.PullLimit, fileapi.PullWindow},
		{&l.ScheduleCreate, "schedule create", scheduleapi.CreateLimit, scheduleapi.CreateWindow},
		{&l.SubuserCreate, "subuser create", subuserapi.CreateLimit, subuserapi.CreateWindow},
		{&l.Locale, "locale", LocaleLimit, LocaleWindow},
		{&l.Health, "health", HealthLimit, HealthWindow},
	} {
		limiter, err := middleware.NewLimiter(n.limit, n.window)
		if err != nil {
			return Limits{}, fmt.Errorf("the %s limiter: %w", n.name, err)
		}
		*n.dst = limiter
	}
	return l, nil
}
