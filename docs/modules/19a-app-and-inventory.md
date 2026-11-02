# The program around the routes: loops, web server, route inventory

Status: **done** (2026-11-02): `internal/app` (supervisor, web server), `internal/api/inventory` (tests only). What is still to come in
`internal/app` is the part that needs the stores: building every handler from them (`Build`) and the `raptor serve` command.

## `app.Supervisor`
Runs the loops of the panel (the outbox, the scheduler, the housekeeping, the web server) together.
- A loop that returns **before it was asked to** has failed, with an error or without: the others are told to stop and `Run` returns an error
  naming the loop. A panic is a failure too (the stack goes to the log, only the value to the error).
- `context.Canceled` after the request to stop is the loop doing its job; any other error is still reported.
- After the request to stop the loops get `StopTimeout` (30 s); a loop still running then is given up on and named in `ErrStopTimeout`.
  The time is counted from the request, not from the start.
- Adapters for the loops that exist: `Ticking("outbox", d, runner.Run)`, `Forever("maintenance", scheduler.Run)`,
  `WithSetup(Forever(…), runner.Recover)` for the scheduler, which must clear the marks a crash left before its first round.
- Names must be unique and non-empty; no loops at all is an error.

## `app.Web`
`Listen(addr)` (a port that is taken is found out before any loop starts) and `Web.Serve(ctx, ln)`. Limits: 10 s for the headers, 60 s to read
and to write, 2 min idle, 64 KiB of headers (the standard library allows 4 KiB more for the request line); a client slower than that is not a
browser (files and the console go to the node, not through the panel). On stop it takes no new request and gives the ones in progress 30 s,
then cuts them off and says so. The server's own complaints (a panic nobody caught, a bad connection) go to the panel's log at WARN through
the redacting handler, not to the standard error. TLS is not done here: the panel listens on plain HTTP behind a reverse proxy
(`http.trusted_proxies`).

## `internal/api/inventory` (tests only)
Registers **every** endpoint package on one router, with the zero value of each `Handlers` (registering builds method values only), and checks:
1. the routes are exactly the reference's routes (`testdata/reference-routes.txt`, written from `reference/routes/*.php`: 137 routes) plus the
   listed additions (`/healthz`, `/readyz`, `/assets/*`); the admin area (`/admin`) is declared absent and stays so;
2. who may call what: `/api/application` → application key, `/api/remote` → node token, `/api/client/servers` → server access, the rest of
   `/api/client` → signed in; only the listed routes are public or guest, and a public route is a `GET`;
3. an application key needs read-write on everything but a `GET` (and on the one `GET` that hands out a node's token, `…/configuration`);
4. every permission a server route asks for exists in `internal/permissions`;
5. a package with a `Register` method that is not in the list fails (`TestEveryPackageIsListed`).

It found nothing missing: all of the reference's routes are served, which is the answer to "is the API complete" until Wings and the frontend say otherwise.

## Tests
`app` 99 % (race clean, run 3 times); about 30 mutants killed (every failure path of the supervisor, the stop timeout and its start, every limit of the server,
the cut-off, the log of the server). The inventory was checked by removing and adding a line of the reference list.
