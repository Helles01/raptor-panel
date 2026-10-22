# M4 / M8 groundwork – API key restrictions and limits

Status: **groundwork done** (2026-10-22): the rules of the IP restriction of API keys, and the limits for creating them. The key format and the access levels were done
earlier (`internal/auth/apikey.go`). The endpoints (`/api/client/account/api-keys`, the Application API keys by CLI) and the authentication middleware wait for the schema.

## Done: `internal/auth/keyaccess.go`
| Function | Content |
|---|---|
| `ParseAllowedIPs(entries)` | Checks the list of a new key (at most 50 entries; an address, a network, a range, or an IPv4 address with trailing wildcards); the first bad entry is the error. |
| `LoadAllowedIPs(entries)` | Reads a stored list: unreadable entries match nothing and are counted. A list with entries is always a restriction. |
| `AllowedIPs.Allows(ip)`, `Restricted()` | Is a client address allowed. IPv4-mapped IPv6 addresses count as IPv4, a zone is ignored, and an address that cannot be read is never allowed by a restriction. |
| `DeniedMessage(ip)` | The reference's text for a refused address. |
| `CheckCanCreateKey(count)`, `ValidateKeyDescription`, `ShouldTouchLastUsed` | 25 keys per account (counted under a lock), the description, and writing the time of last use at most once a minute. |

## The authentication of a key, in this order
1. Find the key by its identifier and compare its secret in constant time (`auth.SplitKey` and the stored secret).
2. `LoadAllowedIPs(key.AllowedIPs)`; if `Restricted()`, the client address is the one the proxy-trust rules resolved (`middleware.ProxyTrust.ClientIP`, never a header taken as it
   comes); `Allows` is false: log `auth:ip-blocked` with the key's identifier, answer 403 with `DeniedMessage`. The same for Application API keys.
3. `ShouldTouchLastUsed(key.LastUsedAt, now)`: write the time.
4. Check the access level of the resource the request needs (`auth.Access`).

A key with unreadable entries is logged once per use with the number found (`bad` of `LoadAllowedIPs`), so that an administrator notices.

## What matches the reference
An empty list restricts nothing; any listed address, network or range allows its addresses; the message and the event; the limits of 25 keys, 50 entries and 500 characters.

## Deliberate differences (`docs/compat.md`: C-040)
Validation when a key is made, lenient and fail-closed reading of stored lists, a limit on writing the time of last use, and no netmask form.

## Still to do (waits for the schema)
- The endpoints and the middleware; the activity entries (`user:api-key.create`, `user:api-key.delete`, `auth:ip-blocked`).
- **The audit before cutover (M17):** list the stored keys whose `allowed_ips` hold an entry that `LoadAllowedIPs` cannot read (the number it returns), so that an
  administrator can rewrite them. This is the same audit as for egg rules and recovery tokens: one `raptor audit` command with one section per kind.
