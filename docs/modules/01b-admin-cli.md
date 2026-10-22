# M1b – Admin CLI (előrehozva)

Állapot: **részben kész** (2026-10-22). A parancsok mögötti domain-logika kész és tesztelt. Maguk a parancsok
(`raptor user|apikey|location|node|nest|egg|settings`) és az adatbázis-adapterek a baseline sémára várnak, mert az
adapterek sqlc-vel készülnek, a séma nélkül nem generálhatók. Üres vagy félig működő parancsokat szándékosan nem
regisztráltam.

## Kész (tesztelt domain-logika)
| Csomag | Tartalom |
|---|---|
| `internal/ids` | Véletlen alfanumerikus token (a referencia `Str::random` ábécéje, torzításmentes bájtleképezéssel) és UUIDv4. |
| `internal/auth` | `HashPassword` (bcrypt, `$2a$10$`, max. 72 bájt) és `VerifyPassword` (a PHP `$2y$` előtagot normalizálja, a 72. bájt utánit figyelmen kívül hagyja, ahogy a PHP). API kulcs: `NewAPIKey` (`ptlc_`/`ptla_` + 16 karakteres azonosító, 32 karakteres titok), `SplitKey`, `Access` (0 nincs, 1 olvasás, 3 olvasás+írás, az `r_*` oszlopok bitjei), `ParseResourceAccess` (`servers=rw`). |
| `internal/users` | Új felhasználó validációja a referencia szabályai szerint (e-mail, felhasználónév regex, névhossz, nyelv) és `Prepare`: UUID és jelszó-hash. |
| `internal/nodes` | `ValidateLocation`, `NewNode` validáció (név, FQDN, séma, memória, lemez, portok, `daemonBase`), `NewCredentials` (16 karakteres token ID, 64 karakteres token), `WingsConfig`: a Wings `config.yml` kulcsról kulcsra a referencia `Node::getConfiguration()`-je szerint. |
| `internal/eggs` | PTDL_v1 és PTDL_v2 egg-fájl beolvasása és validálása, a Docker image-ek fájl szerinti sorrendjével. **A referencia mind a 14 hivatalos eggjét elfogadja** (teszt a `reference/database/Seeders/eggs` fájlokon). |
| `internal/settings` | A módosítható beállítások allow-listája és értékellenőrzése (`settings::` kulcselőtag), titkok nélkül. |

## Hátra
1. A baseline séma (`docs/schema/`), abból sqlc lekérdezések és adapterek: users, api_keys, locations, nodes, nests, eggs, egg_variables, settings.
2. A CLI parancsok bekötése. A jelszót nem kapcsolóból, hanem `--password-stdin`-ből vagy terminálból kérjük be (ehhez kell az `x/term`); az API kulcs és a node token **egyszer** jelenik meg, a `raptor_secrets` táblába kerül.
3. Az `egg_variables` tábla oszlopai: a referencia importere a fájl `field_type` mezőjét is átadja, de a migrációk között nem találtam `field_type` oszlopot; a dump mutatja meg, mit kell menteni.

## Szándékos eltérések a referenciától (`docs/compat.md`: C-004, C-007–C-009)
- Nincs admin UI, a CLI pótolja (C-004).
- Az egg import nem tölt le URL-ről, és nem fogad el image nélküli egget (a referencia a PTDL_v1 konverziónál `nil` nevű képet ír) (C-007).
- Új jelszó: legalább 8 karakter és legfeljebb 72 bájt (a referencia admin felülete nem korlátoz, a PHP pedig némán levágja a 72. bájt után) (C-008).
- Node: a `daemonBase` nem tartalmazhat `..` szakaszt, az FQDN-t csak formailag ellenőrizzük, DNS-feloldás nélkül (C-009).

## Ellenőrzés
`scripts/check.sh` zöld.
