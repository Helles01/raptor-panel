// Audits the frontend dependencies, and fails on high or critical advisories in code that ships to the browser.
//
// `yarn audit` reports every package in the tree, including webpack, babel and the dev server, which never reach a
// user's browser. This script asks webpack which package versions are in the production bundle and only blocks on
// those. Everything else is reported, not blocking. Known and accepted findings are listed in
// web/audit-exceptions.json with a reason.
//
// Usage, from the repository root, after `yarn install --frozen-lockfile` in web/:  node scripts/frontend-audit.js

const fs = require('fs');
const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

const webDir = path.join(__dirname, '..', 'web');
const rank = { info: 0, low: 1, moderate: 2, high: 3, critical: 4 };
const blockingFrom = rank.high;
const isWindows = process.platform === 'win32';
const backslash = String.fromCharCode(92);

function run(command, args, options) {
    const result = spawnSync(isWindows && command === 'yarn' ? 'yarn.cmd' : command, args, {
        cwd: webDir,
        encoding: 'utf8',
        maxBuffer: 512 * 1024 * 1024,
        shell: isWindows,
        ...options,
    });
    if (result.error) throw result.error;
    return result;
}

// 1. The package versions that are in the production bundle.
function shippedVersions() {
    const statsFile = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'raptor-audit-')), 'stats.json');
    const build = run(path.join('node_modules', '.bin', 'webpack'), ['--mode', 'production', '--json=' + statsFile], {
        env: { ...process.env, NODE_ENV: 'production' },
    });
    if (build.status !== 0) {
        console.error(build.stderr || build.stdout);
        throw new Error('the production build failed');
    }

    const stats = JSON.parse(fs.readFileSync(statsFile, 'utf8'));
    const marker = 'node_modules/';
    const shipped = new Set();
    const roots = new Set();

    const walk = (modules) => {
        for (const m of modules || []) {
            const id = String(m.name || m.identifier || '').split(backslash).join('/');
            const at = id.lastIndexOf(marker);
            if (at >= 0) {
                // The innermost package directory of the module path, for example node_modules/a/node_modules/b.
                const rest = id.slice(at + marker.length).split('/');
                const name = rest[0].startsWith('@') ? rest[0] + '/' + rest[1] : rest[0];
                roots.add(id.slice(0, at + marker.length) + name);
            }
            walk(m.modules);
        }
    };
    walk(stats.modules);
    for (const child of stats.children || []) walk(child.modules);

    for (const root of roots) {
        const file = path.resolve(webDir, root.replace('./', ''), 'package.json');
        if (!fs.existsSync(file)) continue;
        const pkg = JSON.parse(fs.readFileSync(file, 'utf8'));
        shipped.add(pkg.name + '@' + pkg.version);
    }
    return shipped;
}

// 2. The advisories of the locked versions.
function advisories() {
    const audit = run('yarn', ['audit', '--json']);
    const found = new Map();
    for (const line of audit.stdout.split('\n')) {
        if (!line.trim()) continue;
        const entry = JSON.parse(line);
        if (entry.type !== 'auditAdvisory') continue;
        const a = entry.data.advisory;
        for (const finding of a.findings || []) {
            const key = `${a.github_advisory_id} ${a.module_name}@${finding.version}`;
            found.set(key, {
                id: a.github_advisory_id,
                severity: a.severity,
                module: a.module_name,
                version: finding.version,
                title: a.title,
                fixed: a.patched_versions,
            });
        }
    }
    return [...found.values()];
}

function exceptions() {
    const file = path.join(webDir, 'audit-exceptions.json');
    if (!fs.existsSync(file)) return new Map();
    return new Map(JSON.parse(fs.readFileSync(file, 'utf8')).map((e) => [e.id, e.reason]));
}

const shipped = shippedVersions();
const accepted = exceptions();
const all = advisories();

const counts = {};
const blocking = [];
const shippedOther = [];
for (const a of all) {
    const isShipped = shipped.has(`${a.module}@${a.version}`);
    const key = `${isShipped ? 'shipped' : 'tooling'} ${a.severity}`;
    counts[key] = (counts[key] || 0) + 1;
    if (!isShipped) continue;
    if (rank[a.severity] >= blockingFrom && !accepted.has(a.id)) blocking.push(a);
    else shippedOther.push(a);
}

console.log(`package versions in the browser bundle: ${shipped.size}`);
console.log('advisories by place and severity:', JSON.stringify(counts));
if (shippedOther.length) {
    console.log('\nshipped, below the blocking level or accepted:');
    for (const a of shippedOther) {
        const why = accepted.get(a.id) ? ` (accepted: ${accepted.get(a.id)})` : '';
        console.log(`  ${a.severity.padEnd(8)} ${a.module}@${a.version} ${a.id} ${a.title}${why}`);
    }
}
if (blocking.length) {
    console.log('\nBLOCKING: high or critical advisories in code that ships to the browser:');
    for (const a of blocking) {
        console.log(`  ${a.severity.padEnd(8)} ${a.module}@${a.version} ${a.id} ${a.title} (fixed in ${a.fixed})`);
    }
    process.exit(1);
}
console.log('\nno blocking findings');
