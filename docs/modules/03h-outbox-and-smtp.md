# The outbox and the mail sender

Status: **done over a store interface** (2026-11-02): `internal/outbox` and `internal/mail/sender.go`. The store of the jobs (a table, and
the `Enqueue` the change of a subuser or an owner writes in its own transaction) waits for the schema.

## `internal/outbox`
A change that the world must hear of (a mail, a node told that a user lost access) writes a job in the same transaction as the change,
so a crash between the two loses nothing; the runner takes the jobs out afterwards. A job may run more than once, so a handler must
do no harm the second time (the mail of a job may be sent twice if the process dies between sending and recording).

| Piece | Content |
|---|---|
| `Store` | `Claim` (the jobs due, kept from the others for a lease), `Done`, `Retry` (at a time, with the failure text), `Fail` (set aside for good). Safe for several processes |
| `Runner` | `Tick` takes up to a batch (20) and does each with a time limit (30 s; the lease, 5 min, must be longer); a failure is retried after a wait that doubles from 30 s to 1 h, at most 10 times; `Permanent(err)` or a kind with no handler sets it aside at once; a panic of a handler is a failure; a stopped context leaves the rest of the batch to its lease. `Run` ticks until stopped and logs a store that fails |
| `MailJob`, `Mailer` | a message as a job; `Mailer.Send` is the `Outbox` the endpoint packages take |
| `MailHandler` | sends with a `Sender`; an error that says `Permanent()` (the `*mail.SendError` of a refused recipient or message) is set aside, anything else is retried |
| `DeauthorizeJob`, `DeauthorizeHandler` | tells a node that a user lost access (on some servers or all): a node that is gone needs no telling, a 4xx of the node (not 408 or 429) will come again and is set aside, anything else is retried; `NewDeauthorizer(*wings.Factory)` talks to a real Wings |

Texts stored with a failure are the errors of the handlers: they hold no passwords or tokens (the SMTP errors carry the server's reply
and nothing of what was sent; the node errors carry the node's message).

## `mail.Sender`
Delivery over SMTP with `net/smtp`: `StartTLS` (the default: a server that does not offer it is not used), `ImplicitTLS` and `NoTLS`
(a password is never sent without TLS except to this machine), sign-in with PLAIN, a time limit for the whole delivery and the context.
Which failures are for good: a refused recipient or message with a code of 500 to 599, or a message that cannot be written (a bad address
or subject); a refused sender, a failed sign-in or TLS, a server that is not there are not: they are cured by waiting or by the
administrator. Compat C-067. The settings come from the settings table (`mail:mailers:smtp:*`, 08b): `mail.OptionsFromSettings` reads them (`18a-operations.md`); the wiring that calls it waits.

## Tests
`outbox` 97 %, `mail` 94 % statement coverage. The sender is tested against a fake SMTP server in the test (greeting, EHLO, STARTTLS and
implicit TLS with a certificate made for the test, PLAIN sign-in, refusals at each step, a server that never answers). Mutation spot checks
killed every mutant but one equivalent one (the loop cap of the backoff, capped again by `min`).
