// Copies docs/** and a few root files into site/src/content/docs/, so the site and the
// repository never disagree. The sources stay plain Markdown that reads fine on GitHub.
//
// For each file it: takes `title` from the first H1 and removes that H1, adds `editUrl` and a
// sidebar order, and rewrites relative links (to a site route when the target is a page here,
// to a GitHub URL otherwise). Expected pages that do not exist yet get a short placeholder and a
// warning, so the build does not fail while they are being written.
import { promises as fs } from 'node:fs';
import path from 'node:path';

const REPO = process.env.REPO_ROOT ?? path.resolve(import.meta.dirname, '..', '..');
const OUT = path.join(REPO, 'site', 'src', 'content', 'docs');
const BASE = '/agentgw';
const GITHUB = 'https://github.com/Moustafa-Elgammal/agentgw';
const BRANCH = 'main';
const MANIFEST_FILE = path.join(OUT, '.synced.json');

// docs/ files that are internal or about process. They stay in the repository only.
const EXCLUDE = new Set(['OPEN-SOURCE-PLAN.md', 'REPO-SETTINGS.md', 'AUDIT-REPORT.md', 'LAUNCH-CHECKLIST.md']);

// Top level files, mapped to a route. Everything else under docs/<dir>/<name>.md keeps its path.
const FILE_ROUTES = new Map([
  ['docs/ARCHITECTURE.md', 'concepts/architecture'],
  ['docs/SECURITY-MODEL.md', 'concepts/security-model'],
  ['docs/FEATURES.md', 'concepts/features'],
  ['docs/PROTOCOL.md', 'reference/protocol'],
  ['docs/MANIFEST.md', 'reference/manifest-guide'],
  ['docs/CLAUDE-DESKTOP.md', 'get-started/claude-desktop'],
  ['docs/RUNBOOK.md', 'operate/runbook'],
  ['docs/RELEASING.md', 'operate/releasing'],
  ['docs/DECISIONS.md', 'project/decisions'],
  ['docs/ROADMAP.md', 'project/roadmap'],
  ['docs/THIRD-PARTY-LICENSES.md', 'project/third-party-licenses'],
  ['docs/design/oidc-consent.md', 'project/design-oidc-consent'],
  ['CHANGELOG.md', 'project/changelog'],
  ['CONTRIBUTING.md', 'project/contributing'],
  ['SECURITY.md', 'project/security'],
  ['CODE_OF_CONDUCT.md', 'project/code-of-conduct'],
]);

// Pages other work is writing. A placeholder is generated until the file exists.
const EXPECTED = new Map([
  ['docs/get-started/overview.md', 'Overview'],
  ['docs/get-started/quickstart.md', 'Quickstart'],
  ['docs/get-started/connect-an-agent.md', 'Connect an agent'],
  ['docs/get-started/glossary.md', 'Glossary'],
  ['docs/concepts/pipeline.md', 'How a call flows'],
  ['docs/concepts/tiers-and-policy.md', 'Tiers, scopes and policy'],
  ['docs/concepts/players-grants-confirmations.md', 'Players, grants and confirmations'],
  ['docs/reference/manifest.md', 'Manifest reference'],
  ['docs/reference/flags.md', 'Gateway flags and environment'],
  ['docs/reference/errors.md', 'Result and error codes'],
  ['docs/reference/audit-events.md', 'Audit event fields'],
  ['docs/reference/studio-api.md', 'Studio and admin API'],
  ['docs/tutorials/01-play-the-example-game.md', '1. Play the example game with an agent'],
  ['docs/tutorials/02-describe-your-actions.md', '2. Describe your game\'s actions'],
  ['docs/tutorials/03-accept-signed-calls.md', '3. Make your backend accept signed calls'],
  ['docs/tutorials/04-link-a-player.md', '4. Link a player'],
  ['docs/tutorials/05-ask-before-big-actions.md', '5. Ask the player before big actions'],
  ['docs/tutorials/06-go-live.md', '6. Go live'],
  ['docs/tutorials/07-test-your-integration.md', '7. Test your integration'],
  ['docs/tutorials/08-sign-in-with-your-identity-provider.md', '8. Sign in with your identity provider'],
  ['docs/tutorials/09-run-it-on-kubernetes.md', '9. Run it on Kubernetes'],
  ['docs/tutorials/10-connect-a-unity-game-through-the-relay.md', '10. Connect a Unity game through the relay'],
  ['docs/tutorials/11-run-it-on-docker-desktop-kubernetes.md', '11. Run it on Docker Desktop'],
]);

// Title overrides, by route, where the H1 would be ambiguous in the sidebar.
const TITLES = new Map([['reference/manifest-guide', 'Manifest guide']]);

const ORDER = [
  'guides/start-here',
  'guides/01-play-the-example-game', 'guides/02-describe-your-actions', 'guides/03-accept-signed-calls',
  'guides/04-link-a-player', 'guides/05-ask-before-big-actions', 'guides/06-go-live',
  'guides/07-test-your-integration', 'guides/08-sign-in-with-your-identity-provider',
  'guides/09-run-it-on-kubernetes',
  'guides/10-connect-a-unity-game-through-the-relay',
  'guides/11-run-it-on-docker-desktop-kubernetes',
  'guides/best-practices', 'guides/cookbook', 'guides/troubleshooting', 'guides/faq',
  'get-started/overview', 'get-started/quickstart', 'get-started/connect-an-agent', 'get-started/claude-desktop', 'get-started/glossary',
  'concepts/pipeline', 'concepts/tiers-and-policy', 'concepts/players-grants-confirmations',
  'concepts/architecture', 'concepts/security-model', 'concepts/features',
  'reference/manifest', 'reference/manifest-guide', 'reference/protocol', 'reference/studio-api',
  'reference/flags', 'reference/errors', 'reference/audit-events',
  'operate/runbook', 'operate/releasing',
  'project/roadmap', 'project/decisions', 'project/changelog', 'project/contributing',
  'project/security', 'project/code-of-conduct', 'project/third-party-licenses',
];

const warnings = [];
const warn = (m) => { warnings.push(m); console.warn(`sync: warning: ${m}`); };

async function exists(p) {
  try { await fs.access(p); return true; } catch { return false; }
}

async function* walk(dir) {
  for (const e of await fs.readdir(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) yield* walk(p);
    else yield p;
  }
}

function routeFor(rel) {
  if (FILE_ROUTES.has(rel)) return FILE_ROUTES.get(rel);
  // Guides and tutorials share one sidebar section. The tutorials keep their numbers, so they sort
  // in order, and docs/guides/index.md is the section's first page (an "index" route would collide
  // with the section root).
  if (rel === 'docs/guides/index.md') return 'guides/start-here';
  const t = /^docs\/tutorials\/(\d{2}-[^/]+)\.md$/.exec(rel);
  if (t) return `guides/${t[1]}`;
  const m = /^docs\/([^/]+)\/(.+)\.md$/.exec(rel);
  if (m && m[2] !== 'index') return `${m[1]}/${m[2]}`;
  return null;
}

const fenceRe = /^\s*(```|~~~)/;
const inlineLinkRe = /(!?\[[^\]]*\]\()(<[^>]+>|[^)\s]+)((?:\s+"[^"]*")?\))/g;
const refDefRe = /^(\s*\[[^\]]+\]:\s*)(\S+)/;

function rewriteHref(href, srcRel, routes, known) {
  if (/^([a-z][a-z0-9+.-]*:|#|\/\/)/i.test(href)) return href;
  const [pathPart, ...anchorParts] = href.split('#');
  const anchor = anchorParts.length ? `#${anchorParts.join('#')}` : '';
  if (pathPart === '') return href;
  const resolved = path.posix.normalize(path.posix.join(path.posix.dirname(srcRel), pathPart));
  if (resolved.startsWith('..')) return href;
  if (routes.has(resolved)) return `${BASE}/${routes.get(resolved)}/${anchor}`;
  if (known.has(resolved)) return `${GITHUB}/blob/${BRANCH}/${resolved}${anchor}`;
  if (known.has(`${resolved}/`)) return `${GITHUB}/tree/${BRANCH}/${resolved.replace(/\/$/, '')}${anchor}`;
  warn(`${srcRel}: link target not found: ${href}`);
  return href;
}

function transform(body, srcRel, routes, known) {
  const lines = body.split('\n');
  let inFence = false;
  let title = null;
  const out = [];
  for (let i = 0; i < lines.length; i++) {
    let line = lines[i];
    if (fenceRe.test(line)) {
      inFence = !inFence;
      out.push(line);
      continue;
    }
    if (inFence) { out.push(line); continue; }
    const h1 = /^# +(.+?)\s*#*\s*$/.exec(line);
    if (h1 && title === null) {
      title = h1[1].replace(/`/g, '');
      if (lines[i + 1] === '') i++; // drop the blank line after the title
      continue;
    }
    line = line.replace(inlineLinkRe, (_m, a, href, c) => {
      const bare = href.startsWith('<') ? href.slice(1, -1) : href;
      if (a.startsWith('!')) return `${a}${href}${c}`;
      return `${a}${rewriteHref(bare, srcRel, routes, known)}${c}`;
    });
    line = line.replace(refDefRe, (_m, a, href) => `${a}${rewriteHref(href, srcRel, routes, known)}`);
    out.push(line);
  }
  return { title, text: out.join('\n') };
}

function frontMatter({ title, srcRel, route, placeholder }) {
  const order = ORDER.indexOf(route);
  const lines = ['---', `title: ${JSON.stringify(TITLES.get(route) ?? title)}`];
  if (!placeholder) lines.push(`editUrl: ${GITHUB}/edit/${BRANCH}/${srcRel}`);
  else lines.push('editUrl: false');
  if (order >= 0) lines.push('sidebar:', `  order: ${order + 1}`);
  lines.push('---', '');
  return lines.join('\n');
}

async function main() {
  // Remove what the previous run wrote, and only that. index.mdx and other hand written pages stay.
  if (await exists(MANIFEST_FILE)) {
    for (const f of JSON.parse(await fs.readFile(MANIFEST_FILE, 'utf8'))) {
      await fs.rm(path.join(OUT, f), { force: true });
    }
  }
  await fs.mkdir(OUT, { recursive: true });

  // Every file in the repository that a link may point at.
  const known = new Set();
  for await (const p of walk(REPO)) {
    const rel = path.relative(REPO, p).split(path.sep).join('/');
    if (/^(\.git|site\/(node_modules|dist|\.astro))\//.test(rel)) continue;
    known.add(rel);
    let d = path.posix.dirname(rel);
    while (d !== '.') { known.add(`${d}/`); d = path.posix.dirname(d); }
  }

  // Sources: docs/*.md, docs/<dir>/*.md and the root files.
  const sources = [];
  for await (const p of walk(path.join(REPO, 'docs'))) {
    const rel = path.relative(REPO, p).split(path.sep).join('/');
    if (!rel.endsWith('.md')) continue;
    if (EXCLUDE.has(path.posix.basename(rel)) && path.posix.dirname(rel) === 'docs') continue;
    sources.push(rel);
  }
  for (const rel of FILE_ROUTES.keys()) {
    if (!rel.startsWith('docs/') && (await exists(path.join(REPO, rel)))) sources.push(rel);
  }

  const routes = new Map();
  for (const rel of sources) {
    const r = routeFor(rel);
    if (r === null) { warn(`${rel}: no route, skipped`); continue; }
    routes.set(rel, r);
  }
  // Expected pages count as link targets even before they exist.
  for (const rel of EXPECTED.keys()) if (!routes.has(rel)) routes.set(rel, routeFor(rel));

  const seen = new Map();
  const written = [];
  const put = async (route, content, from) => {
    if (seen.has(route)) throw new Error(`two sources map to ${route}: ${seen.get(route)} and ${from}`);
    seen.set(route, from);
    const file = `${route}.md`;
    const dest = path.join(OUT, file);
    await fs.mkdir(path.dirname(dest), { recursive: true });
    await fs.writeFile(dest, content);
    written.push(file);
  };

  for (const rel of sources) {
    const route = routes.get(rel);
    if (!route) continue;
    const raw = await fs.readFile(path.join(REPO, rel), 'utf8');
    if (raw.startsWith('---\n')) { warn(`${rel}: has front matter, skipped`); continue; }
    const { title, text } = transform(raw, rel, routes, known);
    await put(route, frontMatter({ title: title ?? path.posix.basename(rel, '.md'), srcRel: rel, route }) + text, rel);
  }

  for (const [rel, title] of EXPECTED) {
    if (sources.includes(rel)) continue;
    const route = routes.get(rel);
    warn(`${rel} does not exist yet, writing a placeholder page`);
    await put(route, frontMatter({ title, srcRel: rel, route, placeholder: true }) +
      ':::note\nThis page is not written yet. It will appear here when it is added to `' + rel + '`.\n:::\n', `placeholder for ${rel}`);
  }

  await fs.writeFile(MANIFEST_FILE, JSON.stringify(written.sort(), null, 2) + '\n');
  console.log(`sync: wrote ${written.length} pages to site/src/content/docs (${warnings.length} warnings)`);
}

main().catch((e) => { console.error(e); process.exit(1); });
