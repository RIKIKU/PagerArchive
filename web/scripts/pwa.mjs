// Writes the installable app's files from the Harbour look's tokens in
// src/app.css (R90, ADR 0037), so their colours and sizes are tokens too
// (R82c):
//   - static/manifest.webmanifest: the web app manifest;
//   - static/offline.html: the page the service worker shows when a page
//     can't be fetched (src/service-worker.ts);
//   - src/app.html's theme-color lines, between its "pwa" markers: the
//     installed app's title and status bar before the app has started
//     (src/lib/statusBar.ts follows the look and theme after that).
//
// usage: node scripts/pwa.mjs [--check]
// --check: fail if any of them isn't what this would write (CI).
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const web = fileURLToPath(new URL('..', import.meta.url));
const css = readFileSync(`${web}src/app.css`, 'utf8');

/** A token's value as Harbour, the default look and the first in app.css, sets it. */
function token(name) {
	const m = new RegExp(`^\\s*${name}:\\s*([^;]+);`, 'm').exec(css);
	if (!m) throw new Error(`app.css has no ${name}`);
	return m[1].trim();
}

/** A colour token's light and dark values, from light-dark(light, dark). */
function colours(name) {
	const v = token(name);
	const m = /^light-dark\((#[0-9a-f]{6}),\s*(#[0-9a-f]{6})\)$/i.exec(v);
	if (m) return { light: m[1].toLowerCase(), dark: m[2].toLowerCase() };
	if (/^#[0-9a-f]{6}$/i.test(v)) return { light: v.toLowerCase(), dark: v.toLowerCase() };
	throw new Error(`${name} isn't a colour pair: ${v}`);
}

// The top bar is --surface, the page behind it --bg (routes/+layout.svelte).
// A manifest has one colour of each, so it has the light theme's; the
// theme-color lines give dark screens theirs.
const bar = colours('--surface');
const page = colours('--bg');

const manifest =
	JSON.stringify(
		{
			id: '/',
			name: 'PagerArchive',
			short_name: 'PagerArchive',
			description: 'An archive and live browser for a POCSAG paging network.',
			lang: 'en-AU',
			dir: 'ltr',
			start_url: '/',
			scope: '/',
			display: 'standalone',
			theme_color: bar.light,
			background_color: page.light,
			icons: [
				{ src: '/icons/icon-192.png', sizes: '192x192', type: 'image/png', purpose: 'any' },
				{ src: '/icons/icon-512.png', sizes: '512x512', type: 'image/png', purpose: 'any' },
				{ src: '/icons/maskable-192.png', sizes: '192x192', type: 'image/png', purpose: 'maskable' },
				{ src: '/icons/maskable-512.png', sizes: '512x512', type: 'image/png', purpose: 'maskable' }
			]
		},
		null,
		'\t'
	) + '\n';

// The offline page's tokens, copied from Harbour as they are: light-dark()
// picks the system's theme, as on the sign-in page.
const used = [
	'--bg',
	'--fg',
	'--muted',
	'--accent',
	'--accent-fg',
	'--accent-hover',
	'--focus',
	'--font-ui',
	'--text-base',
	'--text-2xl',
	'--weight-strong',
	'--leading',
	'--leading-tight',
	'--space-sm',
	'--space-lg',
	'--space-3xl',
	'--page-pad-phone',
	'--measure-narrow',
	'--icon-size-large',
	'--radius-button',
	'--target-min',
	'--button-pad-x',
	'--focus-width',
	'--focus-offset'
];
const offline = `<!doctype html>
<!-- Written by scripts/pwa.mjs from src/app.css: do not edit by hand (R90). -->
<html lang="en-AU">
	<head>
		<meta charset="utf-8" />
		<meta name="viewport" content="width=device-width, initial-scale=1" />
		<meta name="color-scheme" content="light dark" />
		<meta name="referrer" content="no-referrer" />
		<title>You're offline · PagerArchive</title>
		<link rel="icon" href="/favicon.svg" type="image/svg+xml" />
		<style>
			:root {
				color-scheme: light dark;
${used.map((t) => `				${t}: ${token(t)};`).join('\n')}
			}
			body {
				margin: 0;
				background: var(--bg);
				color: var(--fg);
				font-family: var(--font-ui);
				font-size: var(--text-base);
				line-height: var(--leading);
			}
			main {
				max-width: var(--measure-narrow);
				margin: 0 auto;
				padding: var(--space-3xl) var(--page-pad-phone);
			}
			img {
				width: var(--icon-size-large);
				height: var(--icon-size-large);
			}
			h1 {
				font-size: var(--text-2xl);
				font-weight: var(--weight-strong);
				line-height: var(--leading-tight);
				margin: var(--space-lg) 0 var(--space-sm);
			}
			p {
				color: var(--muted);
				margin: 0 0 var(--space-lg);
			}
			a {
				display: inline-flex;
				align-items: center;
				min-height: var(--target-min);
				padding: 0 var(--button-pad-x);
				border-radius: var(--radius-button);
				background: var(--accent);
				color: var(--accent-fg);
				font-weight: var(--weight-strong);
				text-decoration: none;
			}
			a:hover {
				background: var(--accent-hover);
			}
			a:focus-visible {
				outline: var(--focus-width) solid var(--focus);
				outline-offset: var(--focus-offset);
			}
		</style>
	</head>
	<body>
		<main>
			<img src="/favicon.svg" alt="" />
			<h1>You're offline</h1>
			<p>PagerArchive needs a connection to show pages. Nothing from the archive is kept on this device.</p>
			<a href="">Try again</a>
		</main>
	</body>
</html>
`;

const metaStart = '<!-- pwa: written by scripts/pwa.mjs from app.css -->';
const metaEnd = '<!-- /pwa -->';
const metas = [
	metaStart,
	`<meta name="theme-color" content="${bar.light}" media="(prefers-color-scheme: light)" />`,
	`<meta name="theme-color" content="${bar.dark}" media="(prefers-color-scheme: dark)" />`,
	metaEnd
];
const appHtml = readFileSync(`${web}src/app.html`, 'utf8');
const a = appHtml.indexOf(metaStart);
const b = appHtml.indexOf(metaEnd);
if (a < 0 || b < a) throw new Error(`src/app.html has no "${metaStart}" … "${metaEnd}" block`);
const indent = appHtml.slice(appHtml.lastIndexOf('\n', a) + 1, a);
const appHtmlOut = appHtml.slice(0, a) + metas.join(`\n${indent}`) + appHtml.slice(b + metaEnd.length);

const outputs = [
	['static/manifest.webmanifest', manifest],
	['static/offline.html', offline],
	['src/app.html', appHtmlOut]
];

if (process.argv.includes('--check')) {
	let bad = 0;
	for (const [file, text] of outputs) {
		let have = '';
		try {
			have = readFileSync(`${web}${file}`, 'utf8');
		} catch {
			// missing: different
		}
		if (have !== text) {
			console.error(`${file} is not what scripts/pwa.mjs writes from src/app.css: run \`node scripts/pwa.mjs\``);
			bad++;
		}
	}
	if (bad) process.exit(1);
	console.log(`${outputs.map(([f]) => f).join(', ')}: as written from src/app.css`);
} else {
	for (const [file, text] of outputs) writeFileSync(`${web}${file}`, text);
	console.log(`wrote ${outputs.map(([f]) => f).join(', ')} from src/app.css`);
}
