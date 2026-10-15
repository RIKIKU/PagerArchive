/// <reference types="node" />
// Browser acceptance checks (SPEC.md section 9). scripts/e2e.sh starts the API
// on a fresh database with the standard test fixture, a client key, and a
// signed-in user and admin (their session tokens).
import { test, expect, type Browser, type BrowserContextOptions, type Page, type APIRequestContext } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

const URL = process.env.PA_URL ?? 'http://127.0.0.1:18080';
const SESSIONS = { user: process.env.PA_USER_SESSION ?? '', admin: process.env.PA_ADMIN_SESSION ?? '' };
type Role = keyof typeof SESSIONS;

const sessionCookie = (role: Role) => ({ name: 'pa_session', value: SESSIONS[role], url: URL });

/** A browser context signed in as role. */
async function signedIn(browser: Browser, role: Role = 'user', opts: BrowserContextOptions = {}) {
	const ctx = await browser.newContext(opts);
	await ctx.addCookies([sessionCookie(role)]);
	return ctx;
}

// The page fixture is signed in as the user.
test.beforeEach(async ({ context }) => {
	await context.addCookies([sessionCookie('user')]);
});
const KEY = process.env.PA_CLIENT_KEY ?? '';

let seq = 0;
/** A job number no other test (or browser project) has used. */
function uniqueJob(): string {
	seq++;
	return 'EX' + String((Date.now() % 1e5) * 100 + seq).padStart(7, '0').slice(-7);
}

async function post(request: APIRequestContext, capcode: string, text: string) {
	const res = await request.post(`${URL}/api/v1/pages`, {
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
	const a = await (await signedIn(browser)).newPage();
	const b = await (await signedIn(browser)).newPage();
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
	const a = await (await signedIn(browser)).newPage();
	const b = await (await signedIn(browser)).newPage();
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
	const page = await (await signedIn(browser, 'user', { viewport: { width: 375, height: 740 } })).newPage();
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
test('R35c flags are shown to admins only', async ({ page, browser, request }) => {
	await post(request, '0100001', '@@GARBLED E2E ' + uniqueJob());
	await page.goto(`/?q=GARBLED+E2E`);
	await expect(page.getByTestId('message-list').locator('article').first()).toBeVisible();
	await expect(page.getByText('parse failed')).toHaveCount(0);
	await expect(page.getByText('Flags (admins only)')).toHaveCount(0);
	await expect(page.getByRole('link', { name: 'Users' })).toHaveCount(0);

	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto(`/?q=GARBLED+E2E`);
	await expect(admin.getByText('parse failed').first()).toBeVisible();
	await expect(admin.getByRole('link', { name: 'Clients' })).toBeVisible();
	await expect(admin.getByRole('link', { name: 'Users' })).toBeVisible();
});

// A29 in the browser: without a session the app sends you to sign in. An
// admin exists here, so that's Authentik straight away. (No Authentik runs
// here, so the sign-in page says it can't be reached.)
test('A29 signed-out visitors are sent to sign in', async ({ browser }) => {
	const page = await (await browser.newContext()).newPage();
	await page.goto('/jobs/EX0000001');
	await expect(page).toHaveURL(/\/signin\?error=directory_unavailable$/);
	await expect(page.getByRole('alert')).toContainText("couldn't be reached");
	await expect(page.getByRole('link', { name: 'Sign in' })).toBeVisible();
});

// R44: while the first admin can still be set up, a signed-out visitor is
// sent to the sign-in page, which offers it, not straight to Authentik. An
// admin exists here, so the status is answered for the test; the stack test
// checks it for real from the root URL.
test('R44 signed-out visitors on a new install see first-admin set-up', async ({ browser }) => {
	const page = await (await browser.newContext()).newPage();
	await page.route('/auth/status', (r) => r.fulfill({ json: { first_admin_setup: true } }));
	await page.goto('/');
	await expect(page).toHaveURL(/\/signin$/);
	await expect(page.getByRole('button', { name: 'Set up the first admin' })).toBeVisible();
	await page.context().close();
});

// R44 screens: the account page shows who you are and who invited you; the
// users page lists users for admins, with their inviter.
test('account and users pages', async ({ page, browser }) => {
	await page.goto('/account');
	await expect(page.getByRole('heading', { level: 1, name: 'Your account' })).toBeVisible();
	await expect(page.locator('dl')).toContainText('user@example.test');
	await expect(page.locator('dl')).toContainText('Test Admin (admin@example.test)');
	await expect(page.getByRole('button', { name: 'Send invitation' })).toBeVisible();
	await page.goto('/admin/users');
	await expect(page.getByRole('alert')).toContainText('admins only');

	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto('/admin/users');
	const user = admin.locator('li').filter({ hasText: 'user@example.test' });
	await expect(user).toContainText('Invited by Test Admin (admin@example.test)');
	await expect(user.getByRole('button', { name: 'Deactivate with invite tree: Test User (user@example.test)' })).toBeVisible();
	// No actions on your own row.
	const self = admin.locator('li').filter({ has: admin.getByText('Test Admin (admin@example.test)', { exact: true }) });
	await expect(self).toContainText('You');
	await expect(self.getByRole('button')).toHaveCount(0);
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
	const ctx = await signedIn(browser, 'user', { viewport: { width: 375, height: 740 }, hasTouch: true });
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
const views = ['/', '/?q=SMOKE', '/capcodes', '/jobs/EX0000001', 'detail', 'alias', '/capcodes/0999999', '/map', '/reports?capcode=0100001&from=2026-01-01&to=2026-12-31', '/admin/clients', '/admin/users', '/admin/retention', '/account', '/signin', '/signin?error=deactivated'];
for (const scheme of ['light', 'dark'] as const) {
	for (const width of [375, 1280]) {
		test(`axe: every view, ${scheme}, ${width}px`, async ({ browser, request }) => {
			test.setTimeout(120_000); // 15 views
			await post(request, '0100001', `@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
			await post(request, '0999999', 'QDPAGE TO AN UNKNOWN PAGER');
			const res = await request.get(`${URL}/api/v1/messages?capcode=0100001`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
			const aliasID = (await res.json()).messages[0].alias_id;
			// Admin views need the admin; the rest are seen as the user.
			const ctx = await signedIn(browser, 'admin', { colorScheme: scheme, viewport: { width, height: 900 } });
			const userCtx = await signedIn(browser, 'user', { colorScheme: scheme, viewport: { width, height: 900 } });
			const adminPage = await ctx.newPage();
			const userPage = await userCtx.newPage();
			for (const view of views) {
				const page = view === 'detail' || view.startsWith('/admin/') ? adminPage : userPage;
				if (view === 'detail') {
					// A located message, so its small map is checked too (R35c).
					await page.goto('/?q=SMOKE+FROM+ROOF');
					await page.getByTestId('message-list').locator('article a.open').first().click();
					await expect(page.getByRole('heading', { name: 'Received by' })).toBeVisible();
					await expect(page.getByTestId('map')).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
				} else {
					const path = view === 'alias' ? `/aliases/${aliasID}` : view;
					await page.goto(path);
					// Pages with a live stream never go network-idle; wait for content.
					await expect(page.locator('main h1').first()).toBeVisible();
					if (path === '/map') await expect(page.getByTestId('map')).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
					else if (path.startsWith('/reports')) await expect(page.getByTestId('report')).toBeVisible();
					else if (path.startsWith('/?') || path === '/' || path.startsWith('/aliases/') || path.startsWith('/capcodes/')) await waitLive(page);
					else await page.waitForTimeout(300);
				}
				const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
				const summary = results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join('; ')}`);
				expect(summary, `${view} (${scheme}, ${width}px)`).toEqual([]);
			}
			// Each context holds a live stream and, since M4, a map with a WebGL
			// context; left open they slow the following tests down.
			await Promise.all([ctx.close(), userCtx.close()]);
		});
	}
}

// M4. R35: the map shows located messages over the self-hosted test map,
// follows live updates, and loads nothing that breaks the page (CSP, worker,
// glyphs, tiles).
test('map shows located messages, live, with no console errors', async ({ page, request }, info) => {
	const errors: string[] = [];
	page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
	page.on('pageerror', (e) => errors.push(e.message));
	const tiles: number[] = [];
	page.on('response', (r) => r.url().includes('/api/v1/map/tiles.pmtiles') && tiles.push(r.status()));

	const job = uniqueJob();
	await post(request, '0100001', `@@${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1`);
	await page.goto('/map');
	const map = page.getByTestId('map');
	await expect(map).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
	// Without WebGL (headless Firefox) the page says so and still lists them.
	const drawn = (await map.getAttribute('data-ready')) === 'true';
	if (!drawn) await expect(map).toContainText('needs WebGL');
	// Chromium and WebKit have WebGL headless: there the map must draw.
	if (info.project.name !== 'firefox') expect(drawn).toBe(true);
	const list = page.getByTestId('located-list');
	await expect(list).toContainText(job);
	await expect(list.locator('li', { hasText: job })).toContainText('Address');

	// A street-only address arrives live and is marked approximate.
	const job2 = uniqueJob();
	const start = Date.now();
	await post(request, '0100002', `@@${job2} GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2`);
	await expect(list.locator('li', { hasText: job2 })).toContainText('Street (approximate)', { timeout: 2000 });
	console.log(`map: live point listed ${Date.now() - start} ms after the post`);

	// The tile extract is read with range requests (ADR 0001).
	if (drawn) {
		expect(tiles.length).toBeGreaterThan(0);
		expect(tiles.every((s) => s === 206)).toBe(true);
		expect(errors).toEqual([]);
	}

	// The detail view links to the message on the map.
	await list.locator('li', { hasText: job }).getByRole('link').click();
	await expect(page.getByTestId('location')).toContainText('From the address');
	await page.getByRole('link', { name: 'Show on map' }).click();
	await expect(page).toHaveURL(/\/map\?focus=\d+/);
	if (drawn) await expect(page.getByRole('heading', { name: 'Selected on the map (1)' })).toBeVisible({ timeout: 15_000 });
	await expect(page.getByTestId('located-list')).toContainText(job);
});

/**
 * What is drawn at the centre of a map that is centred on one message: a
 * marker (filled with fill, a colour token), a ring (the background token in
 * the middle, fill around it) or neither. Read from a screenshot, so it
 * checks what was actually drawn. Points are drawn above the basemap, so the
 * centre pixel is the point's own.
 */
async function drawnAtCentre(map: ReturnType<Page['getByTestId']>, fill: string): Promise<'marker' | 'ring' | 'none'> {
	const png = (await map.screenshot()).toString('base64');
	const cssWidth = (await map.boundingBox())!.width;
	return map.page().evaluate(
		async ({ src, cssWidth, fill }) => {
			const img = new Image();
			img.src = `data:image/png;base64,${src}`;
			await img.decode();
			const c = document.createElement('canvas');
			c.width = img.width;
			c.height = img.height;
			const g = c.getContext('2d')!;
			g.drawImage(img, 0, 0);
			const scale = img.width / cssWidth;
			const x = Math.floor(img.width / 2), y = Math.floor(img.height / 2);
			// up: CSS pixels above the centre.
			const at = (up: number) => Array.from(g.getImageData(x, Math.round(y - up * scale), 1, 1).data.slice(0, 3));
			const rgb = (token: string) => {
				const el = document.createElement('div');
				el.style.color = `var(${token})`;
				document.body.append(el);
				const v = getComputedStyle(el).color.match(/\d+/g)!.slice(0, 3).map(Number);
				el.remove();
				return v;
			};
			const near = (a: number[], b: number[]) => Math.hypot(a[0] - b[0], a[1] - b[1], a[2] - b[2]) < 40;
			// Points have a radius of 8 px; a ring's stroke runs from 8 to 12 px.
			if (near(at(0), rgb(fill))) return 'marker';
			if (near(at(0), rgb('--bg')) && near(at(10), rgb(fill))) return 'ring';
			return 'none';
		},
		{ src: png, cssWidth, fill }
	);
}

// A34. A message's detail view shows a small map centred on it: a marker for a
// house address, a ring for a street only, and no map for a message that
// isn't placed. On phones the message text comes first (P2).
for (const width of [375, 1280]) {
	test(`A34 map in the detail view, ${width}px`, async ({ browser, request }, info) => {
		const job = uniqueJob();
		const texts = {
			marker: `@@${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`,
			ring: `@@${job} GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1`,
			none: `QDTraining ${job} at the station`
		};
		for (const t of Object.values(texts)) await post(request, '0100001', t);
		const res = await request.get(`${URL}/api/v1/messages?q=${job}`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
		const found = (await res.json()).messages as { id: number; text: string }[];
		const words = { marker: 'SMOKE FROM ROOF', ring: 'GRASS FIRE', none: 'Training' };
		const idOf = (k: keyof typeof words) => found.find((m) => m.text.includes(words[k]))!.id;
		const page = await (await signedIn(browser, 'user', { viewport: { width, height: 900 } })).newPage();

		for (const kind of ['marker', 'ring'] as const) {
			await page.goto(`/messages/${idOf(kind)}`);
			const map = page.getByTestId('map');
			await expect(map).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
			const drawn = (await map.getAttribute('data-ready')) === 'true';
			// Headless Firefox has no WebGL: the small map says so; elsewhere it draws.
			if (info.project.name !== 'firefox') expect(drawn).toBe(true);
			// Both are Emergency pages (HSF1, GRS3), drawn in --emergency-bg.
			if (drawn) await expect.poll(() => drawnAtCentre(map, '--emergency-bg'), { timeout: 10_000 }).toBe(kind);
			else await expect(map).toContainText('needs WebGL');
			await expect(page.getByRole('link', { name: 'Show on map' })).toBeVisible();
			if (width === 375) {
				const text = (await page.locator('.text').boundingBox())!;
				const box = (await map.boundingBox())!;
				expect(text.y + text.height, 'message text above the map').toBeLessThanOrEqual(box.y);
			}
			await page.screenshot({ path: info.outputPath(`a34-${kind}-${width}.png`), fullPage: true });
		}

		await page.goto(`/messages/${idOf('none')}`);
		await expect(page.getByTestId('location')).toContainText('Not placed on the map');
		await expect(page.getByTestId('map')).toHaveCount(0);
		await page.context().close();
	});
}

// M4. R38 in the browser (A11 itself runs against the API in Go).
test('capcode report counts Emergency messages by category', async ({ page, request }, info) => {
	// Each browser project gets its own capcode: they share one database.
	const capcode = { chromium: '0300101', firefox: '0300102', webkit: '0300103' }[info.project.name] ?? '0300109';
	for (const t of ['HSF1 SMOKE', 'HSF2 SMOKE', 'GRS3 GRASS FIRE']) {
		await post(request, capcode, `@@${uniqueJob()} ${t} @ 12 FICTION ST, MADEUPTON #STN1`);
	}
	await post(request, capcode, 'QDTraining Tuesday 7pm at the station');
	await page.goto(`/reports?capcode=${capcode}`);
	await page.getByRole('button', { name: 'Run report' }).click();
	const table = page.getByTestId('report');
	await expect(table.getByRole('row', { name: 'Structure fire 2' })).toBeVisible();
	await expect(table.getByRole('row', { name: 'Grass fire 1' })).toBeVisible();
	await expect(table.getByRole('row', { name: 'Total 3' })).toBeVisible();
});

// M4. R46: shortening retention asks to confirm the cut-off date.
test('shortening retention asks to confirm the cut-off date', async ({ browser }) => {
	const ctx = await signedIn(browser, 'admin');
	const page = await ctx.newPage();
	await page.goto('/admin/retention');
	const days = page.getByLabel('Keep messages for (days)');
	await expect(days).toHaveValue('3650');
	await days.fill('730');
	await page.getByRole('button', { name: 'Save' }).click();
	const dialog = page.getByRole('alertdialog');
	await expect(dialog.locator('p')).toHaveText(/^All messages older than \d{1,2} \w+ \d{4} will be deleted at the next nightly run\. Confirm\?$/);
	await dialog.getByRole('button', { name: 'Cancel' }).click();
	await page.reload();
	await expect(days).toHaveValue('3650');

	await days.fill('730');
	await page.getByRole('button', { name: 'Save' }).click();
	await page.getByRole('alertdialog').getByRole('button', { name: 'Confirm' }).click();
	await expect(page.getByRole('status').filter({ hasText: 'Saved' })).toContainText('730 days');
	// Lengthening needs no confirmation; put the default back for other tests.
	await days.fill('3650');
	await page.getByRole('button', { name: 'Save' }).click();
	await expect(page.getByRole('status').filter({ hasText: 'Saved' })).toContainText('3,650 days');
	await ctx.close();
});

// M4. R39 on screen: scripts/e2e.sh adds client "quiet", last heard 31
// minutes ago (A13 itself runs against the API in Go).
test('admins see receivers that are not receiving', async ({ browser }) => {
	const ctx = await signedIn(browser, 'admin');
	const page = await ctx.newPage();
	await page.goto('/admin/clients');
	await expect(page.getByTestId('status-quiet')).toHaveText('Not receiving');
	await expect(page.getByTestId('status-e2e')).toHaveText('Receiving');
	await expect(page.getByTestId('not-receiving')).toHaveText('1 receiver not receiving');
	await ctx.close();
	// Users don't see receiver status at all.
	const userCtx = await signedIn(browser, 'user');
	const userPage = await userCtx.newPage();
	await userPage.goto('/');
	await waitLive(userPage);
	await expect(userPage.getByTestId('not-receiving')).toHaveCount(0);
	await userCtx.close();
});
