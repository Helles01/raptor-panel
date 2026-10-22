# M1b – Admin CLI (brought forward)

Status: **partly done** (2026-10-22). The domain logic behind the commands is done and tested. The commands themselves
(`raptor user|apikey|location|node|nest|egg|settings`) and the database adapters wait for the baseline schema, because
the adapters are built with sqlc and cannot be generated without it. Empty or half-working commands were deliberately not
registered.

## Done (tested domain logic)
| Package | Content |
|---|---|
| `internal/ids` | Random alphanumeric tokens (the alphabet of the reference's `Str::random`, with bias-free byte mapping) and UUIDv4. |
| `internal/auth` | `HashPassword` (bcrypt, `$2a$10$`, at most 72 bytes) and `VerifyPassword` (normalises the PHP `$2y$` prefix, ignores everything after the 72nd byte, as PHP does). API keys: `NewAPIKey` (`ptlc_`/`ptla_` + a 16 character identifier, a 32 character secret), `SplitKey`, `Access` (0 none, 1 read, 3 read+write, the bits of the `r_*` columns), `ParseResourceAccess` (`servers=rw`). |
| `internal/users` | Validation of a new user by the reference's rules (e-mail, username regex, name length, language) and `Prepare`: UUID and password hash. |
| `internal/nodes` | `ValidateLocation`, `NewNode` validation (name, FQDN, scheme, memory, disk, ports, `daemonBase`), `NewCredentials` (a 16 character token ID, a 64 character token), `WingsConfig`: the Wings `config.yml`, key by key as the reference's `Node::getConfiguration()`. |
| `internal/eggs` | Reading and validating PTDL_v1 and PTDL_v2 egg files, keeping the file order of the Docker images. **It accepts all 14 official eggs of the reference** (a test over the `reference/database/Seeders/eggs` files). |
| `internal/settings` | The allow-list of changeable settings and their value checks (`settings::` key prefix), without secrets. |

## Left
1. The baseline schema (`docs/schema/`), and from it the sqlc queries and adapters: users, api_keys, locations, nodes,
   nests, eggs, egg_variables, settings.
2. Wiring the CLI commands. The password is not taken from a flag but from `--password-stdin` or the terminal (this needs
   `x/term`); an API key and a node token are shown **once** and are stored in the `raptor_secrets` table.
3. The columns of the `egg_variables` table: the reference importer also passes the file's `field_type` field, but no
   `field_type` column was found among the migrations; the dump will show what has to be saved.

## Deliberate differences from the reference (`docs/compat.md`: C-004, C-007–C-009)
- No admin UI, the CLI replaces it (C-004).
- The egg import does not download from a URL, and does not accept an egg without an image (the reference writes an image
  named `nil` in its PTDL_v1 conversion) (C-007).
- A new password: at least 8 characters and at most 72 bytes (the reference admin UI does not restrict, and PHP silently
  truncates after the 72nd byte) (C-008).
- Node: `daemonBase` must not contain a `..` segment, and the FQDN is checked for format only, without DNS resolution
  (C-009).

## Verification
`scripts/check.sh` is green.
