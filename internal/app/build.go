package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Helles01/raptor-panel/internal/activity"
	"github.com/Helles01/raptor-panel/internal/api"
	"github.com/Helles01/raptor-panel/internal/api/accountapi"
	"github.com/Helles01/raptor-panel/internal/api/allocapi"
	"github.com/Helles01/raptor-panel/internal/api/appallocs"
	"github.com/Helles01/raptor-panel/internal/api/applocations"
	"github.com/Helles01/raptor-panel/internal/api/appnests"
	"github.com/Helles01/raptor-panel/internal/api/appnodes"
	"github.com/Helles01/raptor-panel/internal/api/appserverdbs"
	"github.com/Helles01/raptor-panel/internal/api/appservers"
	"github.com/Helles01/raptor-panel/internal/api/appusers"
	"github.com/Helles01/raptor-panel/internal/api/authapi"
	"github.com/Helles01/raptor-panel/internal/api/authz"
	"github.com/Helles01/raptor-panel/internal/api/backupapi"
	"github.com/Helles01/raptor-panel/internal/api/clientapi"
	"github.com/Helles01/raptor-panel/internal/api/controlapi"
	"github.com/Helles01/raptor-panel/internal/api/databaseapi"
	"github.com/Helles01/raptor-panel/internal/api/fileapi"
	"github.com/Helles01/raptor-panel/internal/api/healthapi"
	"github.com/Helles01/raptor-panel/internal/api/middleware"
	"github.com/Helles01/raptor-panel/internal/api/pageapi"
	"github.com/Helles01/raptor-panel/internal/api/remoteapi"
	"github.com/Helles01/raptor-panel/internal/api/scheduleapi"
	"github.com/Helles01/raptor-panel/internal/api/settingsapi"
	"github.com/Helles01/raptor-panel/internal/api/subuserapi"
	"github.com/Helles01/raptor-panel/internal/auth/csrf"
	"github.com/Helles01/raptor-panel/internal/auth/session"
	"github.com/Helles01/raptor-panel/internal/backups"
	"github.com/Helles01/raptor-panel/internal/config"
	"github.com/Helles01/raptor-panel/internal/dbhosts"
	"github.com/Helles01/raptor-panel/internal/mail"
	"github.com/Helles01/raptor-panel/internal/maintenance"
	"github.com/Helles01/raptor-panel/internal/outbox"
	"github.com/Helles01/raptor-panel/internal/s3"
	"github.com/Helles01/raptor-panel/internal/schedules"
	"github.com/Helles01/raptor-panel/internal/servers"
	"github.com/Helles01/raptor-panel/internal/taskexec"
	"github.com/Helles01/raptor-panel/internal/wings"
)

// The lifetime of a session. The reference's default is 12 hours of idleness (720 minutes); a session also ends a week after it was made, however
// active it is.
const (
	SessionIdle     = 720 * time.Minute
	SessionLifetime = 7 * 24 * time.Hour
)

// The switches of the reference that the configuration has no place for yet, at the values the reference has them. Mail about a finished
// installation goes out for the first installation and for a reinstallation; the activity log shows the entries of administrators; users may make
// databases, on a host picked at random when none is linked to the node; a backup may be started twice in ten minutes; the editor opens files up to
// 4 MiB (fileapi.DefaultMaxEditSize).
const (
	DatabasesEnabled     = true
	DatabasesAllowRandom = true
	HideAdminActivity    = false
	BackupThrottleLimit  = 2
	BackupThrottlePeriod = 600 * time.Second
)

// Input is what a panel is made of.
type Input struct {
	// Config is the validated configuration.
	Config config.Config
	Logger *slog.Logger
	// Stores are the stores of the panel, from the SQL layer. PanelMissing lists the ones the panel needs that are not set.
	Stores Stores
	// Frontend is the build of the frontend.
	Frontend Frontend
	// SecretKey is the secret key of the panel; the key of the anti-CSRF tokens is made from it.
	SecretKey []byte
	// Ready are the checks /readyz makes, by name, for example the database.
	Ready map[string]healthapi.Check
	// Now and Random default to the clock and crypto/rand; they are for tests.
	Now    func() time.Time
	Random io.Reader
}

// Panel is a panel that has been made and not started.
type Panel struct {
	// Handler serves every route.
	Handler http.Handler
	// Loops run beside it: the outbox, the scheduler and the housekeeping.
	Loops []Loop
	// Routes are the routes that were registered, each with the permission it asks for.
	Routes []api.Route
}

// PanelMissing lists the stores of s that a running panel needs and that are not set. The stores that make and change the administrator's things and the
// store of the secret migration are for the command line, and the call to an old node is made here. (The directory of the administrator is read for the
// settings the panel starts with.)
func (s Stores) PanelMissing() []string {
	var out []string
	for _, name := range s.Missing() {
		switch name {
		case "AdminHostStore", "AdminStore", "SecretmigrationStore", "RemoteapiOldNodes":
		default:
			out = append(out, name)
		}
	}
	return out
}

// csrfKey makes the key of the anti-CSRF tokens from the secret key of the panel, so that the two are not the same bytes.
func csrfKey(secret []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte("raptor/csrf-key/v1"))
	return m.Sum(nil)
}

// setting reads a setting that has a value in the database; a setting that is not set is empty.
func setting(ctx context.Context, d interface {
	Setting(ctx context.Context, key string) (string, bool, error)
}, key string) (string, error) {
	v, _, err := d.Setting(ctx, key)
	return v, err
}

// Build makes the panel: the router with every endpoint of every package behind its authorizer, and the loops that run beside it. ctx is the
// context of the process: the schedules that people start by hand run under it, and stop with it.
func Build(ctx context.Context, in Input) (Panel, error) {
	if in.Logger == nil {
		return Panel{}, errors.New("app: building the panel needs a logger")
	}
	if missing := in.Stores.PanelMissing(); len(missing) > 0 {
		return Panel{}, fmt.Errorf("app: the panel is missing stores: %s", strings.Join(missing, ", "))
	}
	if in.Frontend.Shell == nil || in.Frontend.Locales == nil || in.Frontend.Assets == nil {
		return Panel{}, errors.New("app: building the panel needs the frontend")
	}
	if len(in.SecretKey) < 32 {
		return Panel{}, errors.New("app: the secret key must be at least 32 bytes")
	}
	if in.Now == nil {
		in.Now = time.Now
	}

	b := &builder{in: in, st: in.Stores, cfg: in.Config, log: in.Logger}
	if err := b.prepare(ctx); err != nil {
		return Panel{}, err
	}
	if err := b.routes(ctx); err != nil {
		return Panel{}, err
	}
	handler, err := b.srv.Handler()
	if err != nil {
		return Panel{}, fmt.Errorf("app: the router refuses its routes: %w", err)
	}
	loops, err := b.loops(ctx)
	if err != nil {
		return Panel{}, err
	}
	return Panel{Handler: handler, Loops: loops, Routes: b.srv.Router.Routes()}, nil
}

// builder holds the pieces that the endpoints share.
type builder struct {
	in  Input
	st  Stores
	cfg config.Config
	log *slog.Logger

	srv      *api.Server
	limits   Limits
	location *time.Location
	site     mail.Site
	sessions *session.Manager
	protect  *csrf.Protector
	secure   bool
	factory  *wings.Factory
	issuer   *wings.Issuer
	hosts    *dbhosts.Executor
	daemon   appservers.Daemon
	texts    map[string][]string
	// Where backups are kept: archives and uploads are nil unless the S3 settings are given, and disk is where the new ones go.
	archives backupapi.Archives
	uploads  remoteapi.Uploads
	disk     string
	backups  *backupapi.Service
	runner   *schedules.Runner
	err      error
	trackers [3]*middleware.Tracker
}

func (b *builder) prepare(ctx context.Context) (err error) {
	cfg := b.cfg
	if b.location, err = cfg.App.Location(); err != nil {
		return fmt.Errorf("app: the time zone: %w", err)
	}
	if b.limits, err = NewLimits(); err != nil {
		return err
	}
	for i := range b.trackers {
		b.trackers[i] = middleware.NewTracker()
	}
	b.secure = strings.HasPrefix(cfg.App.URL, "https://")

	b.sessions, err = session.NewManager(b.st.SessionStore, session.Options{IdleTimeout: SessionIdle, MaxLifetime: SessionLifetime, Secure: b.secure})
	if err != nil {
		return fmt.Errorf("app: the session manager: %w", err)
	}
	if b.protect, err = csrf.New(csrfKey(b.in.SecretKey)); err != nil {
		return fmt.Errorf("app: the anti-CSRF tokens: %w", err)
	}

	b.factory, err = wings.NewFactory(wings.ClientOptions{
		Timeout: time.Duration(cfg.Wings.TimeoutSeconds) * time.Second, ConnectTimeout: time.Duration(cfg.Wings.ConnectTimeoutSeconds) * time.Second, InsecureTLS: cfg.Wings.InsecureTLS,
	})
	if err != nil {
		return fmt.Errorf("app: the client of the nodes: %w", err)
	}
	if b.issuer, err = wings.NewIssuer(cfg.App.URL); err != nil {
		return fmt.Errorf("app: the tokens of the nodes: %w", err)
	}
	b.daemon = appservers.NewDaemon(b.factory)

	dialer, err := dbhosts.NewMySQLDialer(cfg.DBHosts.TLS, 0)
	if err != nil {
		return fmt.Errorf("app: the database hosts: %w", err)
	}
	if b.hosts, err = dbhosts.NewExecutor(b.st.DbhostsLookup, dialer, 0); err != nil {
		return fmt.Errorf("app: the database hosts: %w", err)
	}

	// The name of the panel is read once: a panel that is renamed is restarted to show it in mail.
	page, err := b.st.PageapiSettings.Site(ctx)
	if err != nil {
		return fmt.Errorf("app: the name of the panel: %w", err)
	}
	if b.site, err = mail.NewSite(page.Name, cfg.App.URL); err != nil {
		return fmt.Errorf("app: the name of the panel in mail: %w", err)
	}

	raw, ok := b.in.Frontend.Locales.Lookup("en", "activity")
	if !ok {
		return errors.New("app: the frontend has no activity texts")
	}
	if b.texts, err = activity.ParseTexts(raw); err != nil {
		return err
	}

	// The settings of the allocations are read at each request; they are read once here too, so that one that was stored wrongly stops the start.
	if _, err = b.allocationPolicy(ctx); err != nil {
		return err
	}

	// S3 is available when its settings are given, for the backups that are on it whichever disk the new ones go to.
	b.disk = backups.DiskWings
	if cfg.Backups.Driver == "s3" {
		b.disk = backups.DiskS3
	}
	if cfg.Backups.S3Configured() {
		client, err := s3.New(cfg.Backups.S3Config(), s3.Options{})
		if err != nil {
			return fmt.Errorf("app: S3 for the backups: %w", err)
		}
		store := s3Backups{client: client, partSize: cfg.Backups.MaxPartSize, validity: time.Duration(cfg.Backups.PresignedMinutes) * time.Minute}
		b.archives, b.uploads = store, store
		b.log.Info("backups can be kept on S3", "s3", cfg.Backups.S3, "new_backups_on", b.disk)
	}

	// The service that starts backups is the same for the people who ask for one and for the schedules.
	b.backups, err = backupapi.NewService(backupapi.ServiceDeps{
		Store: b.st.BackupapiStore, Targets: b.st.ControlapiTargets, Daemon: backupapi.NewDaemon(b.factory), Archives: b.archives, Disk: b.disk, ThrottleLimit: BackupThrottleLimit, ThrottlePeriod: BackupThrottlePeriod,
		Logger: b.log, Now: b.in.Now, Random: b.in.Random,
	})
	if err != nil {
		return fmt.Errorf("app: the backups: %w", err)
	}

	trust, err := cfg.HTTP.TrustedPrefixes()
	if err != nil {
		return err
	}
	authorizer, err := authz.New(authz.Deps{
		Users: b.st.AuthzUsers, Keys: b.st.AuthzKeys, Servers: b.st.AuthzServers, Nodes: b.st.AuthzNodes, Settings: b.st.AuthzSettings, Audit: b.st.AuthzAudit,
		Logger: b.log, Now: b.in.Now,
	})
	if err != nil {
		return fmt.Errorf("app: the authorizer: %w", err)
	}
	b.srv, err = api.New(api.Options{Logger: b.log, Trust: middleware.NewProxyTrust(trust, b.log), Authorizer: authorizer})
	if err != nil {
		return fmt.Errorf("app: the server: %w", err)
	}
	return nil
}

// allocationPolicy reads whether the owners of servers may add allocations themselves, and from which ports. It is what allocapi asks at each request.
func (b *builder) allocationPolicy(ctx context.Context) (allocapi.Policy, error) {
	dir := b.st.AdminDirectoryStore
	on, err := setting(ctx, dir, "pterodactyl:client_features:allocations:enabled")
	if err != nil {
		return allocapi.Policy{}, err
	}
	p := allocapi.Policy{Enabled: on == "true"}
	for _, r := range []struct {
		key string
		dst *int
	}{{"pterodactyl:client_features:allocations:range_start", &p.RangeStart}, {"pterodactyl:client_features:allocations:range_end", &p.RangeEnd}} {
		v, err := setting(ctx, dir, r.key)
		if err != nil {
			return allocapi.Policy{}, err
		}
		if v != "" {
			if *r.dst, err = strconv.Atoi(v); err != nil {
				return allocapi.Policy{}, fmt.Errorf("app: the setting %s is %q, which is not a port", r.key, v)
			}
		}
	}
	return p, nil
}

// routes makes every package of endpoints and registers it. Every browser-facing route has the session loaded and the anti-CSRF token checked in
// front of it (a request with a bearer token is exempt, as a browser cannot be made to send one); the two APIs are limited per user after the
// authorizer has said who the user is.
func (b *builder) routes(ctx context.Context) error {
	cfg, st, lim := b.cfg, b.st, b.limits
	root := b.srv.Router.Group.Sub("", middleware.LoadSession(b.sessions, b.secure), middleware.CSRF(middleware.CSRFOptions{Protector: b.protect, AllowedOrigins: []string{cfg.App.Origin()}}))
	client := root.AfterAuth(middleware.Throttle(lim.ClientAPI, authz.RateKey))
	application := root.AfterAuth(middleware.Throttle(lim.ApplicationAPI, authz.RateKey))

	type registrar interface{ Register(api.Group) }
	use := func(name string, g api.Group) func(registrar, error) {
		return func(h registrar, err error) {
			switch {
			case b.err != nil:
			case err != nil:
				b.err = fmt.Errorf("app: %s: %w", name, err)
			default:
				h.Register(g)
			}
		}
	}
	log, now, random, loc := b.log, b.in.Now, b.in.Random, b.location

	// The daemons that call nodes, one for each package.
	control, files, backupsDaemon, nodesDaemon := controlapi.NewDaemon(b.factory), fileapi.NewDaemon(b.factory), backupapi.NewDaemon(b.factory), appnodes.NewDaemon(b.factory)

	checks := b.in.Ready
	use("healthapi", root)(healthapi.New(healthapi.Deps{Checks: checks, Limiter: lim.Health, Logger: log}))
	use("authapi", root)(authapi.New(authapi.Deps{
		Users: st.AuthapiUsers, Recovery: st.AuthapiRecoveryTokens, Resets: st.AuthapiResets, Outbox: st.AuthapiOutbox, Nodes: st.AuthapiNodes, Log: st.AuthapiActivityLog,
		Sessions: b.sessions, CSRF: b.protect, Failures: b.trackers[0], LoginLimiter: lim.Login, RequestLimiter: lim.Request, ResetLimiter: lim.Reset, EmailLimiter: lim.Email,
		Site: b.site, Secure: b.secure, Logger: log, Now: now, Random: random,
	}))
	use("pageapi", root)(pageapi.New(pageapi.Deps{
		Shell: b.in.Frontend.Shell, Locales: b.in.Frontend.Locales, Assets: b.in.Frontend.Assets, Users: st.PageapiUsers, Settings: st.PageapiSettings, LocaleLimiter: lim.Locale,
		Origins: st.PageapiOrigins, Logger: log, Now: now,
	}))
	use("remoteapi", root)(remoteapi.New(remoteapi.Deps{
		Servers: st.RemoteapiServers, Users: st.RemoteapiUsers, Activity: st.RemoteapiActivitySink, Events: st.RemoteapiEventLog, Outbox: st.RemoteapiOutbox,
		OldNodes: oldNodes{nodes: st.AppnodesStore, daemon: b.daemon}, Backups: st.RemoteapiBackups, Uploads: b.uploads, Failures: b.trackers[2], Site: b.site,
		Notify: servers.NotifyConfig{Install: true, Reinstall: true}, Logger: log, Now: now,
	}))

	use("clientapi", client)(clientapi.New(clientapi.Deps{
		Accounts: st.ClientapiAccounts, Servers: st.ClientapiServers, Activity: st.ClientapiActivityLogs, Texts: b.texts, HideAdminActivity: HideAdminActivity, Location: loc, Logger: log,
	}))
	use("accountapi", client)(accountapi.New(accountapi.Deps{
		Store: st.AccountapiStore, Sessions: b.sessions, Nodes: st.AccountapiNodes, Activity: st.AccountapiActivityLog, Failures: b.trackers[1], Issuer: b.site.Name, Location: loc, Logger: log,
		Now: now, Random: random,
	}))
	use("controlapi", client)(controlapi.New(controlapi.Deps{
		Targets: st.ControlapiTargets, Daemon: control, Tokens: b.issuer, Activity: st.ControlapiActivityLog, WebsocketLimiter: lim.Websocket, Logger: log, Now: now,
	}))
	use("fileapi", client)(fileapi.New(fileapi.Deps{
		Targets: st.ControlapiTargets, Daemon: files, Tokens: b.issuer, Activity: st.FileapiActivityLog, PullLimiter: lim.FilePull, Location: loc, Logger: log, Now: now,
	}))
	use("backupapi", client)(backupapi.New(backupapi.Deps{
		Store: st.BackupapiStore, Targets: st.ControlapiTargets, Daemon: backupsDaemon, Archives: b.archives, Disk: b.disk, Tokens: b.issuer, Activity: st.BackupapiActivityLog, RestoreLimiter: lim.BackupRestore,
		ThrottleLimit: BackupThrottleLimit, ThrottlePeriod: BackupThrottlePeriod, Location: loc, Logger: log, Now: now, Random: random,
	}))
	use("databaseapi", client)(databaseapi.New(databaseapi.Deps{
		Store: st.DatabaseapiStore, Hosts: b.hosts, Activity: st.DatabaseapiActivityLog, CreateLimiter: lim.DatabaseCreate, Enabled: DatabasesEnabled, AllowRandom: DatabasesAllowRandom,
		Logger: log, Random: random,
	}))
	use("allocapi", client)(allocapi.New(allocapi.Deps{
		Store: st.AllocapiStore, Activity: st.AllocapiActivityLog, AddLimiter: lim.AllocationAdd, Policy: b.allocationPolicy,
		Logger: log, Random: random,
	}))
	use("settingsapi", client)(settingsapi.New(settingsapi.Deps{
		Store: st.SettingsapiStore, Reinstaller: reinstaller{targets: st.ControlapiTargets, daemon: b.daemon}, Activity: st.SettingsapiActivityLog, Logger: log,
	}))
	use("subuserapi", client)(subuserapi.New(subuserapi.Deps{
		Store: st.SubuserapiStore, Activity: st.SubuserapiActivityLog, Outbox: st.SubuserapiOutbox, CreateLimiter: lim.SubuserCreate, Site: b.site, Location: loc, Logger: log, Random: random,
	}))

	runner, err := b.scheduleRunner(ctx)
	if err != nil {
		return err
	}
	use("scheduleapi", client)(scheduleapi.New(scheduleapi.Deps{
		Store: st.ScheduleapiStore, Starter: taskexec.NewStarter(ctx, runner), Activity: st.ScheduleapiActivityLog, CreateLimiter: lim.ScheduleCreate, Location: loc, Logger: log, Now: now,
	}))

	use("appusers", application)(appusers.New(appusers.Deps{
		Store: st.AppusersStore, Activity: st.AppusersActivityLog, Outbox: st.AppusersOutbox, Site: b.site, BaseURL: cfg.App.URL, Languages: languages(b.in.Frontend), Logger: log, Random: random,
	}))
	use("applocations", application)(applocations.New(applocations.Deps{Store: st.ApplocationsStore, BaseURL: cfg.App.URL, Logger: log}))
	use("appnests", application)(appnests.New(appnests.Deps{Store: st.AppnestsStore, Logger: log}))
	use("appallocs", application)(appallocs.New(appallocs.Deps{Store: st.AppallocsStore, Logger: log}))
	use("appnodes", application)(appnodes.New(appnodes.Deps{Store: st.AppnodesStore, Daemon: nodesDaemon, BaseURL: cfg.App.URL, Logger: log, Random: random}))
	use("appservers", application)(appservers.New(appservers.Deps{Store: st.AppserversStore, Targets: st.ControlapiTargets, Daemon: b.daemon, Hosts: b.hosts, Logger: log, Random: random}))
	use("appserverdbs", application)(appserverdbs.New(appserverdbs.Deps{
		Store: st.AppserverdbsStore, Hosts: b.hosts, Enabled: DatabasesEnabled, BaseURL: cfg.App.URL, Logger: log, Random: random,
	}))
	return b.err
}

// languages lists the languages the panel has translations for ("en", "hu"), sorted.
func languages(f Frontend) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range f.Locales.Names() {
		lang, _, _ := strings.Cut(name, "/")
		if !seen[lang] {
			seen[lang] = true
			out = append(out, lang)
		}
	}
	sort.Strings(out)
	return out
}

// scheduleRunner makes the runner of the schedules and what it carries them out with.
func (b *builder) scheduleRunner(_ context.Context) (*schedules.Runner, error) {
	if b.runner != nil {
		return b.runner, nil
	}
	exec, err := taskexec.New(b.st.ControlapiTargets, controlapi.NewDaemon(b.factory), b.backups, b.location, b.in.Now)
	if err != nil {
		return nil, fmt.Errorf("app: the tasks of schedules: %w", err)
	}
	logf := func(format string, args ...any) { b.log.Warn(fmt.Sprintf(format, args...), "component", "schedules") }
	b.runner = schedules.NewRunner(b.st.SchedulesStore, exec, taskexec.SystemClock{}, 0, logf)
	return b.runner, nil
}

// loops makes what runs beside the web server.
func (b *builder) loops(_ context.Context) ([]Loop, error) {
	cfg, st := b.cfg, b.st
	handlers := map[string]outbox.Handler{
		outbox.KindMail:        outbox.MailHandler(settingsSender{settings: st.MailSettings, hello: "localhost"}),
		outbox.KindDeauthorize: outbox.DeauthorizeHandler(st.OutboxNodes, outbox.NewDeauthorizer(b.factory)),
	}
	queue, err := outbox.NewRunner(st.OutboxStore, handlers, b.log, outbox.Options{Now: b.in.Now})
	if err != nil {
		return nil, fmt.Errorf("app: the outbox: %w", err)
	}

	jobs := []maintenance.Job{
		maintenance.PruneResetTokens(st.MaintenanceResetTokenStore),
		maintenance.PruneSessions(st.MaintenanceSessionStore, SessionIdle, SessionLifetime),
	}
	if age := cfg.Jobs.BackupPruneAge(); age > 0 {
		jobs = append(jobs, maintenance.PruneOrphanedBackups(st.MaintenanceBackupStore, age))
	}
	if keep := cfg.Jobs.ActivityKeep(); keep > 0 {
		jobs = append(jobs, maintenance.PruneActivity(st.MaintenanceActivityStore, keep))
	}
	housekeeping, err := maintenance.NewScheduler(jobs, taskexec.SystemClock{}, b.log)
	if err != nil {
		return nil, fmt.Errorf("app: the housekeeping: %w", err)
	}

	runner, err := b.scheduleRunner(context.Background())
	if err != nil {
		return nil, err
	}
	interval := cfg.Jobs.Schedules()
	scheduler := WithSetup(Forever("schedules", func(ctx context.Context) { runner.Run(ctx, interval) }), runner.Recover)

	return []Loop{
		Ticking("outbox", cfg.Jobs.Outbox(), queue.Run),
		scheduler,
		Forever("housekeeping", housekeeping.Run),
	}, nil
}
