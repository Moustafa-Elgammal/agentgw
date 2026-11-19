// `site check`: the gate CI runs. It fails when
//   - the build prints a warning (warnings are errors here),
//   - the internal link validator reports a broken link (it runs inside the build),
//   - the built output is missing the sitemap, robots.txt or the landing page, or
//   - an asset or link in the HTML escapes the /agentgw/ base.
import { spawnSync } from 'node:child_process';
import { promises as fs } from 'node:fs';
import path from 'node:path';

const SITE = path.resolve(import.meta.dirname, '..');
const DIST = path.join(SITE, 'dist');
const BASE = '/agentgw/';

function run(cmd, args) {
  const r = spawnSync(cmd, args, { cwd: SITE, encoding: 'utf8', env: { ...process.env, FORCE_COLOR: '0', NO_COLOR: '1' } });
  process.stdout.write(r.stdout ?? '');
  process.stderr.write(r.stderr ?? '');
  return r;
}

const failures = [];

await fs.rm(DIST, { recursive: true, force: true });
run('npm', ['run', '--silent', 'sync']);
run('npm', ['run', '--silent', 'og']);
const build = run('npx', ['astro', 'build']);
if (build.status !== 0) {
  console.error('check: the build failed');
  process.exit(1);
}

// Warnings from the build are errors. Lines look like "12:00:00 [WARN] ..." or "warn ...".
const warnLines = `${build.stdout}\n${build.stderr}`.split('\n').filter((l) => /\[warn\]|^\s*warn\b/i.test(l));
if (warnLines.length) failures.push(`the build printed ${warnLines.length} warning(s):\n  ${warnLines.join('\n  ')}`);

for (const f of ['index.html', 'sitemap-index.xml', 'robots.txt', 'favicon.svg', '404.html']) {
  try { await fs.access(path.join(DIST, f)); } catch { failures.push(`dist/${f} is missing`); }
}

// Every root-relative URL in the HTML must live under the base, and its target must exist.
async function* walk(dir) {
  for (const e of await fs.readdir(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) yield* walk(p);
    else yield p;
  }
}
const attr = /\b(?:href|src)="(\/[^"]*)"/g;
let pages = 0;
const outside = new Set();
const missing = new Set();
for await (const file of walk(DIST)) {
  if (!file.endsWith('.html')) continue;
  pages++;
  const html = await fs.readFile(file, 'utf8');
  for (const m of html.matchAll(attr)) {
    const url = m[1].split('#')[0].split('?')[0];
    if (url.startsWith('//')) continue;
    if (!url.startsWith(BASE)) { outside.add(`${path.relative(DIST, file)} -> ${m[1]}`); continue; }
    const rel = url.slice(BASE.length);
    const candidates = [rel, path.join(rel, 'index.html'), `${rel}.html`];
    let ok = false;
    for (const c of candidates) {
      try { if ((await fs.stat(path.join(DIST, c))).isFile()) { ok = true; break; } } catch { /* try next */ }
    }
    if (!ok) missing.add(`${path.relative(DIST, file)} -> ${m[1]}`);
  }
}
if (outside.size) failures.push(`URLs outside ${BASE}:\n  ${[...outside].slice(0, 20).join('\n  ')}`);
if (missing.size) failures.push(`URLs whose target is not in dist:\n  ${[...missing].slice(0, 20).join('\n  ')}`);

if (failures.length) {
  console.error(`\ncheck: FAILED\n- ${failures.join('\n- ')}`);
  process.exit(1);
}
console.log(`\ncheck: ok (${pages} pages)`);
