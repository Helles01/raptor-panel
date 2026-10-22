# Frontend translations (the locales/ folder)


`locales/<locale>/<namespace>.json` files, embedded into the binary and served by `/locales/locale.json`.

- `en/activity.json` is generated from the reference panel's `resources/lang/en/activity.php` (Pterodactyl v1.15.2, MIT
  licensed, see `/NOTICE`) with `node scripts/convert-lang.js reference/resources/lang internal/api/web/locales en:activity`.
  The frontend only uses this namespace (`<Translate ns="activity">` for the activity log).
- `en/translation.json` is an empty object: i18next asks for the default namespace `translation` when it starts.

The set of files is the allow-list of what the endpoint can answer. Adding a language means adding a folder named with
two lower case letters; nothing else in the request ever reaches the file system.
