// Checks external links found in site/dist. Separate from `check` because it needs a network and
// other people's sites go down. Exit codes: 0 when every link answers or the network is
// unreachable (it says so and skips), 1 when a link answers 404 or 410. Use --warn-only to always
// exit 0. Note: github.com links to this repository answer 404 until it is public.
import { promises as fs } from 'node:fs';
import path from 'node:path';

const DIST = path.resolve(import.meta.dirname, '..', 'dist');
const warnOnly = process.argv.includes('--warn-only');

async function* walk(dir) {
  for (const e of await fs.readdir(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) yield* walk(p);
    else yield p;
  }
}

const urls = new Map();
try {
  for await (const file of walk(DIST)) {
    if (!file.endsWith('.html')) continue;
    const html = await fs.readFile(file, 'utf8');
    for (const m of html.matchAll(/\bhref="(https?:\/\/[^"#]+)/g)) {
      const u = m[1].replace(/&amp;/g, '&');
      if (u.startsWith('https://moustafa-elgammal.github.io/agentgw')) continue; // this site: canonical and social URLs, live only after deploy
      if (/^https?:\/\/(localhost|127\.|0\.0\.0\.0|example\.)/.test(u)) continue;
      if (!urls.has(u)) urls.set(u, path.relative(DIST, file));
    }
  }
} catch {
  console.error('check-external: site/dist not found, run `site build` first');
  process.exit(1);
}

async function probe(url) {
  for (const method of ['HEAD', 'GET']) {
    try {
      const r = await fetch(url, { method, redirect: 'follow', signal: AbortSignal.timeout(10000), headers: { 'user-agent': 'agentgw-site-linkcheck' } });
      if (method === 'HEAD' && (r.status === 405 || r.status === 403 || r.status === 501)) continue;
      return { status: r.status };
    } catch (e) {
      if (method === 'GET') return { error: e.cause?.code ?? e.name };
    }
  }
  return { error: 'unknown' };
}

const list = [...urls.keys()];
const results = [];
let next = 0;
await Promise.all(Array.from({ length: 8 }, async () => {
  while (next < list.length) {
    const u = list[next++];
    results.push({ url: u, from: urls.get(u), ...(await probe(u)) });
  }
}));

const bad = results.filter((r) => r.status === 404 || r.status === 410);
const errored = results.filter((r) => r.error);
const ok = results.length - bad.length - errored.length;
console.log(`check-external: ${results.length} links, ${ok} ok, ${bad.length} dead, ${errored.length} unreachable`);
if (errored.length === results.length && results.length > 0) {
  console.log('check-external: no link could be reached, assuming the network is offline. Skipped.');
  process.exit(0);
}
for (const r of errored) console.warn(`  unreachable (${r.error}): ${r.url}  [${r.from}]`);
for (const r of bad) console.error(`  ${r.status}: ${r.url}  [${r.from}]`);
process.exit(bad.length && !warnOnly ? 1 : 0);
