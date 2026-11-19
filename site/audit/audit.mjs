// Accessibility and layout audit of the built site (site/dist), run by `make site-a11y`.
// For each page, theme and width it checks: axe WCAG 2 A and AA, no horizontal page scroll, no
// console errors or failed requests, every image has alt text, every svg role=img has a name,
// and heading levels do not skip. Screenshots go to AUDIT_OUT (default /out).
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
import http from 'node:http';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

const SRC_DIST = process.env.AUDIT_DIST || '/repo/site/dist';
const OUT = process.env.AUDIT_OUT || '/out';
// Audit a snapshot: a rebuild of site/dist during the run (another build, a watcher) must not
// change what is being checked. Wait briefly for a build in progress to finish writing.
for (let i = 0; i < 30 && !fs.existsSync(path.join(SRC_DIST, 'index.html')); i++) await new Promise((r) => setTimeout(r, 2000));
if (!fs.existsSync(path.join(SRC_DIST, 'index.html'))) {
  console.error(`audit: ${SRC_DIST}/index.html is missing. Build the site first (make site-build).`);
  process.exit(2);
}
const DIST = fs.mkdtempSync(path.join(os.tmpdir(), 'site-dist-'));
fs.cpSync(SRC_DIST, DIST, { recursive: true });
const BASE = '/agentgw';
const PAGES = [
  ['home', '/'],
  ['pipeline', '/concepts/pipeline/'], // mermaid diagram
  ['concept', '/concepts/tiers-and-policy/'],
  ['tutorial', '/guides/04-link-a-player/'],
  ['reference', '/reference/manifest/'],
];
const SCHEMES = ['light', 'dark'];
const WIDTHS = [
  [1280, 900],
  [390, 844],
];
const known = JSON.parse(fs.readFileSync(new URL('./known-issues.json', import.meta.url), 'utf8')).axe;

const types = { '.html': 'text/html', '.js': 'text/javascript', '.mjs': 'text/javascript', '.css': 'text/css', '.svg': 'image/svg+xml', '.png': 'image/png', '.json': 'application/json', '.txt': 'text/plain', '.woff2': 'font/woff2', '.xml': 'application/xml' };
const server = http
  .createServer((req, res) => {
    let p = decodeURIComponent(req.url.split('?')[0]);
    if (p.startsWith(BASE)) p = p.slice(BASE.length) || '/';
    let f = path.join(DIST, p);
    if (p.endsWith('/')) f = path.join(f, 'index.html');
    fs.readFile(f, (e, d) => {
      if (e) {
        res.writeHead(404);
        res.end();
        return;
      }
      res.writeHead(200, { 'content-type': types[path.extname(f)] || 'application/octet-stream' });
      res.end(d);
    });
  })
  .listen(0);
await new Promise((r) => server.once('listening', r));
const origin = `http://127.0.0.1:${server.address().port}`;

for (const [, p] of PAGES) {
  const f = path.join(DIST, p, 'index.html');
  if (!fs.existsSync(f)) {
    console.error(`audit: ${f} is missing. Build the site first (make site-build).`);
    process.exit(2);
  }
}
fs.mkdirSync(OUT, { recursive: true });

const isKnown = (v, node) => known.some((k) => k.rule === v.id && String(node.target?.[0] ?? '').includes(k.selectorContains ?? ''));

const browser = await chromium.launch();
const failures = [];
const ignored = [];
const rows = [];
for (const [name, p] of PAGES) {
  for (const scheme of SCHEMES) {
    for (const [w, h] of WIDTHS) {
      const label = `${name} ${scheme} ${w}px`;
      const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: scheme });
      const page = await ctx.newPage();
      const problems = [];
      page.on('pageerror', (e) => problems.push(`page error: ${e.message}`));
      page.on('console', (m) => m.type() === 'error' && problems.push(`console error: ${m.text()}`));
      page.on('response', (r) => r.status() >= 400 && problems.push(`HTTP ${r.status()} for ${r.url().replace(origin, '')}`));
      await page.goto(`${origin}${BASE}${p}`, { waitUntil: 'networkidle' });
      if (name === 'pipeline') {
        try {
          await page.waitForSelector('pre.mermaid svg', { timeout: 15000 });
        } catch {
          problems.push('the mermaid diagram did not render to an svg');
        }
      }
      await page.waitForTimeout(400);

      const theme = await page.evaluate(() => document.documentElement.dataset.theme);
      if (theme && theme !== scheme) problems.push(`theme is ${theme}, expected ${scheme}`);

      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      if (overflow > 1) {
        const wide = await page.evaluate(() => {
          const vw = document.documentElement.clientWidth;
          return [...document.querySelectorAll('body *')]
            .filter((e) => e.getBoundingClientRect().right > vw + 1 && getComputedStyle(e).position !== 'fixed')
            .slice(0, 3)
            .map((e) => `${e.tagName.toLowerCase()}${e.className && typeof e.className === 'string' ? '.' + e.className.split(' ')[0] : ''}`);
        });
        problems.push(`horizontal page scroll by ${overflow}px (widest: ${wide.join(', ')})`);
      }

      const noAlt = await page.evaluate(() => [...document.querySelectorAll('img:not([alt])')].map((i) => i.getAttribute('src')));
      if (noAlt.length) problems.push(`images without alt: ${noAlt.join(', ')}`);
      const unnamed = await page.evaluate(() =>
        [...document.querySelectorAll('svg[role="img"]')].filter((s) => !s.getAttribute('aria-label') && !s.getAttribute('aria-labelledby') && !s.querySelector('title')).length,
      );
      if (unnamed) problems.push(`${unnamed} svg role=img without a name`);

      const levels = await page.evaluate(() => [...document.querySelectorAll('main h1, main h2, main h3, main h4, main h5, main h6')].map((e) => Number(e.tagName[1])));
      if (levels.filter((l) => l === 1).length !== 1) problems.push(`expected one h1 in main, found ${levels.filter((l) => l === 1).length}`);
      for (let i = 1; i < levels.length; i++) if (levels[i] > levels[i - 1] + 1) problems.push(`heading level jumps from h${levels[i - 1]} to h${levels[i]}`);

      const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa']).analyze();
      for (const v of axe.violations) {
        for (const n of v.nodes) {
          const msg = `axe ${v.id} (${v.impact}): ${n.target?.[0]} | ${String(n.failureSummary ?? '').split('\n').slice(1, 2).join(' ').trim()}`;
          (isKnown(v, n) ? ignored : problems).push(isKnown(v, n) ? `${label}: ${msg}` : msg);
        }
      }

      await page.screenshot({ path: path.join(OUT, `${name}-${scheme}-${w}.png`), fullPage: true });
      rows.push(`${problems.length ? 'FAIL' : 'ok  '} ${label}${problems.length ? ` (${problems.length})` : ''}`);
      for (const pr of problems) failures.push(`${label}: ${pr}`);
      await ctx.close();
    }
  }
}
await browser.close();
server.close();

console.log(rows.join('\n'));
if (ignored.length) console.log(`\n${ignored.length} known issue(s) ignored (site/audit/known-issues.json)`);
if (failures.length) {
  console.log(`\n${failures.length} problem(s):`);
  const seen = new Set();
  for (const f of failures) {
    const key = f.replace(/^[^:]+: /, '');
    if (seen.has(key)) continue; // the same finding on several widths and themes
    seen.add(key);
    console.log(`  - ${f}`);
  }
  fs.writeFileSync(path.join(OUT, 'failures.txt'), failures.join('\n') + '\n');
  process.exit(1);
}
console.log(`\naudit: ${PAGES.length * SCHEMES.length * WIDTHS.length} page views clean. Screenshots in ${OUT}`);
