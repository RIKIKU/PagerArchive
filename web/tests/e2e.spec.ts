/// <reference types="node" />
// Browser acceptance checks for milestone M2 (SPEC.md section 9).
// scripts/e2e.sh starts the API on a fresh database with the standard test
// fixture, a client key, a user-role server and an admin-role server.
import { test, expect, type Page, type APIRequestContext } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

const USER_URL = process.env.PA_USER_URL ?? 'http://127.0.0.1:18080';
const ADMIN_URL = process.env.PA_ADMIN_URL ?? 'http://127.0.0.1:18081';
const KEY = process.env.PA_CLIENT_KEY ?? '';

let seq = 0;
/** A job number no other test (or browser project) has used. */
function uniqueJob(): string {
	seq++;
	return 'EX' + String((Date.now() % 1e5) * 100 + seq).padStart(7, '0').slice(-7);
}

async function post(request: APIRequestContext, capcode: string, text: string) {
	const res = await request.post(`${USER_URL}/api/v1/pages`, {
		headers: { Authorization: `Bearer ${KEY}` },
		data: { capcode, text, received_at: new Date().toISOString(), baud: 512 }
	});
	expect(res.status()).toBe(200);
}

async function waitLive(page: Page) {
	await expect(page.getByRole('status').filter({ hasText: 'Live' })).toBeVisible();
}

// A3. Live update: two users on the unfiltered list both see a new page at
// the top within 2 seconds of the post, without a refresh.
test('A3 live update reaches two viewers within 2 s', async ({ browser, request }) => {
	const a = await (await browser.newContext()).newPage();
	const b = await (await browser.newContext()).newPage();
	await Promise.all([a.goto('/'), b.goto('/')]);
	await Promise.all([waitLive(a), waitLive(b)]);

	const job = uniqueJob();
	const text = `@@${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1`;
	const start = Date.now();
	await post(request, '0100001', text);
	for (const p of [a, b]) {
		const first = p.getByTestId('message-list').locator('article').first();
		await expect(first).toContainText(job, { timeout: 2000 });
	}
	const elapsed = Date.now() - start;
	console.log(`A3: both viewers showed the page ${elapsed} ms after the post`);
	expect(elapsed).toBeLessThanOrEqual(2000);
});

// A4. Live update respects filters.
test('A4 live update respects filters', async ({ browser, request }) => {
	const a = await (await browser.newContext()).newPage();
	const b = await (await browser.newContext()).newPage();
	await Promise.all([a.goto('/?group=STN2'), b.goto('/')]);
	await Promise.all([waitLive(a), waitLive(b)]);

	const job = uniqueJob();
	const start = Date.now();
	await post(request, '0100001', `@@${job} HSF1 SHED FIRE @ 3 FICTION ST, MADEUPTON #STN1`);
	await expect(b.getByTestId('message-list')).toContainText(job, { timeout: 2000 });
	console.log(`A4: unfiltered viewer showed the page ${Date.now() - start} ms after the post`);
	await a.waitForTimeout(5000 - (Date.now() - start));
	await expect(a.getByTestId('message-list')).not.toContainText(job);
});

// A10. Marker hidden in display, Emergency badge shown.
test('A10 marker hidden, Emergency badge shown', async ({ page, request }) => {
	const job = uniqueJob();
	await post(request, '0100001', `@@${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1`);
	await page.goto(`/?job=${job}`);
	const row = page.getByTestId('message-list').locator('article').first();
	await expect(row.locator('p.text')).toHaveText(new RegExp(`^${job} `));
	await expect(row.locator('.badge.emergency')).toBeVisible();
	await expect(row.locator('.badge.emergency')).toContainText('Emergency');
	await expect(row).toContainText('Station 1');
});

// R31: at 375 px, date, time and alias stay visible with no horizontal scroll.
test('R31 phone width keeps date, time and alias visible', async ({ browser, request }) => {
	const job = uniqueJob();
	await post(request, '0100004', `@@${job} HSF1 AN EXTREMELY_LONG_UNBROKEN_DESCRIPTION_THAT_MUST_WRAP_RATHER_THAN_SCROLL @ 3 FICTION ST, MADEUPTON #STN4`);
	const page = await (await browser.newContext({ viewport: { width: 375, height: 740 } })).newPage();
	await page.goto(`/?job=${job}`);
	const row = page.getByTestId('message-list').locator('article').first();
	await expect(row).toContainText(job);
	for (const loc of [row.locator('.date'), row.locator('.time'), row.getByText('Station 4')]) {
		await expect(loc).toBeInViewport();
	}
	const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
	expect(overflow).toBeLessThanOrEqual(0);
});

// A1 in the browser: the job view lists every group's copy.
test('A1 job view shows every copy', async ({ page, request }) => {
	const job = uniqueJob();
	for (let n = 1; n <= 4; n++) {
		await post(request, `010000${n}`, `@@${job} HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN${n}`);
	}
	await page.goto(`/jobs/${job}`);
	await expect(page.locator('article')).toHaveCount(4);
	await expect(page.getByText('4 messages')).toBeVisible();
});

// R35c: flags only for admins.
test('R35c flags are shown to admins only', async ({ page, request }) => {
	await post(request, '0100001', '@@GARBLED E2E ' + uniqueJob());
	await page.goto(`${USER_URL}/?q=GARBLED+E2E`);
	await expect(page.getByTestId('message-list').locator('article').first()).toBeVisible();
	await expect(page.getByText('parse failed')).toHaveCount(0);
	await expect(page.getByText('Flags (admins only)')).toHaveCount(0);

	await page.goto(`${ADMIN_URL}/?q=GARBLED+E2E`);
	await expect(page.getByText('parse failed').first()).toBeVisible();
	await expect(page.getByRole('link', { name: 'Clients' })).toBeVisible();
});

// A22. Tap targets at 375 px (R35d): the text opens the detail view, the
// alias opens the alias view (only that alias's capcodes), the job number
// opens the job view, and a capcode with no alias opens its own view.
test('A22 tap targets at 375 px', async ({ browser, request }) => {
	const job = uniqueJob();
	await post(request, '0100002', `@@${job} HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN2`);
	await post(request, '0100001', `@@${job} HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN1`);
	const ref = job.slice(2); // not a job number, so this page stays out of the job
	await post(request, '0999998', `QDPAGE TO AN UNKNOWN PAGER REF ${ref}`);
	const ctx = await browser.newContext({ viewport: { width: 375, height: 740 }, hasTouch: true });
	const page = await ctx.newPage();
	const list = page.getByTestId('message-list');
	const row = list.locator('article').filter({ hasText: 'Station 1' });

	// The text opens the detail view, and so does the rest of the row (here
	// its top-left corner, over the date).
	for (const target of ['text', 'row'] as const) {
		await page.goto(`/?job=${job}`);
		await expect(list.locator('article')).toHaveCount(2);
		if (target === 'text') await row.locator('p.text').tap();
		else await row.tap({ position: { x: 16, y: 12 } });
		await expect(page).toHaveURL(/\/messages\/\d+$/);
		await expect(page.getByRole('heading', { name: 'Received by' })).toBeVisible();
		await expect(page.locator('main')).toContainText('#STN1');
	}

	// The alias opens the alias view, listing only that alias's capcodes.
	await page.goto(`/?job=${job}`);
	await row.getByRole('link', { name: 'Station 1' }).tap();
	await expect(page).toHaveURL(/\/aliases\/\d+$/);
	await expect(page.getByRole('heading', { level: 1, name: 'Station 1' })).toBeVisible();
	await expect(page.getByText('0100001', { exact: true })).toBeVisible();
	await waitLive(page);
	await expect(list).toContainText(job);
	const who = await list.locator('article .who').allTextContents();
	expect(who.length).toBeGreaterThan(0);
	expect(new Set(who)).toEqual(new Set(['Station 1']));
	await expect(list).not.toContainText('#STN2');

	// The job number opens the job view.
	await page.goto(`/?job=${job}`);
	await row.getByRole('link', { name: `Job ${job}` }).tap();
	await expect(page).toHaveURL(new RegExp(`/jobs/${job}$`));
	await expect(page.getByRole('heading', { level: 1, name: `Job ${job}` })).toBeVisible();
	await expect(page.locator('main article')).toHaveCount(2);

	// A capcode with no alias opens its own view: count, first and last seen.
	await page.goto(`/?q=UNKNOWN+PAGER+REF+${ref}`);
	await list.locator('article').first().getByRole('link', { name: '0999998' }).tap();
	await expect(page).toHaveURL(/\/capcodes\/0999998$/);
	await expect(page.getByRole('heading', { level: 1, name: 'Capcode 0999998' })).toBeVisible();
	await expect(page.getByText('First seen')).toBeVisible();
	await expect(list).toContainText(ref);
	await ctx.close();
});

// WCAG 2.1 AA: axe-core on every view, light and dark, phone and desktop.
// The alias view (R35e) is checked for an alias and for a capcode with none.
const views = ['/', '/?q=SMOKE', '/capcodes', '/jobs/EX0000001', 'detail', 'alias', '/capcodes/0999999', '/admin/clients'];
for (const scheme of ['light', 'dark'] as const) {
	for (const width of [375, 1280]) {
		test(`axe: every view, ${scheme}, ${width}px`, async ({ browser, request }) => {
			await post(request, '0100001', `@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
			await post(request, '0999999', 'QDPAGE TO AN UNKNOWN PAGER');
			const res = await request.get(`${USER_URL}/api/v1/messages?capcode=0100001`);
			const aliasID = (await res.json()).messages[0].alias_id;
			const ctx = await browser.newContext({ colorScheme: scheme, viewport: { width, height: 900 } });
			const page = await ctx.newPage();
			for (const view of views) {
				const base = view === '/admin/clients' ? ADMIN_URL : USER_URL;
				if (view === 'detail') {
					await page.goto(`${ADMIN_URL}/`);
					await page.getByTestId('message-list').locator('article a.open').first().click();
					await expect(page.getByRole('heading', { name: 'Received by' })).toBeVisible();
				} else {
					const path = view === 'alias' ? `/aliases/${aliasID}` : view;
					await page.goto(base + path);
					// Pages with a live stream never go network-idle; wait for content.
					await expect(page.locator('main h1').first()).toBeVisible();
					if (path.startsWith('/?') || path === '/' || path.startsWith('/aliases/') || path.startsWith('/capcodes/')) await waitLive(page);
					else await page.waitForTimeout(300);
				}
				const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
				const summary = results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join('; ')}`);
				expect(summary, `${view} (${scheme}, ${width}px)`).toEqual([]);
			}
		});
	}
}
