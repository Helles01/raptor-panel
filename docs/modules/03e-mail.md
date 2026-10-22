# Mail groundwork – the e-mails of the panel

Status: **groundwork done** (2026-10-22): the texts, the escaping and the encoding of the five e-mails the reference sends. The mail **sending** (SMTP, the outbox with retries, the settings
page values) waits for the schema and the outbox; `internal/mail` sends nothing.

## Done: `internal/mail`
| Function | Content |
|---|---|
| `NewSite(name, url)` | The name and the public address that every link is built from (scheme, host and port only). |
| `Site.PasswordReset`, `AccountCreated`, `AddedToServer`, `RemovedFromServer`, `ServerInstalled` | The five e-mails, with the reference's subjects and texts, as plain text and HTML. |
| `ValidateAddress` | One plain address: no name, no list, no line break. |
| `Message.Encode(from, date, rand)` | The message as it goes over SMTP: headers, an encoded subject, and a multipart/alternative body of quoted-printable parts. |

## What matches the reference
The five messages and their texts (greeting, lines, button), the subjects, the "Setup Your Account" link of a new account, the red button of the removal mail.

## Deliberate differences (`docs/compat.md`: C-043, and C-038 for the reset link)
Links only from the configured address, escaped HTML, one-line text for what users typed, and checked addresses.

## Still to do
- The settings (`mail:*` of the reference: driver, host, port, user, password, from address and name) and the secret (`raptor_secrets`) for the SMTP password; an SMTP client
  with TLS required unless configured otherwise; the outbox with retries and a limit per recipient; the "test mail" endpoint.
- `Message.Encode` is checked by reading it back with `net/mail`; it should also be sent once through a real SMTP server before the first release.
