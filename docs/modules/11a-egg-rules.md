# M11 groundwork – validation rules of egg variables

Status: **groundwork done** (2026-10-22): the interpreter for the `rules` of an egg variable. The endpoints that use it (`PUT
/servers/{server}/startup/variable` in M11, server creation and the build update in M9) and the schema wait.

## Done: `internal/eggrules`
| Function | Content |
|---|---|
| `Parse(text)` | Reads a rule string (`required\|string\|max:20`). A rule or pattern that cannot be used is an error wrapping `ErrUnsupported`. Empty parts are skipped. |
| `Normalize(value)` | What the framework does to input before the rules see it: surrounding white space is removed, and an empty value becomes null. |
| `Rules.Validate(value, attribute)` | One message per failing rule, in the order of the rules, with the texts of the reference's `validation.php`; no message means valid. `attribute` is how the value is named: `value` for a user's edit, `<name> variable` for the administrator's forms. |

`eggs.Validate` (egg import) now parses every variable's rules, so an egg with rules Raptor cannot apply is refused when it is imported (C-037).

### Behaviour worth knowing (all as in the reference's validator)
- A **null** value (nothing, or only spaces) is valid only if the rules say `nullable`; `required` and `filled` always refuse it. Without
  `nullable`, null also fails the type rules (`string`, `integer`, …): a rule string of `string` alone refuses an empty value.
- `max`, `min`, `between` and `size` measure a **number** when `integer` or `numeric` is among the rules, and the **length in characters** otherwise.
  `required|integer|max:128` is a number up to 128; `required|string|max:20` is up to 20 characters.
- `integer` is "an optional sign and digits, no leading zero, within 64 bits"; `numeric` allows a fraction and an exponent; `boolean` is `0` or `1`.
- A rule string is split at every `|`, so a pattern cannot contain one (the same in the reference).

All 14 seeded eggs of the reference (73 variables) parse, and every non-empty default passes the variable's own rules; both are tests.

## What matches the reference
The supported rules and their meaning, the normalization, the messages, the order of the messages, and the size measuring. The tests have about 150
cases of exactly these.

## Deliberate differences (`docs/compat.md`: C-037)
A fixed set of rules (the rest refused, never ignored), RE2 patterns, an exact `in`, a stricter `url`, and strings only.

## The handler, in this order
Find the variable (`user_viewable`, `user_editable` for a client); `Normalize` the value; `Parse` the variable's rules (an error: answer that the variable cannot
be edited, and log it); `Validate` (messages: 422 with one error per message); store `normalized ?? ""`; log the old and new values only if they differ.

## Still to do (waits for the schema)
- **The audit before cutover (M17):** parse the `rules` of every egg variable stored in the old database and list the ones Raptor refuses, with the egg
  and variable, so an administrator can rewrite them (usually a pattern with a lookahead, or a rule such as `ip` or `email`) before any user hits them.
- The handlers (M11, M9) and the activity entry `server:startup.edit`.
