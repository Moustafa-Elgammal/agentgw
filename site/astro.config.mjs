import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightLinksValidator from 'starlight-links-validator';
import { copyFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';

const SITE = 'https://moustafa-elgammal.github.io';
const BASE = '/agentgw';
const DESCRIPTION =
  'An open-source gateway that lets a player\'s own AI agent act safely inside a game: tools from a manifest, policy, signed backend calls, player confirmations and an audit log.';

export default defineConfig({
  site: SITE,
  base: BASE,
  trailingSlash: 'always',
  integrations: [
    {
      // GitHub Pages serves /404.html for unknown paths, Astro writes /404/index.html.
      name: 'copy-404',
      hooks: {
        'astro:build:done': async ({ dir }) => {
          const root = fileURLToPath(dir);
          await copyFile(`${root}404/index.html`, `${root}404.html`);
        },
      },
    },
    starlight({
      title: 'agentgw',
      description: DESCRIPTION,
      favicon: '/favicon.svg',
      social: [{ icon: 'github', label: 'GitHub', href: 'https://github.com/Moustafa-Elgammal/agentgw' }],
      customCss: ['./src/styles/custom.css'],
      lastUpdated: false,
      disable404Route: true, // the page is src/content/docs/404.md
      head: [
        { tag: 'meta', attrs: { property: 'og:site_name', content: 'agentgw' } },
        { tag: 'meta', attrs: { property: 'og:type', content: 'website' } },
        { tag: 'meta', attrs: { property: 'og:image', content: `${SITE}${BASE}/og.png` } },
        { tag: 'meta', attrs: { property: 'og:image:alt', content: 'agentgw: let a player\'s AI agent play your game, safely.' } },
        { tag: 'meta', attrs: { name: 'twitter:card', content: 'summary_large_image' } },
        { tag: 'meta', attrs: { name: 'twitter:image', content: `${SITE}${BASE}/og.png` } },
        { tag: 'meta', attrs: { name: 'theme-color', content: '#0f766e' } },
      ],
      sidebar: [
        { label: 'Get started', items: [{ autogenerate: { directory: 'get-started' } }] },
        { label: 'Concepts', items: [{ autogenerate: { directory: 'concepts' } }] },
        { label: 'Reference', items: [{ autogenerate: { directory: 'reference' } }] },
        { label: 'Operate', items: [{ autogenerate: { directory: 'operate' } }] },
        { label: 'Project', items: [{ autogenerate: { directory: 'project' } }] },
      ],
      plugins: [
        starlightLinksValidator({
          // Links in the synced docs that point at GitHub files are absolute URLs and are
          // not checked here; see `check-external` for those.
          errorOnRelativeLinks: false,
          // Docs mention example URLs such as http://127.0.0.1:8080/mcp. They are not links.
          errorOnLocalLinks: false,
        }),
      ],
    }),
  ],
});
