# Raptor Panel – megvalósítási terv

Állapot: **3. változat, jóváhagyva (2026-10-22): az M0, M1 és M1b modul indulhat.** A terv a `reference/` mappa
tényleges tartalmából készült; a route-ok száma a `reference/routes/*.php` fájlokból származik.

Változások a 2. változathoz képest: lezárt döntések N1–N4 (3), „első használható" mérföldkő az M7 után (5), a titkok
forgatása a cutover pillanatában történik (N2: A, 13), tiszta gépre telepítés: a régi gépről csak az adatbázis kerül át
(13.1), telepítés előtti verzió- és hash-ellenőrzés (11), az `execute` szigorítása a `docs/compat.md`-ben (C-001).

Változások az 1. változathoz képest (2. változat): a referencia pontos verziója (1. fejezet, az 1. változat tévesen
1.12.x-et írt), új 10–13. fejezet (ismert hibák, verziók és eszközök, frontend audit, átállás és titkok forgatása), új M1b
és M3b modul, a titkok migrációja nem írja felül a régi oszlopokat (2.3).

## 1. Cél és hatókör

Go backend, amely ugyanazt a HTTP API-t adja, mint a referencia panel, így:

- a meglévő Wings daemon változtatás nélkül tud vele beszélni (`/api/remote/*`),
- a meglévő MariaDB/MySQL adatbázis újra felhasználható,
- a meglévő React frontend (`reference/resources/scripts`) használható marad.

**Referencia verzió:** Pterodactyl panel **v1.15.2** (kiadva 2026-10-21), commit
`90b2aa87469da4fffd941b3c99f3c1ca48a01b78`, a `reference/` egy sekély klón a `1.0-develop` ágról. Ellenőrizve: a HEAD, a
helyi tag és az `origin` `v1.15.2^{}` tagje ugyanarra a commitra mutat, és a GitHub szerint ez a legújabb stabil
kiadás (a `v2.0.0-beta.*` előzetes, nem célunk). Frissíteni nem kellett. Két figyelmeztetés:

- `reference/CHANGELOG.md` csak a v1.12.3-ig tart; a v1.12.4–v1.15.2 jegyzetei csak a GitHub release oldalakon vannak.
- `config/app.php` `canary` verziót ír; a verzió forrása a git tag, nem a fájl.

| Felület | Route-ok | Frontend használja? | Terv |
|---|---|---|---|
| `/api/client/*` | 69 | igen (React) | portoljuk |
| `/api/application/*` | 42 | nem, külső kliensek | portoljuk |
| `/api/remote/*` | 12 | nem, Wings hívja | portoljuk |
| `/auth/*`, `/`, `/account`, `/locales/*` | 12 | igen (HTML shell + JSON) | portoljuk |
| `/admin/*` | 97 | **nem React, Blade szerver-oldali HTML** | **nem portoljuk** (lásd 2.1) |

## 2. Amit a referenciából megtudtunk, és ami alakítja a tervet

### 2.1 Az admin felület nem újrahasznosítható
Az `/admin/*` oldalak Blade sablonok (`resources/views/admin`), a React csak a bejelentkezést és a felhasználói
területet adja. Ebből következik, hogy a következők **csak az admin felületen** léteznek, az Application API-ban nincsenek:

- Application API kulcsok létrehozása (`/admin/api/new`) – kulcs nélkül az Application API nem használható,
- adatbázis hostok, mountok, nest/egg létrehozás és import, panel beállítások (mail, advanced),
- szerver átköltöztetés indítása (a Wings felőli `transfer/success|failure` viszont API),
- szerver mountok, install flag váltás, allokáció alias.

Az első kiadásban **nincs admin UI**. A bootstrap-hoz szükséges CLI parancsok (felhasználó, API kulcs, node, location,
egg import, beállítások) **korán, az M1b modulban** készülnek el, mert nélkülük a panel nem használható. Az admin UI
külön, később hozott döntés (M16).

### 2.2 Hitelesítés
- Böngésző: süti alapú session + CSRF (`XSRF-TOKEN` süti → `X-XSRF-TOKEN` header; az axios ezt magától küldi).
- API: Sanctum bearer token. Azonosító 16 karakter (`ptlc_` account, `ptla_` application kulcs), titok 32 karakter,
  a DB-ben titkosítva (`api_keys.token`), azonosító alapján keresve.
- Application API: a felhasználónak `root_admin`-nak kell lennie, plusz a kulcs `r_*` oszlopai
  (`r_servers`, `r_nodes`, … értékek: 0 nincs, 1 olvasás, 2 írás) erőforrásonként.
- Client API: tulajdonos / admin / subuser (`subusers.permissions` JSON) jogosultság szerverenként
  (`websocket.connect`, `control.*`, `file.*`, `backup.*`, … – a teljes lista `app/Models/Permission.php`-ban).
- Wings → panel: `Authorization: Bearer <daemon_token_id>.<token>`, a token a `nodes.daemon_token` oszlopban titkosítva.
- Panel → Wings: `Authorization: Bearer <visszafejtett node token>`, valamint a panel **maga ír JWT-t** (HS256, a node
  tokennel aláírva) a websockethez, fájl feltöltéshez/letöltéshez és backup letöltéshez. A panel **soha nem fogad JWT-t**,
  a tokeneket a Wings ellenőrzi (ennek következménye a 10.1 pontban).
- 2FA: TOTP (`users.totp_secret` titkosítva) és recovery tokenek (`user_recovery_tokens`, bcrypt).
- Rate limit: login/checkpoint 10 kérés/perc/IP, jelszó-emlékeztető 2/perc, API kliens/application beállítható,
  szerver-szintű limit (backup 3/15 perc, database 2/perc, file pull 5/10 perc, subuser 10/15 perc, websocket 5/perc).

### 2.3 Titkosított értékek és a migrációs parancs
A referencia a Laravel `Crypt` formátumát használja (`APP_KEY`, AES-256-CBC + HMAC). Ilyen oszlopok:
`nodes.daemon_token`, `api_keys.token`, `users.totp_secret`, `database_hosts.password`, `databases.password`,
valamint a mail jelszó a `settings` táblában.

A CLAUDE.md szerint ehhez migrációs parancs kell, nem kérés-kezelőbe épített újraimplementálás. Terv:

- `internal/secrets`: Raptor saját formátuma (AES-256-GCM, `RAPTOR_SECRET_KEY` környezeti változóból, verzió-előtaggal).
- `raptor migrate-secrets`: a régi `APP_KEY`-jel visszafejti a Laravel formátumot, és az eredményt **új helyre** írja.
  **A meglévő oszlopokat nem írja felül és nem módosítja**, így a régi panelre van visszaút a cutover validálásáig.
  Van `--dry-run`, tranzakció, újrafuttatható (alapból csak a hiányzó értékeket konvertálja, `--all` mindent újra),
  és csak a parancs tartalmaz Laravel-visszafejtő kódot (`internal/legacycrypt`, kizárólag olvasás).
- A Raptor futás közben **csak az új helyet olvassa**.
- A régi oszlopok semlegesítése külön, kifejezetten kiadott, megerősítést kérő parancs (`--finalize`), csak a validálási
  ablak és a titkok forgatása után (13. fejezet).
- Kockázat, amit a modulban külön tesztelni kell: a Laravel `encrypt()` alapból PHP-`serialize`-olt értéket titkosít,
  az `encryptString()` nem; oszloponként ellenőrizni kell, melyik formátum van tárolva.

**Az „új hely" kérdése (lezárva, N1: új `raptor_secrets` tábla).** Eredetileg új oszlopok merültek fel. Ellenőriztem a régi panel modelljeit:
a `User` `$hidden` listája `['password','remember_token','totp_secret','totp_authenticated_at']`, a `Node`-é
`['daemon_token_id','daemon_token']`, az `ApiKey`-é `['token']`, a `Database`/`DatabaseHost`-é `['password']`. Egy új oszlop
(pl. `users.totp_secret_raptor`) **nincs a listákon**, így a régi panel `toArray()`-t használó kimenetei kiadnák; a
felhasználónál ez a `window.PterodactylUser` (`toVueObject()` → `toArray()`), vagyis a böngészőbe is eljutna a Raptor által
titkosított érték. Ugyanazt a visszautat (régi oszlopok érintetlenek) és az additív jelleget ad egy **új, különálló
`raptor_secrets` tábla** (`table_name`, `row_id`, `column_name`, `ciphertext`, `source_hash`, időbélyegek), de a meglévő
táblákhoz hozzá sem nyúl és nincs szivárgási kockázat. Ez lett a döntés (N1).

**A visszaút korlátai:**
- A Raptor által cutover után létrehozott titkok (új API kulcs, új node token, új 2FA beállítás) a régi panelnek nem
  látszanak. Az új sorok régi oszlopaiba a Raptor érvénytelen helyőrzőt ír (a régi oszlopok `NOT NULL`-ok lehetnek), így a
  régi panel nem tud velük mit kezdeni.
- A jelszó hash-ek kompatibilisek maradnak: a Raptor `$2a$` hash-t ír, amit a PHP `password_verify` elfogad.
- A titkok forgatása a cutover része (N2: A), ezért a visszaút nehezebb (13.4).

### 2.4 Egyéb kompatibilitási pontok
- Jelszó hash: `$2y$` → `$2a$` előtag normalizálás bcrypt ellenőrzés előtt (CLAUDE.md szerint).
- Válasz formátum (Fractal): `{"object":"server","attributes":{…},"relationships":{…}}`, listánál
  `{"object":"list","data":[…],"meta":{"pagination":{…}}}`, hibánál `{"errors":[{"code","status","detail"}]}`.
  A React kliens ezekre épít, ezért a kulcsneveket és a null-kezelést a `app/Transformers/**` alapján pontosan kell követni.
- Időbélyegek RFC 3339, UTC (`APP_TIMEZONE`, alapból UTC).
- Hashids (`HASHIDS_SALT`, hossz 8): az adatbázis route-kötése hash alapú a client API-ban; a többi modell használata
  (Allocation, Schedule, Subuser) a modul elején ellenőrizendő.
- Route-kötések a client API-ban: a `{server}` 8 karakteres értéknél `uuidShort`, egyébként `uuid` alapján keres; a
  `{user}` (subuser) a felhasználó `uuid`-ja alapján.
- Séma-verzió: a v1.15.2 utolsó migrációja `2024_07_13_091852_clear_unused_allocation_notes`. A Raptor induláskor
  (`raptor db check`, csak olvasás) összeveti a `migrations` táblát egy beépített listával, és eltérésnél nem indul el.
- A session süti titkosított Laravel süti; a régi session-ök nem vihetők át, **a cutover pillanatában mindenkit kijelentkeztet**.
  A `sessions` táblát (id, user_id, ip_address, user_agent, payload, last_activity) DDL módosítás nélkül tudjuk használni,
  a `payload` Go-oldali JSON lesz.
- A frontendet egy `main.js` webpack csomag adja; a Blade wrapper `window.PterodactylUser`, `window.SiteConfiguration`
  és a csrf meta-t injektál. Go-ban egy `html/template` shell ugyanezt adja (a felhasználó által megadott értékek
  kiszökése ellen lásd 10.2). Build: Yarn classic + `yarn.lock`, Node ≥ 22, React 16, easy-peasy, axios, Tailwind 3.
- Websocket: a konzol/statisztika a böngésző és a Wings között megy közvetlenül, a panelnek csak a JWT-t kell kiadnia.
  A panelben nincs websocket szerver.
- Háttérfolyamatok: a Laravel scheduler percenként futtatja a szerver ütemezéseket (`RunTaskJob`); beállítástól függően
  félóránként az elárvult backupok takarítását (`backups.prune_age`), naponta az activity log törlést
  (`activity.prune_days`); e-mail értesítések a queue-n (AccountCreated, AddedToServer, RemovedFromServer,
  SendPasswordReset, ServerInstalled); SFTP/websocket hozzáférés visszavonása (`RevokeSftpAccessJob`). Go-ban
  folyamaton belüli ütemező, SMTP küldő és tartós „outbox" lesz, Redis és külön queue worker nélkül.
- Fejlesztői környezet: a gépen jelenleg **nincs telepítve** Go, MariaDB/MySQL, Docker, Node, Yarn és sqlc. A Wings Linuxot
  igényel (Windowson WSL2 kell az integrációs teszthez). Telepítési lista: 11. fejezet.

## 3. Döntések

| # | Kérdés | Állapot | Megjegyzés |
|---|---|---|---|
| 1 | HTTP keretrendszer | **Echo – jóváhagyva** | A handler `error`-t ad vissza, egyetlen központi hibakezelő adja a `{"errors":[…]}` formátumot. |
| 2 | Titkosított oszlopok | **Egyirányú migráció, régi oszlopok érintetlenek – jóváhagyva** | Az új hely: `raptor_secrets` tábla (N1). |
| 3 | Admin felület | **Az első kiadásban nincs – jóváhagyva** | CLI előrehozva (M1b). |
| 4 | Séma az sqlc-hez | **Te adod a `mysqldump --no-data` kimenetet** | A `docs/schema/` mappába teszed, adat nélkül (lásd alább). **Még nincs ott** (2026-10-22-i állapot). |
| 5 | Wings teszt környezet | **WSL2 + valódi Wings – jóváhagyva** | A Wings verziója rögzítve (11. fejezet). |
| 6 | Elrendezés bővítése | **jóváhagyva** | 4. fejezet. |
| 7 | Modulsorrend | **jóváhagyva** | M1b és M3b új, a számozás nem változott (5. fejezet). |
| 8 | Eszközök telepítése | **jóváhagyva, csak hivatalos forrásból** | Lista és szabályok a 11. fejezetben; telepítés előtt a verziót és a hash-t a hivatalos oldalról újra ellenőrzöm, és megmutatom neked. |

**Séma-fájlok helye (4. döntés).** Kérlek ide tedd, a mappát te hozod létre (kód még nincs, a mappa a terv része):

- `docs/schema/mysqldump-no-data.sql`: a `mysqldump --no-data` kimenete (csak szerkezet, adat nélkül).
- `docs/schema/migrations.txt`: a `migrations` tábla `migration` oszlopa (a séma-verzió ellenőrzéshez, mert a
  `--no-data` dump nem tartalmazza a sorokat).
- `docs/schema/server-version.txt`: az adatbázis-szerver verziója (`SELECT VERSION();`).

A dumpban ne legyen adat és hitelesítő adat; a fájlokat én olvasom be, és ebből készül a `internal/db/schema/` (sqlc
bemenet). Ha a dump eltér a v1.15.2 migrációinak végállapotától, jelzem az eltérést.

**Lezárt döntések (2026-10-22):**

| # | Kérdés | Döntés |
|---|---|---|
| N1 | Hol tároljuk a Raptor-formátumú titkokat? | **Új `raptor_secrets` tábla** (2.3) |
| N2 | Mikor forgassuk a titkokat? | **A: a cutover pillanatában**; az `APP_KEY` kiszivárgását feltételezzük (13.3) |
| N3 | A `POST …/schedules/{id}/execute` ellenőrizze-e a feladatok akció-jogát is? | **Igen** (10.1 b); szigorúbb a referenciánál, a `docs/compat.md` C-001 rögzíti |
| N4 | Telepíthetők-e a listádon nem szereplő források? | **sqlc, govulncheck, Yarn: igen. WSL2, Docker, Wings: csak az M7 előtt** (11.2) |

## 4. Csomagszerkezet

A CLAUDE.md elrendezése marad az alap; a **(új)** jelölésű elemek a jóváhagyott bővítések.

```
cmd/raptor/                 serve + CLI alparancsok (user, apikey, node, location, egg, settings, migrate-secrets, db, …)
internal/
  config/                   env + ignorált config fájl betöltés
  db/                       sqlc kimenet, queries/*.sql, schema/ (baseline), additív migrációk
  auth/                     jelszó (bcrypt $2y), session, TOTP, API kulcs, jogosultság-feloldás
  wings/                    kliens a node daemonhoz + JWT kiadás (kötelező scope)
  api/
    router.go               route regisztráció, kötelező jogosultság-leíró
    middleware/             session, CSRF, API kulcs, throttle, biztonsági fejlécek, napló
    apierr/  resp/          hibaformátum, Fractal-szerű borítékok, lapozás
    validate/               allow-list validációs segédek
    client/ application/ remote/ authapi/ web/
  secrets/                  (új) Raptor titkosítási formátum
  legacycrypt/              (új) Laravel formátum, csak migrate-secrets használja
  hashid/                   (új) Hashids kompatibilis azonosítók
  activity/                 (új) activity log írás (server:*, auth:*, user:*), mezők allow-listával
  mail/                     (új) SMTP + sablonok
  outbox/                   (új) tartós újrapróbálkozás (SFTP/JWT visszavonás a Wingsen)
  servers/ nodes/ backups/ schedules/ dbhosts/   (új) domain logika, csak ahol valódi logika van
web/                        a frontend (reference/resources/scripts másolata + build)
docs/                       plan.md, modules/NN-*.md, compat.md, frontend-audit.md, schema/
versions.env                (új) rögzített verziók (Wings, Go, Node, MariaDB, sqlc)
```

Függőségi irány: `api` → domain csomagok → `db` / `wings` / `secrets`. Egyszerű CRUD handler közvetlenül egy kis
interfészen át hívja a `db`-t, külön domain réteg csak ott lesz, ahol logika van (szerver létrehozás, Wings konfig
összeállítás, backup állapotgép, ütemező, DB host provisioning, jogosultság-feloldás). Nincs globális állapot,
`context.Context` az első paraméter, a függőségeket a `cmd/raptor` köti össze.

Adatbázis szabályok:
- Raptor soha nem módosítja a meglévő táblák szerkezetét és a meglévő oszlopok tartalmát (kivétel: a felhasználó által
  kiadott művelet, pl. jelszócsere, és a `--finalize` a 13. fejezet szerint), és nem nyúl a Laravel `migrations` táblához.
- Új táblát csak additív migráció hozhat létre, saját `raptor_migrations` nyilvántartással.
- sqlc: MySQL motor, `emit_interface: true` (hogy a handler tesztek hamis `Querier`-rel fussanak), `WithTx` a tranzakciókhoz.
- Dinamikus SQL kizárólag a külső DB hostokon való adatbázis/felhasználó létrehozásnál lehet, azonosító allow-lista és
  idézés mellett.

## 5. Modulok és sorrend

Minden modul: implementáció → tesztek → rövid összefoglaló a `docs/modules/NN-<név>.md`-ben arról, hogy **mi egyezik a
referenciával** és **mi tér el szándékosan**; az eltérések a `docs/compat.md`-be is bekerülnek. A következő modulra a
jóváhagyásod után lépünk. A „10.x" jelölések az ismert hibák fejezetére hivatkoznak: azok a szabályok és tesztek az
adott modul kötelező részei.

| # | Modul | Tartalom | Csomagok |
|---|---|---|---|
| M0 | Váz | `go.mod`, `NOTICE` (MIT szerzői jogi szöveg a referenciából, jelenleg hiányzik), gofmt/vet/test script, `versions.env`, CI lépések (`govulncheck`, 12. fejezet audit) | – |
| M1 | Config, DB, sqlc, secrets | env/config betöltés, DB kapcsolat, baseline séma a `docs/schema/` fájlokból, `raptor db check`, sqlc beállítás, első lekérdezések (users, nodes, servers), `internal/secrets`, `raptor_secrets` tábla, `raptor migrate-secrets` (régi oszlopok érintetlenek) | `config`, `db`, `secrets`, `legacycrypt`, `cmd/raptor` |
| **M1b** | **Admin CLI (előrehozva)** | `raptor user create/list/delete/disable-2fa`, **`raptor apikey create/list/revoke`** (application és account kulcs, `r_*` szintekkel), **`raptor egg import`** (+ `nest list/create`; a referencia `EggImporterService` és a `database/Seeders/eggs` alapján), `raptor location create/list/delete`, `raptor node create/list/configuration` (Wings `config.yml` kimenet), `raptor settings get/set`. Csak DB és `secrets`, HTTP nélkül. Az egg import fájlból dolgozik, URL-ről nem tölt le. | `cmd/raptor`, `nodes`, `auth`, `secrets` |
| M2 | HTTP alap | router + kötelező jogosultság-leíró, hibaformátum, borítékok, lapozás, validáció segédek, throttle (kulcs soha nem konstans, 10.2), megbízható proxy lista, biztonsági fejlécek, napló titok-szűréssel | `api`, `api/middleware`, `apierr`, `resp`, `validate` |
| M3 | Login és session | `POST /auth/login`, `/login/checkpoint` (TOTP + recovery, újrafelhasználás elleni védelem), `/logout`, jelszó-emlékeztető és reset, session tár, CSRF, `ptlc_` kulcs hitelesítés, 2FA-kötelezés, HTML shell (`PterodactylUser`, `SiteConfiguration` biztonságos injektálással), `/locales/locale.json` fix allow-lista alapján (10.1 c) | `auth`, `api/authapi`, `api/web`, `mail` |
| **M3b** | **Frontend build és audit** | `web/` a `reference/resources/scripts` + build-konfig másolata a v1.15.2-ről, `yarn install --frozen-lockfile`, `tsc`, `lint`, `build:production`, függőség-audit és a `docs/frontend-audit.md` (12. fejezet), statikus kiszolgálás | `web/`, `api/web` |
| M4 | Account API | `/api/client/account*`: adatok, email (throttle), jelszó (+SFTP visszavonás), 2FA, API kulcsok (zárolt limit), SSH kulcsok, account activity | `api/client`, `auth`, `activity` |
| M5 | Szerverlista és részletek (csak olvasás) | client: `GET /`, `/permissions`, `/servers/{server}`, `/servers/{server}/activity` (idegen IP-k elrejtése); application: GET users, nodes (+`allocations`), locations, servers, nests, eggs; jogosultság-feloldás (tulajdonos/admin/subuser, `r_*` kulcsjogok), szerver állapot ellenőrzés (suspended, installing, transferring, node maintenance); minden `{server}` alatti erőforrás a szerverre szűkítve töltődik (10.2) | `api/client`, `api/application`, `auth`, `servers` |
| M6 | Power és Wings kliens | `POST /power`, `/command`, `GET /resources`, `GET /websocket` (JWT); Wings HTTP kliens (időtúllépés, TLS, hiba-leképezés); JWT kiadás **kötelező scope-pal és a scope-hoz tartozó jog ellenőrzésével** (10.1 a) | `wings`, `api/client` |
| M7 | Daemon API | `/api/remote/*` a backup nélküli része: sftp auth, servers lista/reset, szerver részletek (`settings` + `process_configuration`), install script és eredmény, transfer success/failure, activity feldolgozás. **Node-hatókör szerkezetileg kikényszerítve** (10.1 e). **Tesztelés a rögzített Wings-szel** (11. fejezet). | `api/remote`, `servers` |
| M8 | Users, nodes, locations, allocations | application API írás: users, nodes (+`/configuration` a Wings `config.yml`-hoz), locations, allocations | `api/application`, `nodes` |
| M9 | Szerver életciklus (Application API) | create (telepítés + deployment: node/allokáció kiválasztás, változó validáció), details (tulajdonosváltásnál a régi tulajdonos hozzáférésének visszavonása), build, startup, suspend, unsuspend, reinstall, delete (+force), `/nodes/deployable` | `servers`, `nodes`, `wings` |
| M10 | Subuserek és jogosultságok | `/servers/{server}/users*`, jogosultság halmaz validáció (csak regisztrált jogok), értesítő e-mail, JWT és SFTP visszavonás a Wingsen tartós outboxszal (10.2) | `api/client`, `servers`, `mail`, `outbox` |
| M11 | Szerver önkiszolgálás | startup (olvasás, változó frissítés), settings (rename, reinstall, docker-image), network allokációk (zárolt limit, 10.1 d) | `api/client`, `servers` |
| M12 | Ütemezések | schedules + tasks CRUD, `execute`, folyamaton belüli ütemező (percenként, cron kifejezés, `only_when_online`, `continue_on_failure`); **akciónkénti jogosultság** (10.1 b) | `schedules`, `api/client` |
| M13 | Backupok | client backup végpontok, letöltési JWT, zárolás, restore; remote: `/api/remote/backups/*`; elárvult backup takarítás; a backup azonosítót a panel generálja és formátum-ellenőrzi; limit zárolással (10.1 d). **Tesztelés a rögzített Wings-szel.** | `backups`, `api/client`, `api/remote` |
| M14 | Adatbázisok | client és application server-database végpontok, `database_hosts` kezelés (CLI: `raptor dbhost`), DB és felhasználó létrehozás a hoston, jelszó forgatás, hashid, limit zárolással (10.1 d) | `dbhosts`, `hashid`, `api/*` |
| M15 | Fájlkezelő | `/files/*` (list, contents, download, rename, copy, write, compress, decompress, delete, create-folder, chmod, pull, upload JWT), a `pull` URL séma-ellenőrzése | `wings`, `api/client` |
| M16 | Admin UI | **nincs betervezve**, külön döntés (2.1) | – |
| M17 | Átállás | csomagolás (Dockerfile), cutover és rollback leírás, **titkok forgatása** (13. fejezet), `--finalize`, valódi Wings (rögzített verzió) + változatlan React frontend végponttól végpontig teszt, negatív JWT és node-hatókör tesztek | – |

Az M6 és M7 egyben tesztelendő valódi Wings-szel (a Wings induláskor a panelből kéri a szerverlistát).

### Első használható mérföldkő (az M7 után)

Az M7 lezárása után külön mérföldkő következik: a Raptor egy **tesztgépen** végigpróbálható. Feltétele az M0–M7, valamint
az M1b (CLI) és az M3b (frontend build). Nem része: szerver létrehozás (M9), subuser (M10), ütemezés, backup,
adatbázis, fájlkezelő.

Tesztgép: WSL2 + Ubuntu LTS + Docker Engine + a rögzített **Wings v1.13.4**, Raptor tiszta telepítéssel és tiszta
MariaDB-vel (a 13.1 modellje kicsiben). A WSL2, Docker és Wings telepítése csak az M7 megkezdése előtt, külön lépésben.

Kész-feltételek (mindegyik a `docs/modules/milestone-1.md`-ben igazolva, teszt-naplóval):
1. A Raptor tiszta DB-n elindul; `raptor db check` zöld; `raptor user create --admin`, `node create`, `egg import` lefut.
2. **Login**: a változatlan React frontenden bejelentkezés jelszóval és TOTP-vel, kijelentkezés.
3. **Szerverlista és részletek**: a felhasználó a saját szervereit látja, idegent nem.
4. **Wings kapcsolat**: a Wings a Raptorból kéri a szerverlistát és elindul; a node csatlakozva, a Wings logban nincs hiba.
5. **Power**: start, stop, restart, kill; a Wings a konténert ténylegesen váltja; activity log bejegyzés készül.
6. **Konzol**: websocket JWT kiadás, a böngésző közvetlenül a Wingshez csatlakozik, kimenet érkezik, parancs küldhető.
7. A negatív tesztek zöldek: scope-os JWT elutasítás (10.1 a) és node-hatókör mátrix (10.1 e) valódi Wingsen.
8. `scripts/check.sh` zöld (gofmt, vet, teszt, govulncheck, frontend audit).

Nyitott kérdés, az M7 végén döntjük el: honnan jön a tesztszerver, amíg nincs szerver létrehozás (M9)? (a) a régi DB
másolata a node-ok átírásával, vagy (b) egy csak tesztcélú `raptor dev seed-server` parancs (build tag `dev`, a
kiadási buildbe nem kerül). Javaslat: (b), mert így éles adat nem kerül a tesztgépre.

## 6. Route → Go csomag megfeleltetés

Jelölés: **jog** = a route-hoz tartozó kötelező jogosultság-leíró (a router nem enged regisztrálni jog nélkül).

### 6.1 Client API (`/api/client`) → `internal/api/client`
Jog minden route-nál: bejelentkezett user (session vagy `ptlc_` kulcs), a `{server}` alatti route-oknál ezen felül
szerver hozzáférés + az alább megadott subuser jog. Application kulcs (`ptla_`) itt tiltott. A jog oszlop a
referencia route-ok és a `Permission` konstansok alapján készült; a végleges megfeleltetést (főleg a `files/*` és a
`power` jelek szerinti jogot) a `app/Http/Requests/Api/Client/**` osztályokból az adott modul elején pontosítjuk.

| Route | Fájl | Subuser jog | Modul |
|---|---|---|---|
| `GET /` , `GET /permissions` | `servers.go`, `permissions.go` | – | M5 |
| `GET /account`, `PUT /account/email`, `PUT /account/password` | `account.go` | – | M4 |
| `GET /account/two-factor`, `POST /account/two-factor`, `POST /account/two-factor/disable` | `twofactor.go` | – | M4 |
| `GET /account/activity` | `activity.go` | – | M4 |
| `GET/POST /account/api-keys`, `DELETE /account/api-keys/{identifier}` | `apikeys.go` | – | M4 |
| `GET/POST /account/ssh-keys`, `POST /account/ssh-keys/remove` | `sshkeys.go` | – | M4 |
| `GET /servers/{server}` | `servers.go` | – | M5 |
| `GET /servers/{server}/activity` | `activity.go` | `activity.read` | M5 |
| `GET /servers/{server}/websocket` | `websocket.go` | `websocket.connect` | M6 |
| `GET /servers/{server}/resources` | `resources.go` | – | M6 |
| `POST /servers/{server}/command` | `command.go` | `control.console` | M6 |
| `POST /servers/{server}/power` | `power.go` | `control.start|stop|restart` (jel szerint) | M6 |
| `GET /servers/{server}/startup`, `PUT …/startup/variable` | `startup.go` | `startup.read`, `startup.update` | M11 |
| `POST …/settings/rename`, `POST …/settings/reinstall`, `PUT …/settings/docker-image` | `settings.go` | `settings.rename`, `settings.reinstall`, `startup.docker-image` | M11 |
| `GET/POST …/network/allocations`, `POST …/allocations/{id}`, `POST …/{id}/primary`, `DELETE …/{id}` | `network.go` | `allocation.read|create|update|delete` | M11 |
| `GET/POST …/users`, `GET/POST/DELETE …/users/{user}` | `subusers.go` | `user.read|create|update|delete` | M10 |
| `GET/POST …/schedules`, `GET/POST/DELETE …/schedules/{schedule}`, `POST …/{schedule}/execute` | `schedules.go` | `schedule.read|create|update|delete` (+ a feladatok akciónkénti joga az `execute`-nál, N3) | M12 |
| `POST …/schedules/{schedule}/tasks`, `POST/DELETE …/tasks/{task}` | `scheduletasks.go` | `schedule.update` + az akció joga (`control.console`, `backup.create`, `control.start|stop|restart`) | M12 |
| `GET/POST …/backups`, `GET …/{backup}`, `GET …/{backup}/download`, `POST …/{backup}/lock`, `POST …/{backup}/restore`, `DELETE …/{backup}` | `backups.go` | `backup.read|create|download|delete|restore` | M13 |
| `GET/POST …/databases`, `POST …/{database}/rotate-password`, `DELETE …/{database}` | `databases.go` | `database.read|create|update|delete` (+`view_password`) | M14 |
| `GET …/files/list`, `…/contents`, `…/download`, `GET …/files/upload` | `files.go` | `file.read`, `file.read-content`, `file.create` | M15 |
| `PUT …/files/rename`, `POST …/copy|write|compress|decompress|delete|create-folder|chmod|pull` | `files.go` | `file.update|create|archive|delete` | M15 |

### 6.2 Application API (`/api/application`) → `internal/api/application`
Jog: `root_admin` user, kulcs típus `ptla_`, és a kulcs `r_<erőforrás>` szintje (olvasás vagy írás).

| Route | Fájl | Erőforrás | Modul |
|---|---|---|---|
| `GET /users`, `/users/{id}`, `/users/external/{external_id}` | `users.go` | `users` | M5 |
| `POST /users`, `PATCH /users/{id}`, `DELETE /users/{id}` | `users.go` | `users` (írás) | M8 |
| `GET /nodes`, `/nodes/{id}`, `/nodes/{id}/allocations` | `nodes.go`, `allocations.go` | `nodes`, `allocations` | M5 |
| `GET /nodes/{id}/configuration`, `POST /nodes`, `PATCH/DELETE /nodes/{id}` | `nodes.go` | `nodes` (írás) | M8 |
| `POST /nodes/{id}/allocations`, `DELETE …/{allocation}` | `allocations.go` | `allocations` (írás) | M8 |
| `GET /nodes/deployable` | `nodes.go` | `nodes` | M9 |
| `GET /locations*` | `locations.go` | `locations` | M5 |
| `POST/PATCH/DELETE /locations*` | `locations.go` | `locations` (írás) | M8 |
| `GET /servers`, `/servers/{id}`, `/servers/external/{external_id}` | `servers.go` | `servers` | M5 |
| `POST /servers`, `PATCH …/details|build|startup`, `POST …/suspend|unsuspend|reinstall`, `DELETE …/{id}[/{force}]` | `servers.go`, `serverdetails.go`, `servermgmt.go` | `servers` (írás) | M9 |
| `GET /servers/{id}/databases[/{database}]` | `serverdatabases.go` | `server_databases` | M14 |
| `POST …/databases`, `POST …/{database}/reset-password`, `DELETE …/{database}` | `serverdatabases.go` | `server_databases` (írás) | M14 |
| `GET /nests`, `/nests/{id}`, `/nests/{id}/eggs`, `/nests/{id}/eggs/{egg}` | `nests.go`, `eggs.go` | `nests`, `eggs` | M5 |

### 6.3 Daemon API (`/api/remote`) → `internal/api/remote`
Jog: node token (`Bearer <token_id>.<token>`), minden route-nál **szerkezetileg** ellenőrizve, hogy a kért szerver/backup
az adott nodehoz tartozik (10.1 e); átköltöztetés alatt a régi és az új node is olvashatja a szerver adatait.

| Route | Fájl | Modul |
|---|---|---|
| `POST /sftp/auth` | `sftp.go` | M7 |
| `GET /servers`, `POST /servers/reset`, `POST /activity` | `servers.go`, `activity.go` | M7 |
| `GET /servers/{uuid}`, `GET/POST /servers/{uuid}/install` | `servers.go`, `install.go` | M7 |
| `POST /servers/{uuid}/transfer/failure`, `…/success` | `transfer.go` | M7 |
| `GET /backups/{backup}`, `POST /backups/{backup}`, `POST /backups/{backup}/restore` | `backups.go` | M13 |

### 6.4 Auth és alap oldalak → `internal/api/authapi`, `internal/api/web`
| Route | Csomag | Jog | Modul |
|---|---|---|---|
| `GET /auth/login`, `/auth/password`, `/auth/password/reset/{token}`, fallback | `web` | vendég | M3 |
| `POST /auth/login`, `POST /auth/login/checkpoint` | `authapi` | vendég, throttle 10/perc, kulcs IP + azonosító (10.2) | M3 |
| `POST /auth/password`, `POST /auth/password/reset` | `authapi` | vendég, throttle 2/perc/IP | M3 |
| `POST /auth/logout` | `authapi` | bejelentkezett, CSRF | M3 |
| `GET /`, `GET /account`, `GET /{react}` | `web` | bejelentkezett | M3 |
| `GET /locales/locale.json` | `web` | nyilvános, fix allow-lista, throttle | M3 |

### 6.5 Admin (`/admin/*`, 97 route) – nem portoljuk, CLI/API pótolja
| Admin terület | Pótlás az első kiadásban |
|---|---|
| users, locations, nodes (+allocations) | CLI az M1b-ben, Application API az M8-ban |
| servers: létrehozás, build, startup, suspend, reinstall, törlés, adatbázisok | Application API (M9, M14) |
| API kulcsok (`/admin/api`) | `raptor apikey create/list/revoke` (**M1b**) |
| nests, eggek (import) | `raptor egg import`, `raptor nest …` (**M1b**); a meglévő DB eggjei megmaradnak |
| eggek szerkesztése, változók, scriptek, export | nincs (M16) |
| panel beállítások (general, mail, advanced) | config fájl / env, `raptor settings get/set` (**M1b**) |
| database hosts | `raptor dbhost …` (M14) |
| mountok, szerver mountok | nincs (M16) |
| szerver átköltöztetés indítása | nincs (M16); a Wings visszahívásai (M7) megvannak |

## 7. Átvágó szabályok

- **Jogosultság minden route-on**: a router csak jog-leíróval (`Public`, `Authenticated`, `Admin`, `ApplicationKey{res,szint}`,
  `ServerPerm("control.start")`, `Daemon`) enged regisztrálni; egy teszt végigmegy az összes route-on, és hibázik, ha hiányzik.
- **Validáció minden végpontnál**: egyszerű allow-list (karakterkészlet, hossz, felsorolt értékek), a szabályokat a
  referencia `app/Http/Requests/**` fájljaiból olvassuk ki; végpontonként táblás teszt a hibás bemenetekre. Kérésből
  érkező érték **soha nem képez fájlútvonalat** (10.1 c).
- **SQL**: csak paraméteres lekérdezés (sqlc), kivétel a 4. fejezetben leírt DB-host provisioning.
- **Titkok**: jelszó, token, API kulcs, TOTP titok soha nincs naplóban és activity log tulajdonságban; a naplózó szűri a
  fejléceket és mezőket, ezt teszt védi (10.2).
- **Hibák**: kontextussal csomagolva (`fmt.Errorf("…: %w", err)`); a kliensnek menő üzenet nem szivárogtat belső részletet.
- **Kód**: gofmt, go vet, táblás tesztek, nincs globális változó, `context.Context` első paraméter.
- **Ismert hibák**: a 10. fejezet minden sora a hozzá tartozó modul kész-feltétele (szabály + teszt).
- **Licenc**: a referenciából átvett részekhez a szerzői jogi szöveg a `NOTICE`-ba kerül; a kód nem soronkénti fordítás.

## 8. Tesztstratégia

1. Egységteszt: táblás, hamis `Querier` és hamis Wings kliens; handler tesztek `httptest`-tel.
2. Szerződésteszt: a válaszok kulcskészlete és null-kezelése a `app/Transformers/**` alapján rögzített JSON fixture-ökkel.
3. Integrációs teszt (build tag `integration`): valódi MariaDB a baseline sémával; `RAPTOR_TEST_DSN` környezeti változó;
   a párhuzamossági tesztek (10.1 d) itt futnak. Mátrix: MariaDB 10.11 és 11.8 (11. fejezet).
4. Titkosítás: Laravel formátumú tesztvektorok a `migrate-secrets`-hez (serialize és nem serialize változat), és teszt arra,
   hogy a parancs a régi oszlopokat bájtra azonosan hagyja.
5. Jogosultsági mátrix tesztek: node × route (10.1 e), felhasználó × szerver × erőforrás (10.2), task-akció × jog (10.1 b).
6. Végponttól végpontig (M7, M13, M17): valódi, **a `versions.env`-ben rögzített verziójú Wings** + a változatlan React
   frontend ellen. A teszt induláskor ellenőrzi, hogy a futó Wings verziója megegyezik a rögzítettel, különben megáll.
7. Biztonsági regressziós tesztek: minden 10. fejezeti sorhoz legalább egy teszt, amely az eredeti hibát reprodukálná.

## 9. Nem cél (egyelőre)

reCAPTCHA, telemetria, `upgrade` parancs, karbantartási mód, Redis/queue worker, Blade admin UI, a Pterodactyl v2 ág
(`2.0-develop`) újításai. A cél a v1.15.2 viselkedés.

## 10. Ismert hibák és javítások

Cél: a referenciában már javított hibákat a Raptorban ne ismételjük meg. Minden sor: mi történt → mit csinál a
referencia v1.15.2 → mi a Raptor szabálya és tesztje → melyik modul.

**Módszer és korlátok.** Források: a `reference/CHANGELOG.md` (v1.12.3-ig), a `pterodactyl/panel` és `pterodactyl/wings`
GitHub release oldalai (v1.13.0–v1.15.2, illetve Wings v1.13.x) és a két repó nyilvános biztonsági közleményei
(2026-10-22-i állapot). A közlemények és release jegyzetek szövegét egy lekérő eszköz összegezte, **nem olvastam el
mindet szóról szóra**, ezért az itt szereplő összefoglalók másodlagos források. Minden sor érintett modulja elején az
eredeti közleményt (GHSA azonosító) el kell olvasni, és a szabályt ahhoz igazítani. A Wings közlemények listájának a
legrégebbi része (2021 előtti és a 2023-as első tételek) csak részben látszott. A GitHub v1.13.0–v1.15.1 jegyzetei
nagyrészt PR-listák, a v1.14.0, v1.14.1 és a Wings v1.13.1/v1.13.2 jegyzetei üresek; a v1.13.0 „API key overflow" javítás
tartalmát a PR-ból (#5563) kell megnézni az M4 előtt.

### 10.1 A kiemelt öt tétel

**a) JWT-k célhoz kötése** – GHSA-8r6w-3qq5-4p4r (CVE-2026-54593, High; Panel <1.12.3, Wings <1.12.2).
- Mi történt: a Wings minden panel által aláírt JWT-t elfogadott, ami tartalmazta a `server_uuid`, `user_uuid`,
  `unique_id` mezőket, a cél nélkül. A `websocket.connect` joggal kiadott websocket token ezért az `/upload/file`
  végponton is működött, így `file.create` nélkül lehetett feltölteni.
- Referencia v1.15.2: minden JWT kap `scope` claimet (`JwtScope`: `websocket`, `file-upload`, `file-download`,
  `backup-download`, `transfer`), scope nélkül a generálás kivételt dob (v1.12.3). Az `aud` a node kapcsolati címe, a
  `jti` a hash-elt azonosító, van `unique_id`, `user_uuid`, lejárat.
- Raptor szabály: a `wings` csomag JWT-kiadó függvénye **kötelező scope paramétert** kap (nincs scope nélküli token), és a
  scope-hoz tartozó jogot **kiadáskor** ellenőrzi: `file-upload` → `file.create`, `file-download` → `file.read-content`,
  `websocket` → `websocket.connect`, `backup-download` → `backup.download`. Rövid lejárat. A `scope` a cél-claim; az `aud`
  a referenciához hasonlóan a node címe marad (kompatibilitás), mert a célhoz kötést a `scope` adja.
- Fontos pontosítás: a panel **nem fogad** JWT-t, a „fogadó oldal" a Wings. Ezt a Raptor nem tudja kikényszeríteni, csak a
  kiadás oldalát. Ezért (1) a Wings ≥ 1.12.2 kell, a rögzített verzió 1.13.4, (2) az M7 és M17 negatív teszteket kap valódi
  Wingsen: websocket tokennel a feltöltő és letöltő végpont elutasít, feltöltő tokennel a websocket elutasít, stb.
- Teszt: egységteszt a kiadó függvényre (scope nélkül nem fordul/hibázik; jog nélkül nem ad tokent), e2e a Wingsszel.
- Modul: M6 (kiadás), M13, M15, M7 és M17 (e2e).

**b) Ütemezett feladatok akciónkénti jogosultsága** – nem találtam hozzá közleményt, a követelmény helyes.
- Referencia v1.15.2: `StoreTaskRequest::authorize` a `schedule.update` mellett ellenőrzi az akció jogát
  (`Task::permissionForAction`: `command` → `control.console`, `backup` → `backup.create`, `power`: `start` →
  `control.start`, `stop` és `kill` → `control.stop`, `restart` → `control.restart`), és ezt a task létrehozásánál és
  módosításánál is használja. A `POST …/schedules/{id}/execute` (`TriggerScheduleRequest`) viszont **csak `schedule.update`-et kér**,
  így `control.*` vagy `backup.create` nélküli subuser is elindíthat egy olyan ütemezést, amelyben ilyen feladat van.
  Az ütemező a futáskor rendszerként dolgozik, a létrehozót nem tárolja.
- Raptor szabály: (1) task létrehozás és módosítás: `schedule.update` **és** az akció joga; (2) ismeretlen akció vagy
  payload → tiltás (az alapértelmezés a tiltás); (3) `execute`: a schedule minden feladatának akció-jogát is ellenőrizni
  (**szigorúbb a referenciánál, szándékos eltérés, N3 – jóváhagyva, rögzítve: `docs/compat.md` C-001**). A task törlése
  `schedule.update`-tel marad.
- Teszt: táblás teszt akció × payload × subuser-jog mátrixszal, külön az `execute`-ra.
- Modul: M12.

**c) `/locales/locale.json`** – GHSA-24wv-6c99-f843 (CVE-2025-49132, **Critical**, hitelesítés nélküli RCE; Panel <1.11.11).
- Referencia v1.15.2: a route nyilvános (`auth` és 2FA middleware nélkül), a `LocaleRequest` regex-szel szűr
  (`locale`: `^[a-z][a-z]$`, `namespace`: `^[a-z]{1,191}$`), majd a Laravel fordító-betöltőnek adja át.
- Raptor szabály: a megengedett `locale` és `namespace` értékek **fix allow-listája** a build során beágyazott
  (`embed.FS`) fordítófájlokból készül induláskor; a kérés értékét csak a lista kulcsaival hasonlítjuk össze, **soha nem
  képezünk belőle fájlútvonalat és nem nyitunk meg fájlt a kérés alapján**. Ismeretlen érték → 404/422. Throttle.
- Teszt: `../`, `%2e%2e%2f`, `\`, NUL bájt, nagybetű, túl hosszú érték, üres, tömb típusú paraméter, ismert-jó párok.
- Modul: M3.

**d) Adatbázis-limit (és más erőforrás-limit) ellenőrzés** – GHSA-jw2v-cq5x-q68g (CVE-2025-69198, Medium, <1.12.0) és
GHSA-fgmm-w5cx-vrfw (CVE-2026-35202, Low, a zár téves beállítása, javítva 1.12.3-ban).
- Referencia v1.15.2: tranzakcióban `lockForUpdate()->count()` a backupoknál, adatbázisoknál, allokációknál és az
  account API kulcsoknál (25 darab).
- Raptor szabály: egy tranzakcióban **először a szülő sort zároljuk** (`SELECT … FOR UPDATE` a `servers` vagy `users`
  soron), utána számolunk és szúrunk be ugyanabban a tranzakcióban. Így a számlálás és a beszúrás nem külön lépés, és üres
  halmaznál sincs rés a párhuzamos kérések között. Érinti: `database_limit`, `backup_limit`, `allocation_limit`, account API
  kulcs limit.
- Teszt (integrációs, MariaDB): N párhuzamos kérés a limitre; a limit soha nem léphető túl; zár nélküli megvalósítás
  elbukik a teszten.
- Modul: M4 (API kulcs), M11 (allokáció), M13 (backup), M14 (adatbázis).

**e) Wings node token hatóköre** – GHSA-g7vw-f8p5-c728 (CVE-2026-26016, **Critical**; Panel ≤1.12.0 → 1.12.1).
- Mi történt: a `GET /api/remote/servers/{uuid}`, a `ServerTransferController` (`failure`, `success`) és a
  `ServerInstallController` uuid alapján talált szervert, de nem ellenőrizte, hogy a kérő node az, amelyikhez a szerver
  tartozik. Egy node bármelyik másik node szerverének konfigját (és az egg install scriptjét, benne titkokkal) lekérhette,
  és hamis „transfer success" a szerver törlését okozhatta a forrás nodeon.
- Referencia v1.15.2: minden remote kontrollerben van explicit node-ellenőrzés (`ServerDetailsController`,
  `ServerInstallController`, `ServerTransferController`, `BackupStatusController`, `BackupRemoteUploadController`,
  `SftpAuthenticationController` `node_id` szűrés, `ActivityProcessingController` `$node->servers()`).
- Raptor szabály: **szerkezeti védelem** az „elfelejtett ellenőrzés" ellen: az `api/remote` handlerek csak egy
  `NodeScope` tárolót kapnak, amelynek minden lekérdezése kötelezően `nodeID` paramétert vesz (`… WHERE node_id = ?`),
  szűretlen szerver-lekérdezés nem érhető el onnan (architektúra-teszt a csomag importjaira és a tároló interfészére).
  Kivétel: átköltöztetés alatt a régi és az új node is olvashatja a szerver részleteit; a `success`/`failure`
  jogosultságát a referencia szerint, az M7 elején pontosítva.
- Teszt: két node × minden `/api/remote` route × saját és idegen szerver/backup azonosító; idegen azonosító mindig elutasítva
  (ugyanaz a válasz idegen és nem létező azonosítóra, hogy ne legyen felderíthető). A node token alapú azonosításnál
  állandó idejű összehasonlítás.
- Modul: M7, M13 (backup remote), M17 (e2e).
- Kapcsolódó Wings közlemény: GHSA-gqmf-jqgv-v8fw (CVE-2024-34066): kiszivárgott node token esetén tetszőleges fájl
  olvasás/írás. Ezért fontos a node token védelme és forgatása (13. fejezet).

### 10.2 További, a Raptort érintő tételek

| Forrás | Hiba | Raptor szabály és teszt | Modul |
|---|---|---|---|
| GHSA-xvc3-826v-xf47 (CVE-2026-61609, High; 1.7.0–1.12.4 → 1.13.0) | A `throttle:authentication` limiter kulcsa üres volt, így minden kérés egy közös gyűjtőbe került: egyetlen IP kb. 10 kéréssel percenként mindenkit (adminokat is) kizárhatott a login és a 2FA ellenőrzésből. A v1.15.2 kódban a kulcs IP. | A throttle kulcsa soha nem konstans: login = IP + azonosító, checkpoint = IP + megerősítő token, jelszó-emlékeztető = IP. A kliens IP-jét csak a megbízható proxyk listájából (`RAPTOR_TRUSTED_PROXIES`) vesszük az `X-Forwarded-For`-ból; proxy mögött beállítás nélkül induláskor figyelmeztet. Teszt: A IP kimerítése nem blokkolja B IP-t; konstans kulcsra elbukó regressziós teszt. | M2, M3 |
| GHSA-rgmp-4873-r683 (CVE-2025-69197, Medium) | A TOTP kód a lejárati ablakán belül újrahasználható volt. | A `users.totp_authenticated_at` alapján azonos vagy régebbi időablak kódját elutasítjuk (a referencia így tesz). Teszt: ugyanaz a kód kétszer. | M3 |
| GHSA-5vfx-8w6m-h3v4 (CVE-2021-41129, High) és CHANGELOG | 2FA megerősítő token hiba, ritkán hitelesítés-megkerülés; régebben a jelszó ellenőrzése előtt kérte a 2FA kódot (felhasználó-létezés szivárgás). | A jelszót a 2FA előtt ellenőrizzük; a megerősítő token véletlen 64 karakter, a sessionhöz és felhasználóhoz kötött, egyszer használatos, 5 perc, állandó idejű összehasonlítás; a checkpoint throttle-ozva. Teszt: lejárt, újrahasznált, más felhasználó tokenje. | M3 |
| GHSA-c479-wq8g-57hr (CVE-2024-49762, Medium) | A jelszó egyszerű szövegként naplózódott 2FA kikapcsolásakor. | Az activity log tulajdonságai eseményenként allow-listából jönnek, kérés-bemenet nem kerül bele. Teszt: a naplózó és az activity kimenetén jelszó-, token-, secret-, recovery-szerű mezők keresése minden érintett végpontra. | M2, M4 |
| GHSA-wwgq-9jhf-qgw6, GHSA-m49f-hcxp-6hm6 (CSRF: teszt e-mail, node token generálás, kijelentkeztetés) | Hiányzó CSRF védelem állapotváltoztató kéréseknél. | Minden session-nel hitelesített, nem biztonságos metódusú kérés CSRF-ellenőrzött (a kijelentkezés POST); bearer kulcsos kérés és `/api/remote` mentes. SameSite süti. Teszt: minden nem-GET route-ra vagy CSRF, vagy bearer követelmény. | M2, M3 |
| GHSA-j7f5-gfqm-pcx3 (Medium, 1.12.3) | Az e-mail módosítás végpont felhasználó-felderítést tett lehetővé. | `PUT /account/email` throttle; a login és az elfelejtett jelszó azonos válasz ismert és ismeretlen felhasználóra. Teszt: válasz- és időkülönbség. | M3, M4 |
| GHSA-7v3x-h7r2-34jv (Medium), CHANGELOG (v1.7.0) | Az API kulcsból származó session a kulcs törlése után is élt. | Bearer kulcsos kérés nem hoz létre tartós sessiont; a kulcs törlése/lejárata azonnal hat (nincs gyorsítótár). Teszt: törlés után a következő kérés 401. | M3, M4 |
| GHSA-g8gw-6j32-8w7g (Critical, javítva 1.2.2; a közlemény szövege csak részben látszott), CHANGELOG (hitelesítés-megkerülés: ütemezés futtatása, DB jelszó forgatás, backup megtekintés/törlés), GHSA-6888-7f3w-92jx (nem admin felhasználó listázhatja az összes szervert) | Hozzáférés olyan szerverhez vagy erőforráshoz, amelyhez a felhasználónak nincs joga. | Minden `{server}` alatti erőforrás (schedule, task, backup, database, allocation, subuser) a szerverre szűkítve töltődik (`WHERE id = ? AND server_id = ?`); `GET /api/client` alapból csak a saját szervereket listázza. Teszt: S1-hez jogosult felhasználó S2 erőforrás-azonosítóival minden route-on 404/403. | M5–M15 |
| GHSA-pjmh-7xfm-r4x9 (Medium) | Gyors API kulcs létrehozás. | Limit (25) zárolással, lásd 10.1 d. | M4 |
| GHSA-8c39-xppg-479c (CVE-2025-68954, High), GHSA-hr7j-63v7-vj7g (High; a közlemény javított verziót nem jelöl, a CHANGELOG a v1.12.1 javításai között sorolja fel) és CHANGELOG | Az SFTP (és websocket) hozzáférés nem szűnt meg a szerver-hozzáférés csökkentésekor, felhasználó törlésekor, jelszó cserekor. | A referencia a Wings `POST /api/deauthorize-user` (`{user, servers}`) hívásával vonja vissza: subuser módosítás/törlés, szerver tulajdonosváltás, felhasználó törlése, jelszóváltás. Raptor: ugyanezek az események, **tartós outbox** újrapróbálkozással (a node elérhetetlensége nem veszítheti el a visszavonást). Teszt: kiesett Wingsnél az outbox újrapróbál. | M4, M8, M9, M10 |
| GHSA-grp4-3hv5-m6vf (Wings, Medium, javítva 1.13.2) | A visszavont felhasználó az aláírt letöltő/feltöltő URL-eket a lejáratig használhatta. | Rövid JWT lejárat (az értékek a referenciából az M6 elején rögzítve), egyedi `jti`; a Wings verziója rögzítve. | M6, M15 |
| GHSA-77pg-7gfq-9hjr (Wings, **Critical**, javítva 1.13.1), GHSA-7w9q-4q32-wfj8 (SSRF a restore URL-eken), GHSA-qq22-jj8x-4wwv (SSRF fájl letöltésnél) | Backup azonosítók útvonal-bejárása a hoston; SSRF. | A backup azonosítót a panel generálja (UUID), a remote és client route-ok `{backup}` paramétere formátum-ellenőrzött; a `files/pull` URL-jét séma (`http`/`https`), hossz és hitelesítő adat nélküli forma szerint ellenőrizzük. A tényleges SSRF-szűrés a Wingsé, ezért a verzió rögzítve. | M13, M15 |
| GHSA-pfvc-3p5h-x7h6 (Wings, **Critical**, javítva 1.12.3) | A node konfig titkai kiszivároghattak az egg konfig-sablonokon keresztül. | Wings ≥ 1.13.4. Javaslat (az M1b elején a Wings forrás alapján pontosítandó): az `egg import` figyelmeztessen a `config.*` helyettesítőkre. | M1b |
| GHSA-384w-wffr-x63q, GHSA-mgr9-6c2j-jxrq, GHSA-5822-pw57-vv37, GHSA-3q45-4vhr-c7g7, v1.12.1 (aktivitás napló) | XSS az admin területen, a fájlkezelőben, a naplónézetben. | Az admin Blade nem portolódik. A Go shell **nem fűz be nyers értéket** a sablonba: a `window.PterodactylUser` és `SiteConfiguration` JSON-ja kontextus-függő escape-pel (`html/template`) kerül be (`</script>`, U+2028/9). Biztonsági fejlécek (CSP is; a referencia a v1.12.1-ben vezetett be alapértelmezett fejléceket). Teszt: `</script><script>alert(1)` a felhasználónévben. | M2, M3 |
| CHANGELOG (biztonsági és kapcsolódó bejegyzések, v1.1.3–v1.10.4) | Idegen IP-címek az activity logban; 2FA-kötelezés megkerülése; subuser jogosultság halmaz regisztrálatlan jogokkal; régi tulajdonos JWT-je tulajdonosváltás után. | Az activity API csak a saját bejegyzések IP-jét adja ki; a 2FA-kötelező beállításnál a client API hibát ad; a subuser jogokat allow-lista szerint validáljuk; tulajdonosváltásnál visszavonás (outbox). | M3, M5, M9, M10 |

### 10.3 Nem alkalmazható vagy csak a Wingsre vonatkozik
- v1.15.2 „php2shell" (CVE-2026-109618, CVE-2026-109619), PHP/php-fpm beállítás, valamint a Laravel-függőségek (pl.
  CVE-2024-52301): a Raptor Go, nem érinti. A megfelelő Go oldali védelem: a `govulncheck` a CI-ben (M0).
- A Wings-only közlemények (SFTP kvóta, symlink, regisztrációs adatok, chmod, konfigfájl-méret stb.): a Wings verziója
  rögzítve, a panel oldalán nincs tennivaló. Külön ellenőrizendő: GHSA-8j54-xcwx-597p (CVE-2026-61617, SFTP írási kvóta),
  mert a közlemény nem jelöl javított verziót; az M7 előtt meg kell nézni, hogy a v1.13.4 tartalmazza-e a javítást.

## 11. Rögzített verziók és eszközök

A verziók a `versions.env`-be kerülnek (M0), a tesztek és a CI azt olvassák. Az adatok 2026-10-22-én, hivatalos
forrásokból lettek kiolvasva. **Szabály (jóváhagyva): minden telepítés előtt a verziót és a hash-t a hivatalos oldalról
újra ellenőrzöm, a hash-t megmutatom neked, a letöltött fájl hash-ét kiszámolom és összevetem; eltérésnél nem telepítek.**
A letöltést külön, üres mappába teszem, és csak a hash egyezése után használom.

### 11.1 Rögzített verziók

| Összetevő | Verzió | Megjegyzés |
|---|---|---|
| Referencia (Pterodactyl panel) | v1.15.2, commit `90b2aa8` | Már így van, nincs teendő. |
| **Wings** | **v1.13.4** (kiadva 2026-10-14, nem előzetes) | A legújabb stabil. Az M7, M13 és M17 e2e tesztjei **kizárólag ezzel** futnak; a teszt induláskor ellenőrzi a futó Wings verzióját. Verziócsere csak külön jóváhagyással, új e2e futással. |
| Go | 1.27.3 | A `go.mod` `go` és `toolchain` sora is ezt rögzíti. |
| Node.js | 24.22.1 LTS (npm 11.19.1) | A referencia `engines`: `>=22`. Ha a régi webpack/babel lánc nem fut 24-en, 22 LTS-re váltunk, ezt az M3b rögzíti. |
| MariaDB | 11.8 LTS (támogatás 2030-02-13-ig) | Dev és fő teszt. Az integrációs mátrixban még 10.11 LTS. A pontos pontkiadás a telepítéskor kerül a `versions.env`-be. |
| Yarn | 1.x (classic) | A `yarn.lock` v1 formátumú; a referencia CI is `yarn install --frozen-lockfile`-ot használ. |

### 11.2 Telepítési lista és állapot

Telepítés előtt a pontos fájlnév, forrás, méret és hash megjelenik. A Node.js, a Yarn és a MariaDB akkor kerül sorra,
amikor először kellenek (M3b, illetve az integrációs tesztek); a MariaDB root jelszavát te adod meg.

**A te listádon szereplő hivatalos források:**

| Eszköz | Forrás | Fájl és ellenőrzés |
|---|---|---|
| Go 1.27.3 | go.dev/dl | `go1.27.3.windows-amd64.msi`, 66 146 304 bájt, SHA-256 `89ec8325f667c674fe1b94a5a88e26932aa12323589881927ff54da99d4b7faa` (a go.dev JSON szerint) |
| Node.js 24.22.1 LTS | nodejs.org/dist/v24.22.1/ | Windows x64 `.msi`, ellenőrzés a `SHASUMS256.txt` alapján |
| MariaDB 11.8 LTS | mariadb.org/download | Windows x64 `.msi`, a hash az oldalról; a root jelszót **te adod meg**, én nem tárolom, a teszt DSN környezeti változóban él |

**Nem szerepelt a listádon; az N4 döntés szerint: sqlc, govulncheck, Yarn jóváhagyva, WSL2/Docker/Wings csak az M7 előtt:**

| Eszköz | Forrás | Miért kell |
|---|---|---|
| sqlc | `go install github.com/sqlc-dev/sqlc/cmd/sqlc@<rögzített verzió>` (a sqlc hivatalos repója, Go modul proxyn át) | CLAUDE.md szerint az adatbázis-hozzáférést sqlc generálja |
| govulncheck | `go install golang.org/x/vuln/cmd/govulncheck@<rögzített verzió>` (a Go csapat hivatalos modulja) | Go oldali sebezhetőség-ellenőrzés a CI-ben |
| Yarn classic | Node-dal érkező `corepack` vagy `npm install -g yarn@1` (npm registry) | A frontend build és audit; a `yarn.lock` miatt |
| WSL2 + Ubuntu LTS, Docker Engine, Wings v1.13.4 bináris | Microsoft (`wsl --install`), Canonical, docs.docker.com, github.com/pterodactyl/wings/releases (a hash a release oldalról) | A valódi Wings e2e teszthez; csak az M7 megkezdése előtt, külön lépésben |

A Go függőségeket (pl. Echo) a `go.mod`/`go.sum` rögzíti; minden új függőséget a hozzáadásakor megmutatok.

## 12. Frontend (`web/`) build és audit

A frontend a `reference/resources/scripts` + build-konfig másolata a v1.15.2-ről; a régi függőségek (React 16, webpack 5,
~1300 `yarn.lock` bejegyzés) miatt az audit az M3b része.

- **Build**: `yarn install --frozen-lockfile`, `yarn tsc`, `yarn lint`, `yarn build:production` (ahogy a referencia CI).
  Node verzió: 11.1 szerint, kompatibilitási próbával.
- **Audit**: a lockfile `yarn.lock`, ezért a **`yarn audit`** fut a commitolt lockfile-on, mert az pontosan a szállított
  verziókat vizsgálja. A kért **`npm audit` nem használható közvetlenül** (nincs `package-lock.json`; ha az
  `npm install --package-lock-only` újrafeloldaná a verziókat, az már nem a szállított csomag lenne). Keresztellenőrzésnek
  futtatható az `npm audit` egy generált lockfile-on, de a mérvadó a `yarn audit`.
- **Besorolás**: a böngészőbe kerülő (runtime) függőségek magas és kritikus találatai blokkolnak, kivéve dokumentált
  kivétel (pl. nem elérhető kódút). A build-eszközök (dev) találatait követjük, de nem blokkolnak.
- **Javítás**: `resolutions` felülírás vagy függőség-frissítés; minden változtatás a `docs/frontend-audit.md`-ben
  (találat, besorolás, döntés, teszt). A módosítások minimálisak maradnak, a React forrást csak ott érintjük, ahol a
  javításhoz kell.
- **CI**: az audit minden futásnál lefut; új blokkoló találat megállítja a build-et.
- **Licenc**: a frontend MIT licencű; a szerzői jogi szöveg a `NOTICE`-ba kerül.

## 13. Átállás és titkok forgatása (M17 tervezete)

Kiindulás (N2): **az `APP_KEY` kiszivárgását feltételezzük.** Ebből következik, hogy a régi gépet és minden rajta lévő
fájlt megbízhatatlannak tekintjük, és a régi titkosítással védett minden titkot kompromittáltnak számítunk. Amíg a régi
oszlopok léteznek, a kulcs és az adatbázis ismeretében mindent vissza lehet fejteni.

### 13.1 Telepítési modell: tiszta gép, a régi gépről csak az adatbázis jön át

- A Raptor **tiszta gépre vagy tiszta telepítésre** kerül (új szerver/VM, vagy újratelepített operációs rendszer). Nem a
  régi panel gépére, nem a régi könyvtárakra.
- A régi gépről **kizárólag az adatbázis tartalma** kerül át egy `mysqldump`-ban. Semmi más nem: se `.env`, se
  `storage/`, se `vendor/`, PHP fájl, cron bejegyzés, webszerver- vagy php-fpm beállítás, tanúsítvány, SSH kulcs, szkript.
- Egyetlen kivétel az `APP_KEY` értéke, és az is csak ideiglenesen: a `raptor migrate-secrets` futásának idejére, a
  `RAPTOR_LEGACY_APP_KEY` környezeti változóban (soha fájlban, soha naplóban, a shell előzményekbe sem), utána
  megsemmisítve.
- Az adatbázis az új gépen **új MariaDB példányba** kerül, **új adatbázis-felhasználóval és új jelszóval** (a régi DB
  hitelesítő adatait nem használjuk újra), dump-importtal; az adatkönyvtárat nem másoljuk.
- Új, az új gépen generált elemek: `RAPTOR_SECRET_KEY` (`raptor keygen`), TLS tanúsítvány, szolgáltatás-fiók minimális
  jogosultsággal.
- A Wings nodeok a helyükön maradnak (külön gépek), de a konfigjuk az új node tokenekkel és az új panel URL-lel frissül
  (13.3).
- Az átvett adatbázis **nem megbízható adat**: a régi gép kompromittálódhatott, ezért az import után a 13.5 szerinti
  ellenőrzés következik, mielőtt bármi éles használatba kerül.

### 13.2 Lépések
1. **Előkészület**: az új gép felépítése a rögzített verziókkal (11. fejezet); a régi gép DB-jének `mysqldump`-ja
   karbantartási ablakban; a régi node `config.yml`-ek mentése (csak a visszaúthoz, nem az új gépre); teljes próba egy
   teszt gépen, DB-másolaton és egy teszt nodeon; `raptor db check`.
2. **Cutover**: a régi panel leállítása, **utolsó dump**, import az új DB-be (új felhasználóval), 13.5 szerinti
   ellenőrzés, `raptor migrate-secrets` (a régi oszlopok érintetlenek), a **forgatás** (13.3), a Raptor indítása, a nodeok
   Wings konfigjának frissítése és újraindítása, füstteszt. Minden felhasználó újra bejelentkezik.
3. **Validálás**: rövid, előre rögzített időszak. A régi gép és DB-je ez alatt érintetlenül, kikapcsolva marad (13.4).
4. **Lezárás**: `raptor migrate-secrets --finalize` megerősítéssel (a régi oszlopok semlegesítése az új DB-ben, előtte
   friss mentés); a régi gép végleges leállítása; a régi `APP_KEY`, `.env` és a régi DB hitelesítő adatainak
   megsemmisítése.

### 13.3 Mit forgatunk (a cutover része)

| Titok | Parancs (M1b/M14/M17) | Hatás és figyelmeztetés |
|---|---|---|
| **Node tokenek** (`nodes.daemon_token`, `daemon_token_id`) | `raptor node rotate-token <id>`, kimenet: az új Wings `config.yml` rész | A Raptor már az új tokenekkel indul, ezért a régi tokenek soha nem érvényesek az új panelen. Minden node `config.yml`-jét frissíteni és a Wingst újraindítani kell; addig a panel nem éri el azt a nodeot. **A játékszerverek futnak tovább**, csak a panel felől nem kezelhetők. A panel által kiadott JWT-k is érvénytelenek. |
| **API kulcsok** (account `ptlc_`, application `ptla_`) | `raptor apikey revoke --all` | A kulcsok nyers értéke visszafejthető volt, ezért kompromittáltnak számít, és nem adható ki újra (csak létrehozáskor látszik). Új kulcsokat kell létrehozni; a felhasználókat és az integrációkat előre értesíteni. |
| **Adatbázis-host jelszavak** (`database_hosts.password`) | `raptor dbhost rotate-password <id>` | A régi jelszót (az áttelepített másolatból) csak arra használjuk, hogy a hosthoz kapcsolódva beállítsuk az újat; a rekord és a hoston lévő fiók egyszerre változik. |
| **Szerverek adatbázis jelszavai** (`databases.password`) | `raptor dbhost rotate-all` (kifejezetten kérve) | **A játékszerverek konfigjaiba beírt jelszavak elromlanak**, ezért értesítéssel együtt és a tulajdonosokkal egyeztetve. Az `APP_KEY` kiszivárgása miatt az elhalasztás kockázat, ezt a cutover előtt külön eldöntjük. |
| **2FA titkok** (`users.totp_secret`) | `raptor user reset-2fa --all` | A felhasználóknak újra kell regisztrálniuk a TOTP-t. Hogy a 2FA ne ürüljön ki, a Raptor a mellék-táblában „újra-regisztráció kötelező" jelzőt tart: jelszó után csak az újraregisztráció után lép tovább. Az új recovery tokenek is újra készülnek. |
| Mail (SMTP) jelszó, reCAPTCHA kulcsok, S3 és egyéb hitelesítő adatok a régi `.env`-ben és a `settings` táblában | kézi, a szolgáltatónál | Mindent forgatni, ami a régi gépen volt. |
| `APP_KEY` | – | A Raptor **nem használja újra**; új `RAPTOR_SECRET_KEY` készül, a mentése külön, biztonságos helyen. |

A `HASHIDS_SALT` nem titok, és a cseréje megváltoztatná az URL-ekben látható azonosítókat, ezért marad.

Az A időzítés miatt a `migrate-secrets` főleg a `database_hosts.password` olvasásához kell (a host elérésére a forgatáskor);
a node tokenek, API kulcsok és 2FA titkok helyébe új értékek lépnek.

### 13.4 Visszaút
- A régi gép és DB-je a validálás végéig **kikapcsolva, érintetlenül** marad. Rutin visszagörgetés nincs, mert a régi gép
  kompromittáltnak számít; vészhelyzetben a régi panel csak elszigetelt hálózaton indítható.
- Ami a cutover után keletkezett (új kulcsok, node tokenek, 2FA beállítások, kijelentkeztetés), a régi panelnek nem látszik
  (2.3); a jelszó hash-ek kompatibilisek maradnak. A node tokenek forgatása miatt a Wings konfigokat vissza kell állítani
  (ezért kötelező a régi `config.yml`-ek mentése a 13.2 1. lépésben). Mindez a teljes próbát indokolja a cutover előtt.

### 13.5 Az átvett adatbázis ellenőrzése (javaslat, M17)
Új, csak olvasó `raptor audit` jelentés az importált DB-n, amelyet ember néz át, és semmit nem töröl magától:
- `root_admin` felhasználók és a nemrég létrehozott vagy módosított fiókok,
- application és account API kulcsok, felhasználói SSH kulcsok, subuserek jogosultságai,
- eggek `startup` parancsa, install scriptje és docker image-e a referencia `database/Seeders/eggs` állományához képest
  (az install script a nodeon fut),
- ütemezések és feladatok (`command`, `power`, `backup`), mountok forrás útvonalai, node rekordok (`fqdn`, séma),
  adatbázis-hostok, a `settings` tábla (mail szerver, 2FA követelmény),
- gyanús `activity_logs` események.
Az új parancsot külön jóváhagyás után építjük be, az M17 elején.

## 14. Következő lépés

Az M0, M1 és M1b indul (jóváhagyva). Az M1 sqlc része és a Raptor-tulajdonú táblák integrációs tesztjei a `docs/schema/`
fájlokra és egy MariaDB példányra várnak; az állapotot a `docs/modules/00-…`, `01-…` és `01b-…` jegyzetek rögzítik.
