// Builds the social preview image (public/og.png, 1200x630) from an inline SVG with sharp.
// It is generated, not committed. A failure here is not fatal: the page just has no image.
import { promises as fs } from 'node:fs';
import path from 'node:path';

const out = path.resolve(import.meta.dirname, '..', 'public', 'og.png');
const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="630" viewBox="0 0 1200 630">
  <rect width="1200" height="630" fill="#0b1f1e"/>
  <rect x="64" y="64" width="96" height="96" rx="22" fill="#0f766e"/>
  <path d="M88 112h16m24 0h16" stroke="#fff" stroke-width="8" stroke-linecap="round"/>
  <rect x="104" y="84" width="24" height="56" rx="8" fill="none" stroke="#fff" stroke-width="8"/>
  <text x="188" y="134" font-family="DejaVu Sans, Arial, sans-serif" font-size="56" font-weight="700" fill="#e6fffb">agentgw</text>
  <text x="64" y="340" font-family="DejaVu Sans, Arial, sans-serif" font-size="68" font-weight="700" fill="#ffffff">Let a player's AI agent</text>
  <text x="64" y="424" font-family="DejaVu Sans, Arial, sans-serif" font-size="68" font-weight="700" fill="#2dd4bf">play your game, safely.</text>
  <text x="64" y="540" font-family="DejaVu Sans, Arial, sans-serif" font-size="30" fill="#9ccfc9">Manifest to MCP tools. Policy, signed calls, confirmations, audit.</text>
</svg>`;

try {
  const { default: sharp } = await import('sharp');
  await sharp(Buffer.from(svg)).png().toFile(out);
  console.log('og: wrote public/og.png');
} catch (e) {
  console.warn(`og: skipped (${e.message})`);
  await fs.rm(out, { force: true });
}
