// Renders the installed app's icons (R90, ADR 0037) from static/favicon.svg
// (R82e) in Chromium, as the favicon's PNGs were:
//   - static/icons/icon-192.png and icon-512.png: the favicon as it is, a
//     white pager on a rounded square of Harbour's green ("any");
//   - static/icons/maskable-192.png and maskable-512.png: the green to every
//     edge and the pager smaller, inside the middle 80% circle a launcher's
//     mask always keeps ("maskable", for Android's adaptive icons);
//   - static/apple-touch-icon.png (180): the green to every edge, the pager
//     the favicon's size, for iOS to round itself (a transparent corner shows
//     black there).
// Run by hand when favicon.svg changes; the PNGs are committed.
//
// usage: node scripts/app-icons.mjs
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { chromium } from '@playwright/test';

const web = fileURLToPath(new URL('..', import.meta.url));
const favicon = readFileSync(`${web}static/favicon.svg`, 'utf8');

// The favicon's first shape is its rounded square; the rest is the pager.
const shapes = favicon.slice(favicon.indexOf('>', favicon.indexOf('<svg')) + 1, favicon.lastIndexOf('</svg>')).trim().split('\n').map((l) => l.trim());
const [square, ...pager] = shapes;
const fullSquare = square.replace(/\s+rx="[^"]*"/, '');
if (fullSquare === square) throw new Error('favicon.svg: its first shape has no rx to remove');

const svg = (body) => `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">${body}</svg>`;
// The pager spans x 4-28 and y 6-26: from the middle, its far corner is
// 15.6 units away. At 0.75 that's 11.7, inside the safe circle's 12.8.
const any = svg(shapes.join(''));
const maskable = svg(fullSquare + `<g transform="translate(16 16) scale(0.75) translate(-16 -16)">${pager.join('')}</g>`);
const touch = svg(fullSquare + pager.join(''));

const outputs = [
	['static/icons/icon-192.png', any, 192],
	['static/icons/icon-512.png', any, 512],
	['static/icons/maskable-192.png', maskable, 192],
	['static/icons/maskable-512.png', maskable, 512],
	['static/apple-touch-icon.png', touch, 180]
];

const browser = await chromium.launch();
for (const [file, image, size] of outputs) {
	const page = await browser.newPage({ viewport: { width: size, height: size } });
	await page.setContent(`<html><body style="margin:0">${image.replace('<svg ', `<svg width="${size}" height="${size}" `)}</body></html>`);
	await page.screenshot({ path: `${web}${file}`, omitBackground: true, clip: { x: 0, y: 0, width: size, height: size } });
	await page.close();
	console.log(`wrote ${file} (${size} x ${size})`);
}
await browser.close();
