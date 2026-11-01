/// <reference types="node" />
// M13 prototypes (ADR 0015), throwaway: every look passes axe and WCAG AA
// contrast in light and dark, the structure is R77-R81a's, and, with
// PA_PROTO_SHOTS set to a folder, screenshots of each look are written there.
import { test, expect, type Page } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import fs from 'node:fs';
import path from 'node:path';

const looks = ['ledger', 'console', 'harbour'] as const;
const themes = ['light', 'dark'] as const;
const url = (look: string, theme: string, extra = '') => `/prototype?look=${look}&theme=${theme}&bare=1${extra}`;

async function axe(page: Page) {
	const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
	return results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join(', ')}`);
}

// Each view at the width it's for: the list with a message in the pane, the
// shrunk menu with a group's panel open, a placeholder page with switches,
// and on a phone the list, a message and the More menu.
const views: { name: string; width: number; extra: string; focus?: string }[] = [
	{ name: 'list and pane', width: 1280, extra: '&m=1' },
	{ name: 'shrunk menu, panel open', width: 1920, extra: '&menu=shrunk&m=3', focus: '.p-menu .head' },
	{ name: 'integrations', width: 1280, extra: '&page=integrations' },
	{ name: 'template, three levels deep', width: 1280, extra: '&page=template-fire' },
	{ name: 'phone list', width: 375, extra: '' },
	{ name: 'phone message', width: 375, extra: '&m=3' },
	{ name: 'phone More', width: 375, extra: '&more=1' }
];

for (const look of looks) {
	for (const theme of themes) {
		test(`prototype ${look}, ${theme}: axe in every view`, async ({ page }) => {
			const failures: string[] = [];
			for (const v of views) {
				await page.setViewportSize({ width: v.width, height: v.width === 375 ? 812 : 900 });
				await page.goto(url(look, theme, v.extra));
				await expect(page.locator('.p-shell')).toBeVisible();
				if (v.focus) await page.locator(v.focus).first().focus();
				for (const f of await axe(page)) failures.push(`${v.name}: ${f}`);
			}
			expect(failures).toEqual([]);
		});
	}
}

// WCAG 2.1 AA: 4.5:1 for text (1.4.3), 3:1 for focus rings, control borders
// and the marks that show priority (1.4.11). Each token is resolved by the
// browser, as the page draws it.
const colourTokens = [
	...['bg', 'surface', 'surface-2', 'fg', 'muted', 'control-border', 'accent', 'accent-fg', 'accent-hover', 'accent-pressed'],
	...['accent-soft', 'accent-soft-fg', 'hover', 'focus', 'warn-bg', 'warn-fg', 'heartbeat-fg', 'plain-row', 'plain-row-hover'],
	...['emergency', 'nonemergency', 'admin'].flatMap((p) => ['edge', 'dot', 'row', 'row-hover', 'badge-bg', 'badge-fg'].map((t) => `${p}-${t}`))
];
type Pair = { what: string; fg: string; on: string[]; min: number };
function pairs(look: Record<string, string>): Pair[] {
	const list: Pair[] = [
		{ what: 'Text', fg: 'fg', on: ['bg', 'surface', 'surface-2', 'plain-row-hover'], min: 4.5 },
		{ what: 'Secondary text', fg: 'muted', on: ['bg', 'surface', 'surface-2', 'plain-row-hover'], min: 4.5 },
		{ what: 'Links', fg: 'accent', on: ['bg', 'surface', 'surface-2'], min: 4.5 },
		{ what: 'Button text', fg: 'accent-fg', on: ['accent', 'accent-hover', 'accent-pressed'], min: 4.5 },
		{ what: 'Selected or pressed', fg: 'accent-soft-fg', on: ['accent-soft'], min: 4.5 },
		{ what: 'Text on hover', fg: 'fg', on: ['hover'], min: 4.5 },
		{ what: 'Tooltip', fg: 'bg', on: ['fg'], min: 4.5 },
		{ what: 'Flags', fg: 'warn-fg', on: ['warn-bg'], min: 4.5 },
		{ what: 'Heartbeat', fg: 'heartbeat-fg', on: ['bg', 'surface-2'], min: 4.5 },
		{ what: 'Focus ring', fg: 'focus', on: ['bg', 'surface', 'surface-2'], min: 3 },
		{ what: 'Control borders', fg: 'control-border', on: ['bg', 'surface', 'surface-2'], min: 3 },
		{ what: 'Switch on', fg: 'accent', on: ['surface-2'], min: 3 }
	];
	for (const p of ['emergency', 'nonemergency', 'admin']) {
		const rows = [`${p}-row`, `${p}-row-hover`];
		const tinted = look[`${p}-badge-bg`] !== 'transparent';
		list.push(
			{ what: `${p}: label`, fg: `${p}-badge-fg`, on: tinted ? [`${p}-badge-bg`] : [...rows, 'surface-2'], min: 4.5 },
			{ what: `${p}: text on its row`, fg: 'fg', on: rows, min: 4.5 },
			{ what: `${p}: secondary text and links on its row`, fg: 'muted', on: rows, min: 4.5 },
			{ what: `${p}: job link on its row`, fg: 'accent', on: rows, min: 4.5 },
			{ what: `${p}: selected row's ring`, fg: 'accent', on: rows, min: 3 }
		);
		if (look[`${p}-edge`] !== 'transparent') list.push({ what: `${p}: row edge`, fg: `${p}-edge`, on: [...rows, 'bg'], min: 3 });
		if (look['badge-dot'] !== 'none') list.push({ what: `${p}: dot`, fg: `${p}-dot`, on: tinted ? [`${p}-badge-bg`] : [...rows, 'surface-2'], min: 3 });
	}
	return list;
}

test('prototype: WCAG AA contrast of every look, light and dark', async ({ page }) => {
	const report: string[] = [];
	const failures: string[] = [];
	for (const look of looks) {
		for (const theme of themes) {
			await page.goto(url(look, theme));
			await expect(page.locator('.p-shell')).toBeVisible();
			// Each colour token as the page resolves it (light-dark() picks one).
			const tokens: Record<string, string> = await page.evaluate((names) => {
				const root = getComputedStyle(document.documentElement);
				const probe = document.createElement('span');
				document.querySelector('.p-shell')!.append(probe);
				const out: Record<string, string> = { 'badge-dot': root.getPropertyValue('--badge-dot').trim() };
				for (const n of names) {
					probe.style.color = `var(--${n})`;
					out[n] = root.getPropertyValue(`--${n}`).trim() === 'transparent' ? 'transparent' : getComputedStyle(probe).color;
				}
				probe.remove();
				return out;
			}, colourTokens);
			const rgb = (name: string) => {
				const m = tokens[name]?.match(/rgba?\(([\d.]+),\s*([\d.]+),\s*([\d.]+)(?:,\s*([\d.]+))?/);
				if (!m) throw new Error(`${look} ${theme}: --${name} isn't a colour: ${tokens[name]}`);
				return [m[1], m[2], m[3]].map(Number);
			};
			const lum = (c: number[]) =>
				c.map((v) => v / 255).map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)).reduce((s, v, i) => s + v * [0.2126, 0.7152, 0.0722][i], 0);
			const ratio = (a: string, b: string) => {
				const [x, y] = [lum(rgb(a)), lum(rgb(b))].sort((p, q) => q - p);
				return (x + 0.05) / (y + 0.05);
			};
			report.push(`\n${look}, ${theme}`);
			for (const p of pairs(tokens)) {
				const each = p.on.map((b) => ({ b, r: ratio(p.fg, b) }));
				const worst = each.reduce((w, e) => (e.r < w.r ? e : w));
				const line = `  ${p.what}: ${worst.r.toFixed(2)}:1 at worst (--${p.fg} on --${worst.b}; needs ${p.min}:1)  [${each.map((e) => `${e.b} ${e.r.toFixed(2)}`).join(', ')}]`;
				report.push(line);
				if (worst.r < p.min) failures.push(`${look} ${theme}: ${line.trim()}`);
			}
		}
	}
	console.log(report.join('\n'));
	if (process.env.PA_PROTO_SHOTS) fs.writeFileSync(path.join(process.env.PA_PROTO_SHOTS, `contrast-${test.info().project.name}.txt`), report.join('\n') + '\n');
	expect(failures).toEqual([]);
});

// The structure R77-R81a ask for, the same in every look.
test('prototype: one structure (R77, R77a, R79, R80, R81)', async ({ page }) => {
	// R80: at 1,280 px the message opens in a pane beside the list.
	await page.setViewportSize({ width: 1280, height: 800 });
	await page.goto(url('ledger', 'light', '&m=1'));
	const list = await page.locator('.p-list').boundingBox();
	const pane = await page.locator('.p-pane').boundingBox();
	expect(list && pane && pane.x >= list.x + list.width - 1).toBeTruthy();
	// R79: each level above the current page is a link.
	await page.goto(url('ledger', 'light', '&page=template-fire'));
	await expect(page.locator('.p-crumbs li')).toHaveText(['Administration', 'Templates', 'Example Fire']);
	await expect(page.locator('.p-crumbs li a')).toHaveCount(2);
	// R77, A68: a user sees no Administration; shrunk, each group icon has a
	// name, and focusing one opens its panel.
	await page.goto(url('ledger', 'light', '&role=user&menu=shrunk'));
	await expect(page.locator('.p-side')).not.toContainText('Administration');
	const heads = page.locator('.p-menu .head');
	await expect(heads).toHaveCount(4);
	for (const n of await heads.all()) expect(await n.getAttribute('aria-label')).toBeTruthy();
	await heads.nth(1).focus();
	await expect(page.locator('.p-menu .group').nth(2).locator('.entries')).toBeVisible();
	// R81: under 768 px, a bottom bar and no side menu; More leaves out the
	// print sheet and Administration.
	await page.setViewportSize({ width: 375, height: 812 });
	await page.goto(url('ledger', 'light', '&more=1'));
	await expect(page.locator('.p-side')).toBeHidden();
	await expect(page.locator('.p-bottom')).toBeVisible();
	await expect(page.locator('.p-bottom a')).toHaveText(['Messages', 'Map', 'Albums', 'More']);
	const more = page.locator('.p-more');
	await expect(more).toBeVisible();
	await expect(more).not.toContainText('Print sheet');
	await expect(more).not.toContainText('Administration');
	// R31: no horizontal scrolling on a phone.
	await page.goto(url('ledger', 'light'));
	expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
});

// Screenshots for the owner: each look, light and dark, at 1,920, 1,280 and
// 375 px; desktop with the menu open and shrunk and with a message open.
test('prototype: screenshots', async ({ page, browserName }) => {
	const dir = process.env.PA_PROTO_SHOTS;
	test.skip(!dir || browserName !== 'chromium', 'only when PA_PROTO_SHOTS is set, in Chromium');
	test.setTimeout(300_000);
	const states = [
		{ name: 'menu-open', extra: '' },
		{ name: 'menu-shrunk', extra: '&menu=shrunk' },
		{ name: 'message-open', extra: '&m=3' }
	];
	const phone = [
		{ name: 'list', extra: '' },
		{ name: 'message-open', extra: '&m=3' },
		{ name: 'more', extra: '&more=1' }
	];
	for (const look of looks) {
		for (const theme of themes) {
			for (const width of [1920, 1280, 375]) {
				await page.setViewportSize({ width, height: width === 1920 ? 1080 : width === 1280 ? 800 : 812 });
				for (const s of width === 375 ? phone : states) {
					await page.goto(url(look, theme, s.extra));
					await expect(page.locator('.p-shell')).toBeVisible();
					await page.evaluate(() => document.fonts.ready);
					await page.screenshot({ path: path.join(dir!, `${look}-${theme}-${width}-${s.name}.png`) });
				}
			}
		}
	}
});
