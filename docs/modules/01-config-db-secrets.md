# M1 – Config, adatbázis, titkok

Állapot: **részben kész** (2026-10-22). A séma-független rész kész és tesztelt; a baseline séma, a hozzá tartozó sqlc
lekérdezések és a MariaDB-t igénylő integrációs tesztek a `docs/schema/` fájlokra és egy MariaDB példányra várnak.

## Kész
| Csomag | Tartalom |
|---|---|
| `internal/config` | YAML fájl (opcionális, `--config` vagy `RAPTOR_CONFIG`) és `RAPTOR_*` környezeti változók, a környezet felülír. Ismeretlen mező hiba. Allow-list validáció (host, port, név, felhasználó, TLS mód). A jelszó és a kulcs sem `%v`, `%+v`, `%#v`, sem `slog` kimenetben nem jelenik meg (teszt védi). |
| `internal/secrets` | A Raptor titokformátuma: AES-256-GCM, `rp1:` előtag, a titok a helyéhez kötött (`raptor_secrets\|tábla\|sor\|oszlop` mint AAD), így sorok között nem cserélhető. Az `Open` hibája nem árul el okot. |
| `internal/legacycrypt` | A referencia (Laravel `Crypt`) formátumának **csak visszafejtése**: AES-CBC + HMAC-SHA256 (az `iv+value` felett), PHP-szerializált string kicsomagolása, `APP_KEY` (`base64:` előtaggal is). A MAC előbb ellenőrződik, mint bármi más feldolgozás; minden hiba ugyanaz az `ErrDecrypt`. |
| `internal/legacycrypt/legacycrypttest` | Csak teszthez: a Laravel formátum előállítása. Éles kód nem importálhatja. |
| `internal/secretmigration` | A `migrate-secrets` logikája: 6 oszlop (`nodes.daemon_token`, `api_keys.token`, `users.totp_secret`, `database_hosts.password`, `databases.password`, a `settings` SMTP jelszava). Mindent vagy semmit: ha egy érték sem fejthető vissza, **semmi nem íródik**. Újrafuttatható (forrás-hash alapján csak az új vagy megváltozott sorokat konvertálja, `--all` mindent), `--dry-run`. A régi oszlopokhoz csak olvasási hozzáférése van. |
| `internal/db` | `Open` (biztonságos driver-alapértékek, UTC, `utf8mb4`), `Migrate` (csak Raptor-táblák: `raptor_migrations` nyilvántartás, adatbázis-zár a párhuzamos futás ellen, egy utasítás fájlonként), `ExpectedMigrations` (a v1.15.2 195 migrációja) és `CompareMigrations`, `AppliedLaravelMigrations` (csak olvas), `SecretStore` (a régi oszlopok rögzített `SELECT`-jei, az egyetlen írt tábla a `raptor_secrets`). |
| sqlc | `sqlc.yaml`, `internal/db/migrations/0001_raptor_secrets.sql`, `internal/db/queries/secrets.sql`, generált `internal/db/dbq`. |
| `cmd/raptor` | `raptor keygen`, `raptor db check`, `raptor db migrate`, `raptor migrate-secrets [--dry-run] [--all]`. A régi `APP_KEY` csak a `RAPTOR_LEGACY_APP_KEY` környezeti változóból jön, fájlból soha. |

## Mi egyezik a referenciával
- A titkosítási formátum minden eleme (CBC, HMAC az `iv+value` felett, `\/` escape a JSON-ban, PHP `s:N:"…";` szerializálás bájt-hosszal). A referencia mind a hat oszlopot az alapértelmezett, szerializáló `encrypt()`-tel írja; a kód a nem szerializált (`encryptString`) értéket is kezeli.
- A migrációk listája a `reference/database/migrations` fájlnevei (195 db, az utolsó `2024_07_13_091852_clear_unused_allocation_notes`).

## Szándékos eltérések
Lásd `docs/compat.md`: C-002 (titkok a `raptor_secrets` táblában), C-006 (`raptor_` előtagú additív táblák).

## Ellenőrzés
`scripts/check.sh` zöld. Az integrációs tesztek (`internal/db/integration_test.go`, `-tags integration`) lefordulnak
(`go vet -tags integration`), **de még nem futottak**, mert nincs MariaDB.

## Ami hátra van, és mire vár
1. **Baseline séma** (`docs/schema/mysqldump-no-data.sql`, `migrations.txt`, `server-version.txt`) → `internal/db/schema/`, a sqlc második sémaforrása, a további lekérdezések. A `migrations.txt`-et összevetem az `expected_migrations.txt`-szel.
2. **MariaDB példány** az integrációs tesztekhez (`RAPTOR_TEST_DSN`); a root jelszót te adod meg.
3. **Valódi Laravel minta.** A tesztvektorokat a formátum leírása alapján, saját kóddal állítottam elő, ezért egy közös félreértés kimaradhat. A kizáráshoz a valódi adatbázison futtatott `raptor migrate-secrets --dry-run` kell (minden sor visszafejtése a MAC-ellenőrzéssel együtt igazolja a formátumot), vagy egy valódi PHP-val titkosított érték és a hozzá tartozó kulcs.
4. A `settings` tábla `key`/`value` oszlopneveit és a `settings::mail:mailers:smtp:password` kulcsot a dump alapján újra ellenőrizni.
