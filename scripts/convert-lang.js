// Converts the reference panel's PHP translation files (resources/lang/<locale>/<namespace>.php) to JSON for the
// /locales/locale.json endpoint, applying the same placeholder rewrite as the reference: ":name" becomes "{{name}}",
// the form the frontend's i18next understands.
//
// Usage, from the repository root:
//   node scripts/convert-lang.js reference/resources/lang internal/api/web/locales en:activity
//
// Each argument after the output folder is <locale>:<namespace>. Only the namespaces the frontend uses are converted,
// see internal/api/web/LOCALES.md. The files are plain `return [ ... ];` arrays of strings; anything else is an
// error, so an unexpected construct cannot slip through unnoticed.

const fs = require('fs');
const path = require('path');

const BACKSLASH = String.fromCharCode(92);

function parsePhpArray(source) {
    let i = 0;

    const fail = (message) => {
        throw new Error(`${message} at offset ${i}`);
    };

    function skipSpace() {
        for (;;) {
            while (i < source.length && /\s/.test(source[i])) i++;
            if (source.startsWith('//', i) || source[i] === '#') {
                while (i < source.length && source[i] !== '\n') i++;
            } else if (source.startsWith('/*', i)) {
                const end = source.indexOf('*/', i);
                if (end < 0) fail('unterminated comment');
                i = end + 2;
            } else {
                return;
            }
        }
    }

    function parseString() {
        const quote = source[i++];
        let out = '';
        while (i < source.length && source[i] !== quote) {
            if (source[i] === BACKSLASH && (source[i + 1] === quote || source[i + 1] === BACKSLASH)) {
                out += source[i + 1];
                i += 2;
            } else {
                out += source[i++];
            }
        }
        if (source[i] !== quote) fail('unterminated string');
        i++;
        return out;
    }

    function parseValue() {
        skipSpace();
        if (source[i] === '[') return parseArray();
        if (source[i] === "'" || source[i] === '"') return parseString();
        return fail('expected a string or an array');
    }

    function parseArray() {
        i++; // [
        const entries = [];
        let keyed = false;
        for (;;) {
            skipSpace();
            if (source[i] === ']') {
                i++;
                break;
            }
            const first = parseValue();
            skipSpace();
            if (source.startsWith('=>', i)) {
                i += 2;
                entries.push([first, parseValue()]);
                keyed = true;
            } else {
                entries.push([entries.length, first]);
            }
            skipSpace();
            if (source[i] === ',') i++;
            else if (source[i] !== ']') fail('expected , or ]');
        }
        return keyed ? Object.fromEntries(entries) : entries.map((e) => e[1]);
    }

    const at = source.indexOf('return');
    if (at < 0) fail('no return statement');
    i = at + 'return'.length;
    skipSpace();
    if (source[i] !== '[') fail('expected return [');
    return parseArray();
}

// ":name" -> "{{name}}", exactly as LocaleController::i18n() does.
function rewritePlaceholders(value) {
    if (Array.isArray(value)) return value.map(rewritePlaceholders);
    if (value && typeof value === 'object') {
        return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, rewritePlaceholders(v)]));
    }
    return value.replace(/:([\w.-]+\w)([^\w:]?|$)/gm, '{{$1}}$2');
}

const [, , langRoot, outRoot, ...targets] = process.argv;
if (!langRoot || !outRoot || targets.length === 0) {
    console.error('usage: node scripts/convert-lang.js <lang folder> <output folder> <locale>:<namespace>...');
    process.exit(2);
}

for (const target of targets) {
    const [locale, namespace] = target.split(':');
    const input = path.join(langRoot, locale, `${namespace}.php`);
    const data = rewritePlaceholders(parsePhpArray(fs.readFileSync(input, 'utf8')));
    const output = path.join(outRoot, locale, `${namespace}.json`);
    fs.mkdirSync(path.dirname(output), { recursive: true });
    fs.writeFileSync(output, JSON.stringify(data, null, 2) + '\n');
    console.log(`${input} -> ${output}`);
}
