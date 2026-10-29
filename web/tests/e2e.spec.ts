/// <reference types="node" />
// Browser acceptance checks (SPEC.md section 9). scripts/e2e.sh starts the API
// on a fresh database with the standard test fixture, a client key, and a
// signed-in user and admin (their session tokens).
import { test, expect, type Browser, type BrowserContextOptions, type Page, type APIRequestContext } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const URL = process.env.PA_URL ?? 'http://127.0.0.1:18080';
// userC: a second user, for the print sheet kept per user (M11).
const SESSIONS = {
	user: process.env.PA_USER_SESSION ?? '',
	admin: process.env.PA_ADMIN_SESSION ?? '',
	userC: process.env.PA_USER_C_SESSION ?? ''
};
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

/** Runs SQL on the test database: with psql at PA_E2E_DATABASE_URL when
 * scripts/e2e.sh was given one (CI), else in the database's container. */
function sql(input: string) {
	const url = process.env.PA_E2E_DATABASE_URL;
	if (url) execFileSync('psql', [url, '-q', '-v', 'ON_ERROR_STOP=1'], { input });
	else execFileSync('docker', ['exec', '-i', process.env.PA_E2E_DB ?? '', 'psql', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres'], { input });
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
	await expect(page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Admin' })).toHaveCount(0);

	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto(`/?q=GARBLED+E2E`);
	await expect(admin.getByText('parse failed').first()).toBeVisible();
	await expect(admin.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Admin' })).toBeVisible();
});

// ADR 0010 §4: a role change closes that person's live connections, and the
// open list reconnects with the new role's view, without reloading the page.
test('a demoted admin\'s open list loses admin-only details without a reload', async ({ browser, request }, info) => {
	const n = projectN(info);
	const marker = `DEMOTE-${n}-${Date.now()}`;
	const email = `demoted-${marker.toLowerCase()}@example.test`;
	const token = `e2e-demoted-${crypto.randomUUID()}`;
	sql(`INSERT INTO users (subject, email, email_key, name, role, invited_by)
  SELECT 'e2e-${marker}', '${email}', '${email}', 'Demoted ${n}', 'admin', id FROM users WHERE subject = 'e2e-admin';
INSERT INTO sessions (token_hash, user_id, expires_at)
  SELECT sha256(convert_to('${token}', 'UTF8')), id, now() + interval '1 day' FROM users WHERE email_key = '${email}';`);
	await post(request, '0100004', `@@GARBLED ${marker} ONE`);
	const ctx = await browser.newContext();
	await ctx.addCookies([{ name: 'pa_session', value: token, url: URL }]);
	const page = await ctx.newPage();
	await page.goto(`/?q=${marker}`);
	const list = page.getByTestId('message-list');
	const adminLink = page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Admin' });
	await expect(list.locator('article')).toHaveCount(1);
	await expect(list.getByText('parse failed')).toHaveCount(1);
	await expect(adminLink).toBeVisible();
	await expect(page.getByText('Flags (admins only)')).toHaveCount(1);
	await expect(page.getByRole('status').filter({ hasText: 'Live' })).toBeVisible();
	await page.evaluate(() => ((window as unknown as { notReloaded: boolean }).notReloaded = true));

	const users = await (await request.get(`${URL}/api/v1/admin/users`, { headers: asRole('admin') })).json();
	const id = users.find((u: { email: string }) => u.email === email).id;
	const start = Date.now();
	const res = await request.put(`${URL}/api/v1/admin/users/${id}/role`, { headers: asRole('admin'), data: { role: 'user' } });
	expect(res.status()).toBeLessThan(300);
	await expect(list.getByText('parse failed')).toHaveCount(0, { timeout: 10_000 });
	await expect(adminLink).toHaveCount(0);
	await expect(page.getByText('Flags (admins only)')).toHaveCount(0);
	console.log(`ADR 0010 §4: demoted; the open list showed the user's view ${Date.now() - start} ms later`);
	await expect(page.getByRole('status').filter({ hasText: 'Live' })).toBeVisible();
	await post(request, '0100004', `@@GARBLED ${marker} TWO`);
	await expect(list.locator('article')).toHaveCount(2, { timeout: 2000 });
	await expect(list.getByText('parse failed')).toHaveCount(0);
	expect(await page.evaluate(() => (window as unknown as { notReloaded?: boolean }).notReloaded)).toBe(true);
	await ctx.close();
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
	// R42b: a tree is deactivated from its preview.
	await expect(user.getByRole('link', { name: 'Deactivate with invite tree: Test User (user@example.test)' })).toHaveAttribute('href', /\/admin\/users\/\d+\/deactivate-tree$/);
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
const views = ['/', '/?q=SMOKE', '/capcodes', '/jobs/EX0000001', 'detail', 'alias', 'alias-request', '/capcodes/0999999', '/map', '/reports?capcode=0100001&from=2026-01-01&to=2026-12-31', '/admin', '/admin/clients', '/admin/users', 'admin-user', 'admin-tree-preview', '/admin/aliases', 'admin-alias', '/admin/templates', 'admin-template', '/admin/requests', '/admin/retention', '/admin/integration-hosts', '/integrations', 'integration-form', 'selection', '/requests', '/sheet', '/account', '/signin', '/signin?error=deactivated'];
for (const scheme of ['light', 'dark'] as const) {
	for (const width of [375, 1280]) {
		test(`axe: every view, ${scheme}, ${width}px`, async ({ browser, request }) => {
			test.setTimeout(280_000); // 30 views
			await post(request, '0100001', `@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
			await post(request, '0999999', 'QDPAGE TO AN UNKNOWN PAGER');
			const res = await request.get(`${URL}/api/v1/messages?capcode=0100001`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
			const aliasID = (await res.json()).messages[0].alias_id;
			const users = await request.get(`${URL}/api/v1/admin/users`, { headers: { Cookie: `pa_session=${SESSIONS.admin}` } });
			const userID = (await users.json()).find((u: { email: string }) => u.email === 'user@example.test').id;
			const templates = await request.get(`${URL}/api/v1/admin/templates`, { headers: { Cookie: `pa_session=${SESSIONS.admin}` } });
			const templateID = (await templates.json()).find((t: { name: string }) => t.name === 'Example Fire').id;
			// M6: a waiting alias request, so the review screen and My requests
			// are checked with one; rejected at the end so it doesn't linger.
			const reqPattern = `049${['chromium', 'firefox', 'webkit'].indexOf(test.info().project.name) + 1}${scheme === 'light' ? 1 : 2}${width === 375 ? 1 : 2}`;
			const made = await request.post(`${URL}/api/v1/alias-requests`, {
				headers: { Cookie: `pa_session=${SESSIONS.user}` },
				data: { kind: 'add', pattern: reqPattern, name: `Axe ${reqPattern}`, note: 'NOTE-MARKER axe' }
			});
			expect(made.status()).toBe(201);
			const reqID = (await made.json()).id;
			// Admin views need the admin; the rest are seen as the user.
			const ctx = await signedIn(browser, 'admin', { colorScheme: scheme, viewport: { width, height: 900 } });
			const userCtx = await signedIn(browser, 'user', { colorScheme: scheme, viewport: { width, height: 900 } });
			const adminPage = await ctx.newPage();
			const userPage = await userCtx.newPage();
			for (const view of views) {
				const page = view === 'detail' || view.startsWith('admin-') || view.startsWith('/admin') ? adminPage : userPage;
				if (view === 'detail') {
					// A located message, so its small map is checked too (R35c).
					await page.goto('/?q=SMOKE+FROM+ROOF');
					await page.getByTestId('message-list').locator('article a.open').first().click();
					await expect(page.getByRole('heading', { name: 'Received by' })).toBeVisible();
					await expect(page.getByTestId('map')).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
				} else if (view === 'alias-request') {
					// R54: the request form, open on the alias view.
					await page.goto(`/aliases/${aliasID}`);
					await waitLive(page);
					await page.getByRole('button', { name: 'Request a change' }).click();
					await expect(page.getByRole('heading', { name: 'Request a change' })).toBeVisible();
				} else if (view === 'selection') {
					// R48: the selection field with its suggestions open.
					await page.goto('/integrations');
					await page.getByRole('button', { name: 'New integration' }).click();
					await page.getByRole('combobox', { name: 'Capcodes and aliases' }).fill('Station');
					await expect(page.getByRole('listbox', { name: 'Suggestions' }).getByRole('option').first()).toBeVisible();
				} else if (view === 'integration-form') {
					await page.goto('/integrations');
					await page.getByRole('button', { name: 'New integration' }).click();
					await expect(page.getByRole('heading', { name: 'New integration' })).toBeVisible();
				} else {
					const path =
						view === 'alias'
							? `/aliases/${aliasID}`
							: view === 'admin-user'
								? `/admin/users/${userID}`
								: view === 'admin-tree-preview'
									? `/admin/users/${userID}/deactivate-tree`
									: view === 'admin-alias'
										? `/admin/aliases/${aliasID}`
										: view === 'admin-template'
											? `/admin/templates/${templateID}`
											: view;
					await page.goto(path);
					// Pages with a live stream never go network-idle; wait for content.
					await expect(page.locator('main h1').first()).toBeVisible();
					if (view === 'admin-user' || view === 'admin-tree-preview') await expect(page.getByTestId('tree-person').first()).toBeVisible();
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
			await request.post(`${URL}/api/v1/admin/alias-requests/${reqID}/reject`, {
				headers: { Cookie: `pa_session=${SESSIONS.admin}` },
				data: { feedback: 'Wrong station: axe test' }
			});
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

// M5. R47-R51c in the browser: from the main navigation, a user creates a
// webhook to a local receiver, sends a test and sees the answer, a real page
// arrives, and they pause and resume it. The admin sees that it exists, not
// where it sends.
test('A30 integrations in the browser: notice, create, Send test, delivery, pause and resume', async ({ page, browser, request }) => {
	const http = await import('node:http');
	const received: { path: string; body: Record<string, unknown> }[] = [];
	const receiver = http.createServer((req, res) => {
		let data = '';
		req.on('data', (c) => (data += c));
		req.on('end', () => {
			received.push({ path: req.url ?? '', body: JSON.parse(data || '{}') });
			res.writeHead(200, { 'Content-Type': 'text/plain' }).end('received');
		});
	});
	await new Promise<void>((r) => receiver.listen(0, '127.0.0.1', r));
	const port = (receiver.address() as { port: number }).port;
	try {
		// The receiver is on 127.0.0.1: the admin allows it (R51).
		const allow = await request.put(`${URL}/api/v1/admin/integration-hosts`, {
			headers: { Cookie: `pa_session=${SESSIONS.admin}` },
			data: { entries: ['127.0.0.0/8'] }
		});
		expect(allow.status()).toBe(200);

		const name = `Station 1 webhook ${Date.now()}`;
		await page.goto('/');
		await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Integrations' }).click();
		// R47: the one-line notice that integrations don't replace a pager.
		await expect(page.getByTestId('integrations-notice')).toHaveText(
			'Integrations are a convenience, not a replacement for your pager: delivery is best-effort, and a page may arrive late or not at all.'
		);
		await page.getByRole('button', { name: 'New integration' }).click();
		await page.getByLabel('Name', { exact: true }).fill(name);
		await chooseInSelection(page, '0100001', 'Station 1 (0100001)');
		await page.getByLabel('URL', { exact: true }).fill(`http://127.0.0.1:${port}/hook/T0PS3CRET`);
		await page.getByRole('button', { name: 'Save integration' }).click();
		await expect(page.getByRole('status').filter({ hasText: `Saved “${name}”` })).toBeVisible();
		const card = page.getByTestId('integration').filter({ hasText: name });
		await expect(card.getByTestId('integration-status')).toHaveText('Active');
		await expect(card).toContainText(`http://127.0.0.1:${port}/…`);
		await expect(page.locator('body')).not.toContainText('T0PS3CRET'); // the URL is a secret once saved

		// Send test (R51c): the owner sees what the endpoint answered.
		await card.getByRole('button', { name: 'Send test' }).click();
		const answer = card.getByTestId('test-answer');
		await expect(answer).toContainText('Delivered.');
		await expect(answer).toContainText('HTTP 200 OK');
		await expect(answer).toContainText('received');
		expect(received.at(-1)?.body.test).toBe(true);

		// A real page arrives within 5 seconds, without its marker.
		const job = uniqueJob();
		const start = Date.now();
		await post(request, '0100001', `@@${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
		await expect.poll(() => received.filter((r) => r.body.job_number === job).length, { timeout: 5000 }).toBe(1);
		console.log(`M5: webhook received the page ${Date.now() - start} ms after the post`);
		expect(received.find((r) => r.body.job_number === job)?.body.text).toBe(`${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
		await card.getByRole('button', { name: 'Deliveries' }).click();
		await expect(card.locator('.deliveries')).toContainText('Delivered');

		// Pause and resume.
		await card.getByRole('button', { name: 'Pause' }).click();
		await expect(card.getByTestId('integration-status')).toHaveText('Paused');
		await card.getByRole('button', { name: 'Resume' }).click();
		await expect(card.getByTestId('integration-status')).toHaveText('Active');

		// The admin sees it exists, never where it sends.
		const admin = await (await signedIn(browser, 'admin')).newPage();
		await admin.goto('/admin/users');
		await admin.getByRole('link', { name: 'Test User (user@example.test)', exact: true }).click();
		const row = admin.getByTestId('admin-integration').filter({ hasText: name });
		await expect(row).toContainText('Webhook');
		await expect(row).toContainText('Active');
		await expect(admin.locator('main')).not.toContainText(`127.0.0.1:${port}`);
		await admin.context().close();

		await card.getByRole('button', { name: 'Delete…' }).click();
		await card.getByRole('button', { name: 'Yes, delete it' }).click();
		await expect(page.getByTestId('integration').filter({ hasText: name })).toHaveCount(0);
	} finally {
		receiver.close();
	}
});

/** Types into the selection field (R48) and chooses the suggestion named option. */
async function chooseInSelection(page: Page, typed: string, option: string) {
	const field = page.getByRole('combobox', { name: 'Capcodes and aliases' });
	await field.fill(typed);
	await page.getByRole('listbox', { name: 'Suggestions' }).getByRole('option', { name: option }).click();
	await expect(page.getByRole('list', { name: 'Chosen capcodes and aliases' })).toContainText(option);
	await expect(field).toHaveValue('');
}

// A40. One selection field: typing 0100001 offers and selects the alias
// "Station 1 (0100001)"; typing 0100009 selects only the capcode, with the
// range alias "Stations block" (0100%) named beside it.
test('A40 one selection field', async ({ page, request }) => {
	const allow = await request.put(`${URL}/api/v1/admin/integration-hosts`, {
		headers: { Cookie: `pa_session=${SESSIONS.admin}` },
		data: { entries: ['127.0.0.0/8'] }
	});
	expect(allow.status()).toBe(200);
	await page.goto('/integrations');
	await page.getByRole('button', { name: 'New integration' }).click();
	const field = page.getByRole('combobox', { name: 'Capcodes and aliases' });

	await field.fill('0100001');
	const options = page.getByRole('listbox', { name: 'Suggestions' }).getByRole('option');
	await expect(options).toHaveCount(1);
	await expect(options.first()).toHaveText('Station 1 (0100001)');
	await field.press('Enter'); // the keyboard chooses the first suggestion
	const chosen = page.getByRole('list', { name: 'Chosen capcodes and aliases' }).getByTestId('chosen');
	await expect(chosen).toHaveText([/^Station 1 \(0100001\)\s*×$/]);

	await field.fill('0100009');
	await expect(options).toHaveCount(1);
	await expect(options.first()).toContainText('0100009');
	await expect(options.first()).toContainText('in Stations block');
	await expect(options.first()).not.toContainText('0100%');
	await options.first().click();
	await expect(chosen).toHaveText([/^Station 1 \(0100001\)\s*×$/, /^0100009\s*in Stations block\s*×$/]);

	const name = `A40 selection ${Date.now()}`;
	await page.getByLabel('Name', { exact: true }).fill(name);
	await page.getByLabel('URL', { exact: true }).fill('http://127.0.0.1:9/hook');
	await page.getByRole('button', { name: 'Save integration' }).click();
	const card = page.getByTestId('integration').filter({ hasText: name });
	await expect(card).toContainText('Station 1, 0100009 (in Stations block)');

	// Saved as the alias and the capcode, never the range.
	const res = await request.get(`${URL}/api/v1/integrations`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
	const saved = (await res.json()).integrations.find((i: { name: string }) => i.name === name);
	expect(saved.aliases.map((a: { name: string }) => a.name)).toEqual(['Station 1']);
	expect(saved.capcodes).toEqual(['0100009']);
	expect(saved.covered_by).toEqual({ '0100009': 'Stations block' });

	await card.getByRole('button', { name: 'Delete…' }).click();
	await card.getByRole('button', { name: 'Yes, delete it' }).click();
	await expect(card).toHaveCount(0);
});

// A41. Administration: user B has no Admin link and admin pages refuse
// them; admin A has one Admin link, whose start page reaches every admin
// screen; B's page shows B's integration without where it sends; nothing
// lists every user's integrations.
test('A41 Administration area', async ({ page, browser, request }) => {
	const as = (role: Role) => ({ Cookie: `pa_session=${SESSIONS[role]}` });
	await request.put(`${URL}/api/v1/admin/integration-hosts`, { headers: as('admin'), data: { entries: ['127.0.0.0/8'] } });
	const stamp = Date.now();
	const make = async (role: Role, name: string) => {
		const r = await request.post(`${URL}/api/v1/integrations`, {
			headers: as(role),
			data: { name, kind: 'webhook', capcodes: ['0500009'], secrets: { url: `http://127.0.0.1:9/hook/SECRET${stamp}` } }
		});
		expect(r.status()).toBe(201);
		return (await r.json()).id as number;
	};
	const bName = `B integration ${stamp}`;
	const aName = `A integration ${stamp}`;
	const ids: [Role, number][] = [
		['user', await make('user', bName)],
		['admin', await make('admin', aName)]
	];
	try {
		// B: no Admin link, and every admin page is refused.
		await page.goto('/');
		const main = page.getByRole('navigation', { name: 'Main' });
		await expect(main.getByRole('link', { name: 'Integrations' })).toBeVisible();
		await expect(main.getByRole('link', { name: 'Admin' })).toHaveCount(0);
		for (const path of ['/admin', '/admin/users', '/admin/users/1', '/admin/aliases', '/admin/aliases/1', '/admin/templates', '/admin/templates/1', '/admin/requests', '/admin/clients', '/admin/retention', '/admin/integration-hosts']) {
			await page.goto(path);
			await expect(page.getByRole('alert'), path).toContainText('admins only');
			await expect(page.locator('main'), path).not.toContainText(aName);
		}

		// A: one Admin link, and its start page reaches every admin screen.
		const admin = await (await signedIn(browser, 'admin')).newPage();
		await admin.goto('/');
		const nav = admin.getByRole('navigation', { name: 'Main' });
		await expect(nav.getByRole('link', { name: 'Admin' })).toHaveCount(1);
		for (const old of ['Users', 'Clients', 'Retention', 'All integrations']) await expect(nav.getByRole('link', { name: old })).toHaveCount(0);
		await nav.getByRole('link', { name: 'Admin' }).click();
		await expect(admin.getByRole('heading', { level: 1, name: 'Administration' })).toBeVisible();
		const screens = admin.getByTestId('admin-screens');
		await expect(screens.getByRole('link')).toHaveText(['Users', 'Aliases', 'Templates', 'Alias requests', 'Receiving clients', 'Retention', 'Integration addresses']);
		for (const [link, heading] of [
			['Users', 'Users'],
			['Aliases', 'Aliases'],
			['Templates', 'Templates'],
			['Alias requests', 'Alias requests'],
			['Receiving clients', 'Receiving clients'],
			['Retention', 'Retention'],
			['Integration addresses', 'Integration addresses']
		]) {
			await screens.getByRole('link', { name: link }).click();
			await expect(admin.getByRole('heading', { level: 1 })).toHaveText(heading);
			await expect(admin.getByRole('alert')).toHaveCount(0);
			await admin.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Administration' }).click();
		}

		// The Users page links to the invite form on the Account page (R41, R44b).
		await screens.getByRole('link', { name: 'Users' }).click();
		await admin.getByRole('main').getByRole('link', { name: 'Invite someone' }).click();
		await expect(admin).toHaveURL(/\/account#invite$/);
		await expect(admin.getByRole('heading', { level: 2, name: 'Invite someone' })).toBeInViewport();
		await expect(admin.getByLabel('Their email address')).toBeVisible();
		await expect(admin.getByRole('button', { name: 'Send invitation' })).toBeVisible();
		await admin.goBack();

		// B's page in Administration: B's integration, not where it sends, and not A's.
		await expect(admin.getByRole('heading', { level: 1 })).toHaveText('Users');
		const bRow = admin.locator('li').filter({ hasText: 'user@example.test' });
		await expect(bRow).toContainText(/\d+ integrations?/);
		await bRow.getByRole('link', { name: 'Test User (user@example.test)', exact: true }).click();
		await expect(admin.getByRole('heading', { level: 1 })).toHaveText('Test User (user@example.test)');
		const row = admin.getByTestId('admin-integration').filter({ hasText: bName });
		await expect(row).toContainText('Webhook');
		await expect(row).toContainText('Active');
		await expect(row.getByRole('button', { name: `Pause: ${bName}` })).toBeVisible();
		const text = await admin.locator('main').innerText();
		for (const leak of [`SECRET${stamp}`, '127.0.0.1', 'http://', aName]) expect(text, leak).not.toContain(leak);

		// No page lists every user's integrations.
		const all = await request.get(`${URL}/api/v1/admin/integrations`, { headers: as('admin') });
		expect(all.status()).toBe(404);
		await admin.goto('/admin/integrations');
		await expect(admin.locator('main')).not.toContainText(bName);
		await admin.context().close();
	} finally {
		for (const [role, id] of ids) await request.delete(`${URL}/api/v1/integrations/${id}`, { headers: as(role) });
	}
});

// Radios and checkboxes keep their own small size, not the full-width, tall
// box text inputs get (app.css), on the integration form for every
// destination, the admin-only flags included. The Content radios are
// stacked, each directly under the one above.
test('integration form: radios and checkboxes are no bigger than 1.5rem', async ({ browser }) => {
	const page = await (await signedIn(browser, 'admin')).newPage();
	await page.goto('/integrations');
	await page.getByRole('button', { name: 'New integration' }).click();
	const form = page.locator('form.integration-form');
	await expect(form.getByText('Flags (admins only)')).toBeVisible();
	const rem = await page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).fontSize));
	const max = 1.5 * rem;
	const kinds = await form.getByLabel('Send to').locator('option').evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value));
	expect(kinds).toEqual(['webhook', 'mqtt', 'discord', 'telegram', 'pushover', 'ntfy']);
	let checked = 0;
	for (const kind of kinds) {
		await form.getByLabel('Send to').selectOption(kind);
		if (kind === 'mqtt') await form.getByLabel(/Home Assistant discovery/).check();
		const controls = form.locator('input[type="radio"], input[type="checkbox"]');
		const n = await controls.count();
		expect(n, kind).toBeGreaterThan(0);
		for (let i = 0; i < n; i++) {
			const c = controls.nth(i);
			const box = await c.boundingBox();
			const what = `${kind}: ${await c.getAttribute('type')} ${i} (${box?.width}x${box?.height}px, max ${max}px)`;
			expect(box, what).not.toBeNull();
			expect(box!.width, what).toBeLessThanOrEqual(max);
			expect(box!.height, what).toBeLessThanOrEqual(max);
			checked++;
		}
		const radios = form.getByRole('group', { name: 'Content' }).getByRole('radio');
		await expect(radios).toHaveCount(2);
		const [full, summary] = [await radios.nth(0).boundingBox(), await radios.nth(1).boundingBox()];
		expect(Math.abs(full!.x - summary!.x), `${kind}: Content radios line up`).toBeLessThanOrEqual(1);
		expect(summary!.y, `${kind}: Summary is below Full`).toBeGreaterThanOrEqual(full!.y + full!.height);
	}
	console.log(`radios and checkboxes measured: ${checked}, none over ${max}px`);
	await page.context().close();
});

// M6: aliases in the browser (R53-R58). The three browser projects share one
// database, so each edits its own aliases, seeded by scripts/e2e.sh:
// 04001<n>1-04001<n>3 ("Edit test <n>1"...), n = 1, 2, 3 by project.
const projectN = (info: { project: { name: string } }) => ['chromium', 'firefox', 'webkit'].indexOf(info.project.name) + 1 || 9;
const asRole = (role: Role) => ({ Cookie: `pa_session=${SESSIONS[role]}` });

async function aliasIdByName(request: APIRequestContext, name: string): Promise<number> {
	const r = await request.get(`${URL}/api/v1/admin/aliases?q=${encodeURIComponent(name)}`, { headers: asRole('admin') });
	const found = (await r.json()).aliases.find((a: { name: string }) => a.name === name);
	expect(found, name).toBeTruthy();
	return found.id;
}

async function waitingNow(request: APIRequestContext): Promise<number> {
	const r = await request.get(`${URL}/api/v1/meta`, { headers: asRole('admin') });
	return (await r.json()).waiting_requests;
}

// A42. The admin renames an alias in Administration and the message list
// shows the new name at once; a second alias for the capcode is refused
// with the reason.
test('A42 admin edits aliases', async ({ page, browser, request }, info) => {
	const n = projectN(info);
	const capcode = `04001${n}1`;
	await post(request, capcode, `QDA42 page for ${capcode}`);
	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto('/admin/aliases');
	await admin.getByLabel('Find an alias').fill(`Edit test ${n}1`);
	await admin.getByTestId('alias-table').getByRole('link', { name: `Edit test ${n}1` }).click();
	await expect(admin.getByRole('heading', { level: 1 })).toHaveText(`Alias “Edit test ${n}1”`);
	const renamed = `Renamed by admin ${n}`;
	const form = admin.getByTestId('alias-form');
	await form.getByLabel('Name').fill(renamed);
	await form.getByRole('button', { name: 'Save' }).click();
	await expect(admin.getByRole('status')).toContainText('Saved');
	// History, admins only (R57).
	await expect(admin.getByTestId('alias-history')).toContainText(`Name: “Edit test ${n}1” → “${renamed}”`);
	await expect(admin.getByTestId('alias-history')).toContainText('by Test Admin (admin@example.test)');

	// The user's message list shows the new name straight away.
	await page.goto(`/?capcode=${capcode}`);
	await waitLive(page);
	await expect(page.getByTestId('message-list').locator('article').first()).toContainText(renamed);

	// A second alias for the same capcode is refused, naming the one it has.
	await admin.goto('/admin/aliases');
	await admin.getByRole('button', { name: 'Add alias' }).click();
	const add = admin.getByTestId('alias-form');
	await add.getByLabel('Capcode or pattern').fill(capcode.replace(/^0+/, ''));
	await add.getByLabel('Name').fill('Second alias');
	await add.getByRole('button', { name: 'Add alias' }).click();
	await expect(add.getByRole('alert')).toContainText(`already has the alias "${renamed}"`);
	await expect(add.getByRole('alert')).toContainText(`edit "${renamed}" instead`);
	await admin.context().close();
});

// A43. B requests an alias for a capcode with none; the admin sees the
// count on the Admin link, approves it; history names both; B's request
// shows "approved".
test('A43 request approved', async ({ page, browser, request }, info) => {
	const n = projectN(info);
	const capcode = `040020${n}`;
	const name = `Station 9 ${n}`;
	await post(request, '0999999', 'QDPAGE TO AN UNKNOWN PAGER');
	const before = await waitingNow(request);

	await page.goto('/capcodes/0999999');
	await page.getByRole('link', { name: 'Request an alias for this capcode' }).click();
	await expect(page).toHaveURL(/\/requests\?pattern=0999999/);
	const form = page.getByTestId('request-form');
	await expect(form.getByLabel('Capcode or pattern')).toHaveValue('0999999');
	await form.getByLabel('Capcode or pattern').fill(capcode);
	await form.getByLabel('Name').fill(name);
	await form.getByLabel('Template').selectOption({ label: 'Example Fire (Example Fire Service)' });
	await form.getByLabel('Note for the admins (optional)').fill('NOTE-MARKER new pager');
	await form.getByRole('button', { name: 'Send request' }).click();
	await expect(page.getByRole('status').filter({ hasText: 'Your request was sent' })).toBeVisible();
	const mine = page.getByTestId('alias-request').filter({ hasText: capcode });
	await expect(mine.getByTestId('request-status')).toHaveText('Waiting');

	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto('/');
	const link = admin.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Admin' });
	const waitingName = (k: number) => (k === 0 ? 'Admin' : `Admin, ${k} alias request${k === 1 ? '' : 's'} waiting`);
	await expect(link).toHaveAccessibleName(waitingName(before + 1));
	await expect(link).toContainText(String(before + 1));
	await link.click();
	await admin.getByTestId('admin-screens').getByRole('link', { name: 'Alias requests' }).click();
	const card = admin.getByTestId('alias-request').filter({ hasText: `New alias for ${capcode}` });
	await expect(card).toContainText('Requested by Test User (user@example.test)');
	await expect(card).toContainText('NOTE-MARKER new pager');
	await card.getByRole('button', { name: `Approve: the new alias for ${capcode}` }).click();
	await expect(admin.getByRole('status')).toContainText(`Approved the new alias for ${capcode}`);
	await expect(link).toHaveAccessibleName(waitingName(before));

	// The alias exists; its history says B asked and the admin approved.
	const id = await aliasIdByName(request, name);
	await admin.goto(`/aliases/${id}`);
	await expect(admin.getByRole('heading', { level: 1 })).toHaveText(name);
	await expect(admin.getByTestId('alias-history')).toContainText(
		'requested by Test User (user@example.test), approved by Test Admin (admin@example.test)'
	);
	// B: "approved", and no history on the alias view (admins only).
	await page.goto('/requests');
	await expect(
		page.getByTestId('closed-requests').getByTestId('alias-request').filter({ hasText: capcode }).getByTestId('request-status')
	).toHaveText('Approved');
	await page.goto(`/aliases/${id}`);
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(name);
	await expect(page.getByRole('button', { name: 'Request a change' })).toBeVisible();
	await expect(page.getByTestId('alias-history')).toHaveCount(0);
	await expect(page.getByRole('link', { name: 'Edit alias' })).toHaveCount(0);
	await admin.context().close();
});

// A44. B asks to rename an alias; the admin can't reject without feedback,
// then rejects with "Wrong station"; the alias is unchanged and B sees the
// rejection and the feedback.
test('A44 request rejected', async ({ page, browser, request }, info) => {
	const n = projectN(info);
	const current = `Edit test ${n}2`;
	const id = await aliasIdByName(request, current);
	await page.goto(`/aliases/${id}`);
	await waitLive(page);
	await page.getByRole('button', { name: 'Request a change' }).click();
	const form = page.getByTestId('request-form');
	await form.getByLabel('Name').fill(`Station 7 ${n}`);
	await form.getByRole('button', { name: 'Send request' }).click();
	await expect(page.getByRole('status').filter({ hasText: 'Your request was sent' })).toBeVisible();
	// The alias view now shows B's waiting request instead of the buttons.
	await expect(page.getByTestId('alias-request').getByTestId('request-status')).toHaveText('Waiting');

	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto('/admin/requests');
	const card = admin.getByTestId('alias-request').filter({ hasText: `Change “${current}”` });
	await expect(card.getByRole('row', { name: `Name ${current} Station 7 ${n}` })).toBeVisible();
	await card.getByRole('button', { name: /^Reject:/ }).click();
	await card.getByRole('button', { name: 'Reject', exact: true }).click();
	await expect(card.getByRole('alert')).toContainText('needs feedback');
	await card.getByLabel('Feedback for the person who asked (required)').fill('Wrong station');
	await card.getByRole('button', { name: 'Reject', exact: true }).click();
	await expect(admin.getByRole('status')).toContainText('Rejected');
	await admin.context().close();

	await page.goto('/requests');
	const mine = page.getByTestId('closed-requests').getByTestId('alias-request').filter({ hasText: `Change “${current}”` });
	await expect(mine.getByTestId('request-status')).toHaveText('Rejected');
	await expect(mine).toContainText('Feedback: Wrong station');
	await page.goto(`/aliases/${id}`);
	await expect(page.getByRole('heading', { level: 1 })).toHaveText(current);
});

// A45. Out of date: B asks to rename an alias, the admin renames it first;
// B's request shows "changed since requested" with the current and proposed
// names. B asks for an alias for a capcode the admin then aliases; the
// review screen says it can't be approved.
test('A45 out-of-date requests', async ({ page, browser, request }, info) => {
	const n = projectN(info);
	const name = `Edit test ${n}3`;
	const id = await aliasIdByName(request, name);
	await page.goto(`/aliases/${id}`);
	await waitLive(page);
	await page.getByRole('button', { name: 'Request a change' }).click();
	await page.getByTestId('request-form').getByLabel('Name').fill(`Proposed ${n}`);
	await page.getByTestId('request-form').getByRole('button', { name: 'Send request' }).click();
	await expect(page.getByRole('status').filter({ hasText: 'Your request was sent' })).toBeVisible();

	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto(`/admin/aliases/${id}`);
	await admin.getByTestId('alias-form').getByLabel('Name').fill(`Renamed by admin ${n}3`);
	await admin.getByTestId('alias-form').getByRole('button', { name: 'Save' }).click();
	await expect(admin.getByRole('status')).toContainText('Saved');

	await page.goto('/requests');
	const mine = page.getByTestId('alias-request').filter({ hasText: `Proposed ${n}` });
	await expect(mine).toContainText('Changed since requested');
	await expect(mine.getByRole('row', { name: `Name Renamed by admin ${n}3 Proposed ${n}` })).toBeVisible();

	// A new alias for a capcode that got one meanwhile can't be approved.
	const capcode = `040030${n}`;
	await page.getByRole('button', { name: 'Request a new alias' }).click();
	const form = page.getByTestId('request-form');
	await form.getByLabel('Capcode or pattern').fill(capcode);
	await form.getByLabel('Name').fill(`Wanted ${n}`);
	await form.getByRole('button', { name: 'Send request' }).click();
	await expect(page.getByRole('status').filter({ hasText: 'Your request was sent' })).toBeVisible();
	await admin.goto(`/admin/aliases?add=${capcode}`);
	const add = admin.getByTestId('alias-form');
	await expect(add.getByLabel('Capcode or pattern')).toHaveValue(capcode);
	await add.getByLabel('Name').fill(`Taken ${n}`);
	await add.getByRole('button', { name: 'Add alias' }).click();
	await expect(admin.getByRole('heading', { level: 1 })).toHaveText(`Alias “Taken ${n}”`);

	await admin.goto('/admin/requests');
	const stale = admin.getByTestId('alias-request').filter({ hasText: `New alias for ${capcode}` });
	await expect(stale).toContainText(`${capcode} now has the alias “Taken ${n}”: this request can't be approved.`);
	await expect(stale.getByRole('button', { name: /^Approve:/ })).toBeDisabled();
	const changed = admin.getByTestId('alias-request').filter({ hasText: `Proposed ${n}` });
	await expect(changed).toContainText('Changed since requested');
	// Clean up: both rejected, so no request lingers for other tests.
	for (const card of [stale, changed]) {
		await card.getByRole('button', { name: /^Reject:/ }).click();
		await card.getByLabel('Feedback for the person who asked (required)').fill('Wrong station: already done');
		await card.getByRole('button', { name: 'Reject', exact: true }).click();
		await expect(card).toHaveCount(0);
	}
	await admin.context().close();
});

// A46 (browser part): the admin exports every alias as a CSV in the import
// format. The import side runs in Go (TestA46ImportDoesntOverwrite).
test('A46 export downloads every alias in the import format', async ({ browser }) => {
	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto('/admin/aliases');
	const [download] = await Promise.all([admin.waitForEvent('download'), admin.getByTestId('export').click()]);
	expect(download.suggestedFilename()).toMatch(/^pagerarchive-aliases-\d{4}-\d{2}-\d{2}\.csv$/);
	const fs = await import('node:fs/promises');
	const csv = await fs.readFile(await download.path(), 'utf8');
	expect(csv.split('\r\n')[0]).toBe('id,address,alias,agency,icon,color,ignore');
	expect(csv).toContain(',0100001,Station 1,Example Fire,,#b3261e,false\r\n');
	await admin.context().close();
});

// A47 (browser part): users get no way to edit an alias, only to ask. The
// API refusals run in Go (TestA47UsersCantEditDirectly).
test('A47 users can only request changes', async ({ page }) => {
	await page.goto('/admin/aliases');
	await expect(page.getByRole('alert')).toContainText('admins only');
	await page.goto('/');
	await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Requests' }).click();
	await expect(page.getByRole('heading', { level: 1 })).toHaveText('My requests');
	await expect(page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Requests' })).toHaveAttribute('aria-current', 'page');
});

// The sticky header stays above all page content: a row's alias link
// scrolled under it is covered, and a click there reaches the header, not the
// link (the header and the row links once shared one z-index).
for (const width of [375, 1280]) {
	test(`header covers message rows scrolled under it, ${width}px`, async ({ browser, request }) => {
		const job = uniqueJob();
		for (let n = 1; n <= 20; n++) await post(request, '0100001', `@@${job} HSF1 SMOKE FROM ROOF ${n} @ 12 FICTION ST, MADEUPTON #STN1`);
		const page = await (await signedIn(browser, 'user', { viewport: { width, height: 740 } })).newPage();
		await page.goto(`/?job=${job}`);
		const list = page.getByTestId('message-list');
		await expect(list.locator('article')).toHaveCount(20);
		const header = page.locator('header.top');

		// Scroll the third row's alias link to the middle of the header.
		const link = list.locator('article').nth(2).getByRole('link', { name: 'Station 1' });
		const point = await link.evaluate((a) => {
			const h = document.querySelector('header.top')!.getBoundingClientRect();
			const r = a.getBoundingClientRect();
			window.scrollBy(0, r.top + r.height / 2 - (h.top + h.height / 2));
			const now = a.getBoundingClientRect();
			return { x: now.left + Math.min(now.width / 2, 24), y: now.top + now.height / 2 };
		});
		const box = (await header.boundingBox())!;
		expect(point.y, 'the alias link is under the header').toBeGreaterThan(box.y);
		expect(point.y, 'the alias link is under the header').toBeLessThan(box.y + box.height);

		// Click there and see which got it (the click itself goes nowhere).
		await page.evaluate(() => {
			window.addEventListener(
				'click',
				(e) => {
					const t = e.target as Element;
					document.body.dataset.clicked = t.closest('header.top') ? 'header' : t.closest('article') ? 'message row' : t.tagName;
					e.preventDefault();
				},
				{ capture: true, once: true }
			);
		});
		await page.mouse.click(point.x, point.y);
		await expect(page.locator('body')).toHaveAttribute('data-clicked', 'header');
		await expect(page).toHaveURL(new RegExp(`/\?job=${job}$`));
		await page.context().close();
	});
}

// From and To limit each other (min and max), and a range that is still
// reversed is refused in the browser with a clear message, before the API is
// asked (the report API refuses it; search and the map would find nothing).
// Search is checked on the message list; the map uses the same search form.
for (const f of [
	{ name: 'reports', path: '/reports?capcode=0100001', api: '/api/v1/reports/', submit: 'Run report' },
	{ name: 'search', path: '/', api: '/api/v1/messages', submit: 'Search' },
	{ name: 'map', path: '/map', api: '/api/v1/map', submit: 'Search' }
]) {
	test(`${f.name}: To can't be before From`, async ({ page }) => {
		const asked: string[] = [];
		page.on('request', (r) => {
			const u = new globalThis.URL(r.url());
			if (u.pathname.startsWith(f.api) && u.searchParams.has('from')) asked.push(`${u.searchParams.get('from')}/${u.searchParams.get('to')}`);
		});
		await page.goto(f.path);
		const submit = page.getByRole('button', { name: f.submit, exact: true });
		const form = page.locator('form').filter({ has: submit });
		const from = form.getByLabel('From', { exact: true });
		const to = form.getByLabel('To', { exact: true });
		const message = form.getByTestId('date-range-error');

		await from.fill('2026-10-10');
		await expect(to).toHaveAttribute('min', '2026-10-10');
		await to.fill('2026-10-01');
		await expect(from).toHaveAttribute('max', '2026-10-01');
		await expect(message).toHaveText('The To date is before the From date. Choose a To date on or after the From date.');
		await submit.click();
		await page.waitForTimeout(500);
		expect(page.url(), 'the form was not sent').not.toContain('from=');
		await expect(message).toBeVisible();

		// Put right, it's sent.
		await to.fill('2026-10-12');
		await expect(message).toHaveCount(0);
		await submit.click();
		await expect(page).toHaveURL(/from=2026-10-10&to=2026-10-12/);
		await expect.poll(() => asked).toContain('2026-10-10/2026-10-12');

		// A reversed range in the URL is reported, and the API isn't asked.
		await page.goto(`${f.path}${f.path.includes('?') ? '&' : '?'}from=2026-10-10&to=2026-10-01`);
		await expect(page.getByRole('alert').filter({ hasText: 'The To date is before the From date.' })).toBeVisible();
		await page.waitForTimeout(500);
		expect(asked.filter((a) => a === '2026-10-10/2026-10-01'), 'reversed ranges sent to the API').toEqual([]);
	});
}

// M7 (R42a-R42d, ADR 0008): invite trees, the deactivation preview, trusted
// users and records. scripts/e2e.sh seeds each browser project n its own
// copies of A48's tree (t = 48, 49a, 49b, 50a, 50b): the admin invited A,
// A invited B and C, B invited D, and D has an unused invitation.
function a48Tree(t: string, n: number) {
	const k = `${t}-${n}`;
	const who = (l: string) => ({ name: `${l} ${k}`, email: `${l.toLowerCase()}-${k}@example.test`, label: `${l} ${k} (${l.toLowerCase()}-${k}@example.test)` });
	return { a: who('A'), b: who('B'), c: who('C'), d: who('D'), x: who('X') };
}

async function userIdByEmail(request: APIRequestContext, email: string): Promise<number> {
	const r = await request.get(`${URL}/api/v1/admin/users`, { headers: asRole('admin') });
	const u = (await r.json()).find((u: { email: string }) => u.email === email);
	expect(u, email).toBeTruthy();
	return u.id;
}

/** The tree on the page as "A(B(D) C)", by each person's first word. */
function treeShape(page: Page): Promise<string> {
	return page.getByTestId('invite-tree').evaluate((ul) => {
		const shape = (li: Element): string => {
			const who = li.querySelector(':scope > .row .who')?.textContent?.trim().split(' ')[0] ?? '?';
			const kids = [...li.querySelectorAll(':scope > ul > li')].map(shape);
			return kids.length ? `${who}(${kids.join(' ')})` : who;
		};
		return [...ul.querySelectorAll(':scope > li')].map(shape).join(' ');
	});
}

// A48. The admin opens A's page: the tree A → B → D and A → C under the
// admin who invited A; the preview starts with all 4 ticked, and its totals.
test('A48 invite tree and preview', async ({ browser, request }, info) => {
	const tr = a48Tree('48', projectN(info));
	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto(`/admin/users/${await userIdByEmail(request, tr.a.email)}`);
	await expect(admin.getByRole('heading', { level: 1 })).toHaveText(tr.a.label);
	await expect(admin.getByTestId('tree-person').first()).toBeVisible();
	expect(await treeShape(admin)).toBe('Test(A(B(D) C))');
	const d = admin.locator(`[data-person="${tr.d.label}"]`);
	await expect(d).toContainText('User · joined');
	await expect(d).toContainText('1 unused invitation');

	await admin.getByRole('link', { name: 'Deactivate with invite tree…' }).click();
	await expect(admin.getByRole('heading', { level: 1 })).toHaveText(`Deactivate ${tr.a.label} and their invite tree`);
	for (const p of [tr.a, tr.b, tr.c, tr.d]) await expect(admin.getByRole('checkbox', { name: p.label })).toBeChecked();
	expect(await treeShape(admin)).toBe('A(B(D) C)');
	await expect(admin.getByTestId('totals')).toHaveText('4 people will be deactivated and 1 unused invitation cancelled.');
	await admin.context().close();
});

// A49. Unticking only B deactivates A, C and D, and B keeps A as inviter;
// "Spare this branch" on B (a fresh copy) leaves B and D active and D's
// invitation unused.
test('A49 sparing', async ({ browser, request }, info) => {
	const n = projectN(info);
	const admin = await (await signedIn(browser, 'admin')).newPage();

	const tr = a48Tree('49a', n);
	await admin.goto(`/admin/users/${await userIdByEmail(request, tr.a.email)}/deactivate-tree`);
	await admin.getByRole('checkbox', { name: tr.b.label }).uncheck();
	await expect(admin.getByTestId('totals')).toHaveText('3 people will be deactivated and 1 unused invitation cancelled.');
	await admin.getByRole('button', { name: 'Deactivate 3 people' }).click();
	await expect(admin.getByRole('status')).toContainText('Done: 3 people deactivated, 1 person spared');
	await admin.goto(`/admin/users/${await userIdByEmail(request, tr.b.email)}`);
	await expect(admin.locator('dl')).toContainText(`Invited by ${tr.a.label}`);
	await expect(admin.locator('dl')).toContainText('Status Active');
	await expect(admin.getByTestId('tree-person').first()).toBeVisible();
	expect(await treeShape(admin)).toBe('A(B(D))');

	const tr2 = a48Tree('49b', n);
	await admin.goto(`/admin/users/${await userIdByEmail(request, tr2.a.email)}/deactivate-tree`);
	await admin.getByRole('button', { name: `Spare this branch: ${tr2.b.label}` }).click();
	for (const p of [tr2.b, tr2.d]) await expect(admin.getByRole('checkbox', { name: p.label })).not.toBeChecked();
	await expect(admin.getByTestId('totals')).toHaveText('2 people will be deactivated and 0 unused invitations cancelled.');
	await admin.getByRole('button', { name: 'Deactivate 2 people' }).click();
	await expect(admin.getByRole('status')).toContainText('Done: 2 people deactivated, 2 people spared and 0 unused invitations cancelled');
	await admin.goto('/admin/users');
	const row = (label: string) => admin.locator('.cards > li').filter({ has: admin.getByRole('link', { name: label, exact: true }) });
	for (const p of [tr2.b, tr2.d]) await expect(row(p.label).locator('.head')).not.toContainText('Deactivated');
	for (const p of [tr2.a, tr2.c]) await expect(row(p.label).locator('.head')).toContainText('Deactivated');
	// D's invitation is still unused, and still works.
	await expect(admin.locator('.cards > li').filter({ hasText: tr2.x.email })).toContainText(`From ${tr2.d.label}`);
	await admin.context().close();
});

// A50. C is marked trusted and starts unticked; the record shows the reason,
// who was deactivated and who was spared. With a preview open on a fresh
// copy, D's invitation is accepted: confirming is refused and the updated
// preview includes the new person.
test('A50 trusted, record and changes', async ({ browser, request }, info) => {
	const n = projectN(info);
	const admin = await (await signedIn(browser, 'admin')).newPage();

	const tr = a48Tree('50a', n);
	const aID = await userIdByEmail(request, tr.a.email);
	await admin.goto(`/admin/users/${await userIdByEmail(request, tr.c.email)}`);
	await admin.getByRole('button', { name: 'Mark trusted' }).click();
	await expect(admin.getByTestId('trusted')).toContainText('Yes, marked by Test Admin (admin@example.test)');
	await admin.goto(`/admin/users/${aID}/deactivate-tree`);
	await expect(admin.getByRole('checkbox', { name: tr.c.label })).not.toBeChecked();
	await expect(admin.getByRole('checkbox', { name: tr.c.label })).toBeEnabled();
	for (const p of [tr.a, tr.b, tr.d]) await expect(admin.getByRole('checkbox', { name: p.label })).toBeChecked();
	await admin.getByLabel('Reason (optional, only admins see it)').fill('spam REASON-MARKER');
	await admin.getByRole('button', { name: 'Deactivate 3 people' }).click();
	await expect(admin.getByRole('status')).toContainText('Done: 3 people deactivated, 1 person spared');
	await admin.goto(`/admin/users/${aID}`);
	const record = admin.getByTestId('deactivation-record');
	await expect(record).toHaveCount(1);
	await expect(record).toContainText('Reason: spam REASON-MARKER');
	await expect(record).toContainText(`Deactivated: ${tr.a.label}, ${tr.b.label}, ${tr.d.label}`);
	await expect(record).toContainText(`Spared: ${tr.c.label}`);
	await expect(record).toContainText('by Test Admin (admin@example.test)');

	const tr2 = a48Tree('50b', n);
	const a2 = await userIdByEmail(request, tr2.a.email);
	await admin.goto(`/admin/users/${a2}/deactivate-tree`);
	await expect(admin.getByTestId('totals')).toHaveText('4 people will be deactivated and 1 unused invitation cancelled.');
	// D's invitation is accepted while the preview is open.
	sql(`INSERT INTO users (email, email_key, name, role, invited_by, created_at)
  SELECT '${tr2.x.email}', '${tr2.x.email}', '${tr2.x.name}', 'user', id, clock_timestamp() FROM users WHERE email_key = '${tr2.d.email}';
UPDATE invitations SET status = 'used', used_at = now(), used_by = (SELECT id FROM users WHERE email_key = '${tr2.x.email}')
  WHERE email_key = '${tr2.x.email}' AND status = 'pending';`);
	await admin.getByRole('button', { name: 'Deactivate 4 people' }).click();
	await expect(admin.getByRole('alert')).toContainText('The invite tree changed since this preview, so nothing was deactivated.');
	await expect(admin.getByRole('checkbox', { name: tr2.x.label })).toBeChecked();
	await expect(admin.locator(`[data-person="${tr2.x.label}"]`)).toContainText('New');
	expect(await treeShape(admin)).toBe('A(B(D(X)) C)');
	await expect(admin.getByTestId('totals')).toHaveText('5 people will be deactivated and 0 unused invitations cancelled.');
	const users = await request.get(`${URL}/api/v1/admin/users`, { headers: asRole('admin') });
	expect((await users.json()).find((u: { id: number }) => u.id === a2).status, 'nothing was deactivated').toBe('active');
	await admin.getByRole('button', { name: 'Deactivate 5 people' }).click();
	await expect(admin.getByRole('status')).toContainText('Done: 5 people deactivated');
	await admin.context().close();
});

// M10 (ADR 0011). The made-up test icons are in testdata/icons.
const testIcon = (name: string) => join(process.cwd(), '..', 'testdata', 'icons', name);

// A56 in the browser (R64): the admin adds a template, which is listed with 0
// aliases; an alias moved to it shows its agency at once; deleting a template
// that aliases use is refused with how many use it; a user can't change one.
// Each project has its own template and alias ("Rescue test <n>", 05000<n>1).
test('A56 admin manages templates', async ({ page, browser, request }, info) => {
	const n = projectN(info);
	const name = `Example Rescue ${n}`;
	const job = uniqueJob();
	await post(request, `05000${n}1`, `@@${job} HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN4`);

	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto('/admin');
	await admin.getByTestId('admin-screens').getByRole('link', { name: 'Templates' }).click();
	await expect(admin.getByRole('heading', { level: 1 })).toHaveText('Templates');
	await admin.getByRole('button', { name: 'Add template' }).click();
	const form = admin.getByTestId('template-form');
	await form.getByLabel('Name').fill(name);
	await form.getByLabel('Agency', { exact: true }).fill('Example Rescue Service');
	await expect(form.getByLabel('Agency profile')).toHaveValue('');
	await form.getByRole('button', { name: 'Add template' }).click();
	await expect(admin.getByRole('heading', { level: 1 })).toHaveText(`Template “${name}”`);
	await expect(admin.getByRole('status')).toContainText('Template added');
	await admin.getByRole('link', { name: 'All templates' }).click();
	const row = admin.getByTestId('template-table').locator(`tr[data-template="${name}"]`);
	await expect(row.locator('td')).toHaveText([name, 'None', 'Example Rescue Service', 'None', 'None (text only)', '0']);
	await admin.screenshot({ path: info.outputPath('a56-templates.png'), fullPage: true });

	// The alias moves to it, and its page shows the new agency at once.
	await admin.goto(`/admin/aliases/${await aliasIdByName(request, `Rescue test ${n}`)}`);
	const aliasForm = admin.getByTestId('alias-form');
	await aliasForm.getByLabel('Template').selectOption({ label: `${name} (Example Rescue Service)` });
	await aliasForm.getByRole('button', { name: 'Save' }).click();
	await expect(admin.getByRole('status')).toContainText('Saved');
	await page.goto(`/?job=${job}`);
	await page.getByTestId('message-list').locator('article a.open').first().click();
	await expect(page.locator('dt:text-is("Agency") + dd')).toHaveText('Example Rescue Service');
	await admin.goto('/admin/templates');
	await expect(row.locator('td').last()).toHaveText('1');

	// Deleting "Example Fire" is refused, saying how many aliases use it.
	const listed = await (await request.get(`${URL}/api/v1/admin/templates`, { headers: asRole('admin') })).json();
	const fire = listed.find((t: { name: string }) => t.name === 'Example Fire');
	await admin.getByTestId('template-table').getByRole('link', { name: 'Example Fire' }).click();
	await admin.getByRole('button', { name: 'Delete…' }).click();
	await admin.getByRole('button', { name: 'Delete template' }).click();
	await expect(admin.getByRole('alert')).toHaveText(`"Example Fire" can't be deleted: ${fire.aliases} aliases use it. Move them to another template first.`);
	await expect(admin.getByRole('heading', { level: 1 })).toHaveText('Template “Example Fire”');

	// A user can't change a template through the API, nor open the screens.
	const refused = await request.patch(`${URL}/api/v1/admin/templates/${fire.id}`, { headers: asRole('user'), data: { agency: 'Changed by B' } });
	expect(refused.status()).toBe(403);
	await page.goto('/admin/templates');
	await expect(page.getByRole('alert')).toHaveText('This page is for admins only.');
	await admin.context().close();
});

// A57 in the browser (R65, R66): the admin uploads the made-up 32×32 test
// icon to "Example Fire". The message list shows it beside "Station 1" on a
// light tile, in light and dark mode (axe passes), with the text
// alternative "Example Fire Service". Every request the page makes is to
// PagerArchive, and its CSP stops a request anywhere else. The icon is
// fetched once and then reused. An SVG, a 200 KB PNG and a text file renamed
// .png are each refused with the reason, and the icon is unchanged.
test('A57 icons shown', async ({ browser, request }, info) => {
	test.setTimeout(90_000);
	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto('/admin/templates');
	await admin.getByTestId('template-table').getByRole('link', { name: 'Example Fire' }).click();
	const fileInput = admin.getByLabel(/^(Add an icon|Replace it with)$/);
	await fileInput.setInputFiles(testIcon('example-icon.png'));
	await admin.getByRole('button', { name: /^(Upload|Replace) icon$/ }).click();
	await expect(admin.getByRole('status').filter({ hasText: 'Icon saved' })).toBeVisible();
	const current = admin.getByTestId('current-icon').getByRole('img', { name: 'Example Fire Service' });
	await expect(current).toBeVisible();
	const src = (await current.getAttribute('src'))!;
	expect(src).toMatch(/^\/api\/v1\/icons\/[0-9a-f]{64}\.png$/);

	for (const [file, reason] of [
		['example.svg', "SVG icons aren't accepted, because an SVG file can hold scripts and links to other sites."],
		['large-200kb.png', 'An icon file can be at most 100 KB, and this one is larger.'],
		['not-an-image.png', 'Icons must be PNG, ICO, GIF, JPEG or WebP images, and this file is none of those.']
	]) {
		await fileInput.setInputFiles(testIcon(file));
		await admin.getByRole('button', { name: 'Replace icon' }).click();
		await expect(admin.getByTestId('icon-error'), file).toContainText(reason);
		await expect(current, file).toHaveAttribute('src', src);
	}
	await admin.context().close();

	// Something that isn't PagerArchive, which no request may reach.
	const http = await import('node:http');
	let elsewhere = 0;
	const other = http.createServer((_req, res) => {
		elsewhere++;
		res.writeHead(200, { 'Content-Type': 'image/png' }).end();
	});
	await new Promise<void>((r) => other.listen(0, '127.0.0.1', r));
	const probe = `http://127.0.0.1:${(other.address() as { port: number }).port}/elsewhere.png`;
	// The browsers reach PagerArchive through a proxy that counts the icon
	// requests the server gets: each browser fetches an icon once.
	const apiPort = Number(new globalThis.URL(URL).port);
	let iconFetches = 0;
	const proxy = http.createServer((req, res) => {
		if (req.url?.startsWith('/api/v1/icons/')) iconFetches++;
		const up = http.request({ host: '127.0.0.1', port: apiPort, path: req.url, method: req.method, headers: req.headers }, (r) => {
			res.writeHead(r.statusCode ?? 502, r.headers);
			r.pipe(res);
		});
		up.on('error', () => res.destroy());
		req.pipe(up);
	});
	await new Promise<void>((r) => proxy.listen(0, '127.0.0.1', r));
	const app = `http://127.0.0.1:${(proxy.address() as { port: number }).port}`;

	const job = uniqueJob();
	await post(request, '0100001', `@@${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
	try {
		for (const scheme of ['light', 'dark'] as const) {
			const ctx = await signedIn(browser, 'user', { colorScheme: scheme, baseURL: app });
			const page = await ctx.newPage();
			const origins = new Set<string>();
			page.on('request', (r) => {
				if (!r.url().startsWith('data:')) origins.add(new globalThis.URL(r.url()).origin);
			});
			const before = iconFetches;
			const doc = await page.goto('/');
			expect(doc?.headers()['content-security-policy']).toContain("img-src 'self' data:");
			await waitLive(page);
			const row = page.getByTestId('message-list').locator('article').filter({ hasText: job });
			const img = row.getByRole('img', { name: 'Example Fire Service' });
			await expect(img).toBeVisible();
			await expect(img).toHaveAttribute('src', src);
			await expect.poll(() => img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth)).toBe(64);
			// Beside the alias name, on a light tile in both themes.
			await expect(img.locator('xpath=../following-sibling::a[1]')).toHaveText('Station 1');
			expect(await img.locator('..').evaluate((t) => getComputedStyle(t).backgroundColor)).toBe('rgb(255, 255, 255)');
			await row.screenshot({ path: info.outputPath(`a57-row-${scheme}.png`) });
			await page.screenshot({ path: info.outputPath(`a57-list-${scheme}.png`) });
			const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
			expect(results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join('; ')}`), scheme).toEqual([]);

			// Fetched once: the job view and a reload, new documents, reuse it.
			await page.goto(`/jobs/${job}`);
			await expect(page.getByRole('img', { name: 'Example Fire Service' }).first()).toBeVisible();
			await page.reload();
			await expect(page.getByRole('img', { name: 'Example Fire Service' }).first()).toBeVisible();
			await expect.poll(() => page.getByRole('img', { name: 'Example Fire Service' }).first().evaluate((i: HTMLImageElement) => i.naturalWidth)).toBe(64);
			const fetched = iconFetches - before;
			expect(fetched, 'icon requests the server got for three page loads').toBe(1);
			const requested = [...origins];
			expect(requested, 'origins the page requested').toEqual([app]);

			// The page's Content-Security-Policy stops an image from anywhere else.
			const directive = await page.evaluate(
				(u) =>
					new Promise<string>((resolve) => {
						document.addEventListener('securitypolicyviolation', (e) => resolve(e.effectiveDirective), { once: true });
						const i = new Image();
						i.src = u;
						document.body.append(i);
						setTimeout(() => resolve('not blocked'), 5000);
					}),
				probe
			);
			expect(directive).toBe('img-src');
			console.log(`A57 (${scheme}): icon on a white tile, fetched ${fetched} time(s) for 3 page loads, origins requested ${requested.join(' ')}, another site blocked by ${directive}`);

			// Until it changes: a new icon has a new URL, which is fetched.
			if (scheme === 'dark') {
				const fire = (await (await request.get(`${URL}/api/v1/admin/templates`, { headers: asRole('admin') })).json()).find(
					(t: { name: string }) => t.name === 'Example Fire'
				);
				const set = async (file: string) => {
					const res = await request.put(`${URL}/api/v1/admin/templates/${fire.id}/icon`, { headers: asRole('admin'), data: readFileSync(testIcon(file)) });
					expect(res.status(), file).toBe(200);
					return (await res.json()).icon as string;
				};
				const changed = await set('wide.webp');
				expect(changed).not.toBe(src);
				await page.goto(`/jobs/${job}`);
				await expect(page.getByRole('img', { name: 'Example Fire Service' }).first()).toHaveAttribute('src', changed);
				expect(iconFetches - before, 'after the icon changed').toBe(2);
				expect(await set('example-icon.png')).toBe(src);
			}
			await ctx.close();
		}
		expect(elsewhere, 'requests that reached the other site').toBe(0);
	} finally {
		other.close();
		proxy.closeAllConnections();
		proxy.close();
	}
});

// M11 (R67, R68, A60): the print sheet.
const sheetKey = (userId: number) => `pagerarchive:print-sheet:${userId}`;

async function meId(request: APIRequestContext, role: Role): Promise<number> {
	return (await (await request.get(`${URL}/api/v1/me`, { headers: asRole(role) })).json()).id;
}

/** Posts a page as the client and returns its message ID. */
async function postId(request: APIRequestContext, capcode: string, text: string): Promise<number> {
	const res = await request.post(`${URL}/api/v1/pages`, {
		headers: { Authorization: `Bearer ${KEY}` },
		data: { capcode, text, received_at: new Date().toISOString(), baud: 512 }
	});
	expect(res.status()).toBe(200);
	const id = (await res.json()).message_id;
	expect(id, text).toBeGreaterThan(0);
	return id;
}

/** A word no other test or browser project uses, for searching. */
const uniqueRef = () => `SHEET${['chromium', 'firefox', 'webkit'].indexOf(test.info().project.name) + 1}${uniqueJob().slice(2)}`;

/** Everything in this page's localStorage. */
function storage(page: Page): Promise<Record<string, string>> {
	return page.evaluate(() => {
		const out: Record<string, string> = {};
		for (let i = 0; i < localStorage.length; i++) {
			const k = localStorage.key(i)!;
			out[k] = localStorage.getItem(k)!;
		}
		return out;
	});
}

const rowOf = (page: Page, id: number) => page.locator(`article[data-message-id="${id}"]`);
const tickOf = (page: Page, id: number) => rowOf(page, id).getByRole('checkbox', { name: 'Add to print sheet' });
const sheetIds = (page: Page) =>
	page.getByTestId('sheet').locator('li').evaluateAll((els) => els.map((e) => Number((e as HTMLElement).dataset.messageId)));
const listIds = (page: Page) =>
	page.getByTestId('message-list').locator('article').evaluateAll((els) => els.map((e) => Number((e as HTMLElement).dataset.messageId)));
const squash = (s: string) => s.replace(/\s+/g, '');

/** Searches with the search form on this page (the print page's or the
 * search page's), filling the fields given by label ('' empties one). */
async function searchWith(page: Page, fields: Record<string, string>) {
	const form = page.getByRole('search', { name: 'Search messages' });
	if (!(await form.isVisible())) await page.locator('summary', { hasText: 'Search' }).click();
	for (const [label, v] of Object.entries(fields)) await form.getByLabel(label, { exact: true }).fill(v);
	await form.getByRole('button', { name: 'Search' }).click();
}

/** R67: messages are listed here, and none has a tick box. */
async function noTicks(page: Page, label: string) {
	await expect(page.locator('article').first(), label).toBeVisible();
	await expect(page.getByRole('checkbox', { name: 'Add to print sheet' }), label).toHaveCount(0);
	await expect(page.locator('article input[type=checkbox]'), label).toHaveCount(0);
	await expect(page.locator('article').getByText('Print', { exact: true }), label).toHaveCount(0);
}

/** Each paper page's text, from a PDF of the page as Chromium prints it
 * (saved at save). */
async function pdfPages(page: Page, save: string): Promise<string[]> {
	const pdf = await page.pdf({ format: 'A4', margin: { top: '1cm', right: '1cm', bottom: '1cm', left: '1cm' } });
	writeFileSync(save, pdf);
	const { getDocument } = await import('pdfjs-dist/legacy/build/pdf.mjs');
	const task = getDocument({ data: new Uint8Array(pdf) });
	const doc = await task.promise;
	const pages: string[] = [];
	for (let i = 1; i <= doc.numPages; i++) {
		const content = await (await doc.getPage(i)).getTextContent();
		pages.push(content.items.map((it) => ('str' in it ? it.str : '')).join(''));
	}
	await task.destroy();
	return pages;
}

/**
 * R68 with print media, in any engine: only the texts, in order, each in a
 * heavy black border with space between, black on white, wrapped and never
 * cut off, and kept whole across paper pages.
 */
async function checkPrinted(page: Page, texts: string[], label: string) {
	await page.emulateMedia({ media: 'print' });
	try {
		// Nothing but the texts: no header, navigation, heading, buttons,
		// times, aliases or badges.
		await expect(page.getByRole('navigation', { name: 'Main' }), label).toBeHidden();
		await expect(page.locator('header.top'), label).toBeHidden();
		await expect(page.getByRole('heading', { name: 'Print sheet' }), label).toBeHidden();
		for (const b of await page.getByRole('button').all()) await expect(b, label).toBeHidden();
		const shown = await page.evaluate(() => document.body.innerText);
		expect(squash(shown), `${label}: everything printed`).toBe(squash(texts.join('')));

		const looks = await page.getByTestId('sheet').locator('li').evaluateAll((els) =>
			els.map((el) => {
				const s = getComputedStyle(el);
				const t = getComputedStyle(el.querySelector('.text')!);
				const chain: string[] = [];
				for (let p = el.parentElement; p; p = p.parentElement) chain.push(`${p.tagName.toLowerCase()}:${getComputedStyle(p).display}`);
				return {
					display: s.display,
					breakInside: s.breakInside,
					pageBreakInside: s.getPropertyValue('page-break-inside'),
					borders: ['top', 'right', 'bottom', 'left'].map(
						(side) =>
							`${s.getPropertyValue(`border-${side}-width`)} ${s.getPropertyValue(`border-${side}-style`)} ${s.getPropertyValue(`border-${side}-color`)}`
					),
					background: s.backgroundColor,
					colour: t.color,
					whiteSpace: t.whiteSpace,
					overflowWrap: t.overflowWrap,
					overflow: `${t.overflowX} ${t.overflowY} ${s.overflowX} ${s.overflowY}`,
					maxHeight: `${t.maxHeight} ${s.maxHeight}`,
					cut: Math.max(el.scrollWidth - el.clientWidth, el.scrollHeight - el.clientHeight),
					top: el.getBoundingClientRect().top,
					bottom: el.getBoundingClientRect().bottom,
					chain: chain.join(' ')
				};
			})
		);
		expect(looks.length, label).toBe(texts.length);
		const { display, breakInside, pageBreakInside, borders, background, colour, chain } = looks[0];
		console.log(`${label}: message 1 printed with ${JSON.stringify({ display, breakInside, pageBreakInside, border: borders[0], background, colour, chain })}`);
		looks.forEach((l, i) => {
			const what = `${label}: message ${i + 1}`;
			expect(l.display, what).toBe('block');
			expect([l.breakInside, l.pageBreakInside], what).toContain('avoid');
			for (const b of l.borders) {
				const [width, style, ...colour] = b.split(' ');
				expect(parseFloat(width), `${what}: border width`).toBeGreaterThanOrEqual(3);
				expect(style, what).toBe('solid');
				expect(colour.join(' '), what).toBe('rgb(0, 0, 0)');
			}
			expect(l.background, what).toBe('rgb(255, 255, 255)');
			expect(l.colour, what).toBe('rgb(0, 0, 0)');
			expect(l.whiteSpace, what).toBe('pre-wrap');
			expect(l.overflowWrap, what).toBe('anywhere');
			expect(l.overflow, what).toBe('visible visible visible visible');
			expect(l.maxHeight, what).toBe('none none');
			expect(l.cut, `${what}: nothing cut off`).toBeLessThanOrEqual(0);
			// Page breaks are only avoided reliably inside block layout.
			expect(l.chain, what).toMatch(/^(\w+:(block|list-item|contents) ?)+$/);
			if (i > 0) expect(l.top - looks[i - 1].bottom, `${what}: space above`).toBeGreaterThanOrEqual(16);
		});
		const whole = await page.evaluate(() => {
			const b = getComputedStyle(document.body);
			return { background: b.backgroundColor, colour: b.color, wide: document.documentElement.scrollWidth - document.documentElement.clientWidth };
		});
		expect(whole, label).toEqual({ background: 'rgb(255, 255, 255)', colour: 'rgb(0, 0, 0)', wide: 0 });
	} finally {
		// null, not 'screen': page.pdf() prints with print media unless told otherwise.
		await page.emulateMedia({ media: null });
	}
}

// A60. On the print page, B searches and ticks 3 messages in the results,
// then searches for something else and ticks 1 more; the sheet below shows
// them in that order, stores their IDs only, prints only their text, and
// empties when cleared. The message list (with a row arriving live), search
// results, the job view and the alias view have no tick boxes (owner,
// 2026-10-30). One message has a redacted phone number (P1).
test('A60 print sheet', async ({ browser, request }, info) => {
	test.setTimeout(150_000);
	const ref = uniqueRef();
	const job = uniqueJob();
	const a = await postId(request, '0100001', `QDTRAINING DRILL ${ref} ALPHA CHECK THE PUMP AT 3 FICTION ST`);
	const b = await postId(request, '0100002', `QDTRAINING DRILL ${ref} BRAVO TEXT 0412 345 678 WHEN DONE`);
	const c = await postId(request, '0100003', `@@${uniqueJob()} HSF1 TRAINING DRILL ${ref} SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN3`);
	const d = await postId(request, '0100001', `@@${job} HSF1 TRAINING DRILL SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN1`);
	const userId = await meId(request, 'user');
	const ctx = await signedIn(browser, 'user');
	const page = await ctx.newPage();

	// No tick boxes outside the print page: the message list, with a row
	// that arrives live; search results; the job view; the alias view. The
	// texts the list shows, for comparing with the sheet.
	await page.goto('/');
	await waitLive(page);
	const arrived = await postId(request, '0100004', `QDTRAINING DRILL ${ref} ARRIVED LIVE IN THE LIST`);
	await expect(rowOf(page, arrived)).toBeVisible();
	await noTicks(page, 'the message list, with a live row');
	await page.goto(`/?q=${ref}`);
	await waitLive(page);
	await expect(page.getByTestId('message-list').locator('article')).toHaveCount(4);
	await noTicks(page, 'search results');
	const texts: Record<number, string> = {};
	for (const id of [a, b, c]) texts[id] = await rowOf(page, id).locator('p.text').innerText();
	expect(texts[b]).toContain('TEXT REDACTED WHEN DONE');
	expect(texts[c]).not.toContain('@@');
	await page.goto(`/jobs/${job}`);
	texts[d] = await rowOf(page, d).locator('p.text').innerText();
	await noTicks(page, 'the job view');
	await rowOf(page, d).getByRole('link', { name: 'Station 1' }).click();
	await expect(page).toHaveURL(/\/aliases\/\d+/);
	await waitLive(page);
	await noTicks(page, 'the alias view');
	console.log(`A60: no tick boxes in the message list (live row ${arrived} included), search results, the job view or the alias view`);

	// The print page: its own search, with a tick box on each result.
	await expect(page.getByTestId('sheet-link')).toHaveAccessibleName('Print sheet, 0 messages');
	await page.getByTestId('sheet-link').click();
	await expect(page.getByRole('heading', { level: 1, name: 'Print sheet' })).toBeVisible();
	await waitLive(page);
	await searchWith(page, { Text: ref });
	await expect(page).toHaveURL(new RegExp(`/sheet\\?q=${ref}(&|$)`));
	await expect(page.getByTestId('message-list').locator('article')).toHaveCount(4);
	expect(await listIds(page)).toEqual([arrived, c, b, a]);
	// Ticked b (a click), c (the keyboard), a (its visible label).
	for (const id of [a, b, c]) await expect(tickOf(page, id)).not.toBeChecked();
	await tickOf(page, b).click();
	await expect(tickOf(page, b)).toBeChecked();
	await tickOf(page, c).focus();
	await page.keyboard.press('Space');
	await expect(tickOf(page, c)).toBeChecked();
	await rowOf(page, a).getByText('Print', { exact: true }).click();
	await expect(tickOf(page, a)).toBeChecked();
	await expect(page).toHaveURL(new RegExp(`/sheet\\?q=${ref}(&|$)`)); // ticking opened nothing (R35d)
	await expect(page.getByTestId('sheet-link')).toHaveAccessibleName('Print sheet, 3 messages');
	await expect.poll(() => sheetIds(page)).toEqual([b, c, a]);

	// A second search, for the job: d.
	await searchWith(page, { Text: '', 'Job number': job });
	await expect(page).toHaveURL(new RegExp(`/sheet\\?.*job=${job}`));
	await expect(page.getByTestId('message-list').locator('article')).toHaveCount(1);
	await tickOf(page, d).check();
	await expect(page.getByTestId('sheet-link')).toHaveAccessibleName('Print sheet, 4 messages');
	await expect(page.getByTestId('sheet-link')).toContainText('4');
	// Messages already on the sheet show as ticked when listed again.
	await page.goBack();
	await expect(page.getByTestId('message-list').locator('article')).toHaveCount(4);
	for (const id of [a, b, c]) await expect(tickOf(page, id)).toBeChecked();
	await expect(tickOf(page, arrived)).not.toBeChecked();

	// The browser's storage: the 4 IDs, in order, and none of their text.
	const order = [b, c, a, d];
	const stored = await storage(page);
	expect(Object.keys(stored).sort()).toEqual([sheetKey(userId), 'theme'].sort());
	expect(JSON.parse(stored[sheetKey(userId)])).toEqual(order);
	const all = JSON.stringify(stored) + (await page.evaluate(() => JSON.stringify({ ...sessionStorage })));
	for (const t of [...Object.values(texts), ref, job, 'TRAINING', 'DRILL', 'REDACTED', 'FICTION']) expect(all, 'storage').not.toContain(t);
	console.log(`A60: storage holds ${JSON.stringify(stored)}`);

	// The sheet, below the results: in the order added, each text exactly as
	// the message list shows it.
	await expect.poll(() => sheetIds(page)).toEqual(order);
	expect(await page.getByTestId('sheet').locator('p.text').allInnerTexts()).toEqual(order.map((id) => texts[id]));
	await expect(page.getByTestId('sheet')).not.toContainText('0412');
	const below = await page.evaluate(() => {
		const results = document.querySelector('[data-testid="message-list"]')!.getBoundingClientRect();
		const sheet = document.querySelector('[data-testid="sheet"]')!.getBoundingClientRect();
		return sheet.top - results.bottom;
	});
	expect(below, 'the sheet is below the results').toBeGreaterThanOrEqual(0);
	// On screen only (the print checks below find nothing but the texts).
	await expect(page.getByTestId('print-hint')).toHaveText(`In the print dialog, untick "Headers and footers" to leave out the browser's own date, title and address.`);
	for (const scheme of ['light', 'dark'] as const) {
		await page.getByLabel('Colour theme').selectOption(scheme);
		const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
		expect(results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join('; ')}`), scheme).toEqual([]);
		await page.screenshot({ path: info.outputPath(`a60-sheet-${scheme}.png`), fullPage: true });
	}

	// Printed (still in dark mode): the texts alone, black on white; the
	// search and its results don't print. Chromium: the real printout first.
	if (info.project.name === 'chromium') {
		const pages = await pdfPages(page, info.outputPath('a60.pdf'));
		expect(squash(pages.join('')), 'the PDF holds the texts and nothing else').toBe(squash(order.map((id) => texts[id]).join('')));
		expect(pages.join(''), 'the PDF').not.toContain('0412');
		console.log(`A60: PDF of ${pages.length} page(s) holds only the 4 texts, in order: ${JSON.stringify(pages)}`);
	}
	await checkPrinted(page, order.map((id) => texts[id]), 'A60');
	console.log(`A60 (${info.project.name}): sheet ${JSON.stringify(order)}, as ticked in two searches; print media shows only the texts, black on white in dark mode; axe clean in both themes`);

	// Unticked in the results, then removed on the sheet, then cleared
	// (after confirming).
	await tickOf(page, b).uncheck();
	await expect.poll(() => sheetIds(page)).toEqual([c, a, d]);
	await tickOf(page, b).check();
	await expect.poll(() => sheetIds(page)).toEqual([c, a, d, b]);
	await page.getByRole('button', { name: /^Remove the message from Station 1 at / }).first().click();
	await expect.poll(() => sheetIds(page)).toEqual([c, d, b]);
	await expect(tickOf(page, a)).not.toBeChecked();
	await expect(page.getByTestId('sheet-link')).toHaveAccessibleName('Print sheet, 3 messages');
	page.once('dialog', (dlg) => dlg.dismiss());
	await page.getByRole('button', { name: 'Clear', exact: true }).click();
	await expect.poll(() => sheetIds(page)).toEqual([c, d, b]);
	let asked = '';
	page.once('dialog', (dlg) => {
		asked = dlg.message();
		return dlg.accept();
	});
	await page.getByRole('button', { name: 'Clear', exact: true }).click();
	await expect(page.getByTestId('sheet').locator('li')).toHaveCount(0);
	expect(asked).toBe('Remove all 3 messages from the print sheet?');
	await expect(page.getByTestId('sheet-count')).toContainText('The print sheet is empty.');
	await expect(page.getByTestId('sheet-link')).toHaveAccessibleName('Print sheet, 0 messages');
	expect(Object.keys(await storage(page))).toEqual(['theme']);
	for (const id of [a, b, c]) await expect(tickOf(page, id)).not.toBeChecked();
	await ctx.close();
});

// R67: the print page's search is the search page's: for the same criteria,
// the same results in the same order, with the same paging, for a user and
// for an admin; the same form, with the flag filters for admins only. 55
// messages make two pages of results.
test('print page search matches the search page', async ({ browser, request }) => {
	test.setTimeout(150_000);
	const ref = uniqueRef();
	for (let i = 1; i <= 52; i++) await postId(request, `010000${(i % 4) + 1}`, `QDTRAINING DRILL ${ref} ROUTINE ${i}`);
	await postId(request, '0100002', `QETRAINING DRILL ${ref} AN EMERGENCY`);
	await postId(request, '0100001', `@@GARBLED ${ref} TRAINING DRILL`);
	await postId(request, '0999997', `QDTRAINING DRILL ${ref} TO A PAGER WITH NO ALIAS`);
	const searches = [
		`q=${ref}`,
		`q=${ref}&priority=Emergency`,
		`q=${ref}&alias=Station+2`,
		`q=${ref}&unaliased=true`,
		`q=${ref}&capcode=0100003&ignored=true`,
		`q=${ref}&parse_failed=true`
	];
	const fieldsOf = (page: Page) =>
		page.getByRole('search', { name: 'Search messages' }).locator('[name]').evaluateAll((els) => els.map((e) => (e as HTMLInputElement).name));
	for (const role of ['user', 'admin'] as const) {
		const ctx = await signedIn(browser, role);
		const [list, print] = [await ctx.newPage(), await ctx.newPage()];
		await list.goto('/');
		await print.goto('/sheet');
		await expect(print.locator('article').first()).toBeVisible();
		const fields = await fieldsOf(list);
		expect(await fieldsOf(print), `${role}: the same search form`).toEqual(fields);
		expect(fields.includes('parse_failed'), `${role}: flag filters`).toBe(role === 'admin');
		await expect(print.getByText('Flags (admins only)')).toHaveCount(role === 'admin' ? 1 : 0);
		const seen: string[] = [];
		for (const s of searches) {
			await list.goto(`/?${s}`);
			await print.goto(`/sheet?${s}`);
			for (const p of [list, print]) await expect(p.getByRole('status').filter({ hasText: 'Live' })).toBeVisible();
			await expect.poll(async () => (await listIds(print)).length, `${role} ${s}`).toBe((await listIds(list)).length);
			let want = await listIds(list);
			expect(await listIds(print), `${role} ${s}`).toEqual(want);
			// The same paging: older results with the button.
			const more = (p: Page) => p.getByRole('button', { name: 'Load older messages' });
			while (await more(list).count()) {
				await expect(more(print), `${role} ${s}: more`).toHaveCount(1);
				await more(list).click();
				await more(print).click();
				await expect.poll(() => listIds(list)).not.toEqual(want);
				want = await listIds(list);
				await expect.poll(() => listIds(print), `${role} ${s}`).toEqual(want);
			}
			await expect(more(print)).toHaveCount(0);
			seen.push(`${s}: ${want.length}`);
		}
		console.log(`print page search (${role}): the same results as the search page: ${seen.join(', ')}`);
		await ctx.close();
	}
});

// A60: 30 messages of mixed lengths, some long enough to straddle a page
// break if laid out naively, and one with a long unbroken word. Chromium:
// the real printed PDF, each text whole on one page, and, as a control, the
// same sheet without break-inside splits one. Firefox and WebKit: print
// media emulation and the computed styles.
test('A60 thirty messages print without splitting', async ({ browser, request }, info) => {
	test.setTimeout(150_000);
	const ref = uniqueRef();
	const words = ['SMOKE', 'FROM', 'SHED', 'FICTION', 'ST', 'MADEUPTON', 'PUMP', 'CHECK', 'UNIT', 'T1', 'HOSE', 'LADDER', 'TANKER', 'NORTH', 'GATE'];
	const lengths = [40, 400, 1500, 90, 2200, 250, 700, 60, 1800, 120, 900, 2400, 300, 50, 1200, 600, 2000, 150, 80, 1000, 1600, 200, 450, 70, 2300, 350, 130, 1400, 500, 100];
	const ids: number[] = [];
	for (const [i, len] of lengths.entries()) {
		let body = `TRAINING DRILL ${ref} M${i + 1}`;
		if (i === 4) body += ' ' + 'UNBROKEN'.repeat(20);
		for (let w = 0; body.length < len; w++) body += ' ' + words[(i + w) % words.length];
		ids.push(await postId(request, '0100004', `QD${body} END${i + 1}`));
	}
	const ctx = await signedIn(browser, 'user');
	const page = await ctx.newPage();
	await page.goto(`/sheet?q=${ref}`);
	await waitLive(page);
	await expect(page.getByTestId('message-list').locator('article')).toHaveCount(30);
	for (const id of ids) await tickOf(page, id).check(); // oldest first
	const texts: string[] = [];
	for (const id of ids) texts.push(await rowOf(page, id).locator('p.text').innerText());
	await expect.poll(() => sheetIds(page)).toEqual(ids);

	if (info.project.name !== 'chromium') {
		await checkPrinted(page, texts, 'A60 (30)');
		console.log(`A60 (30, ${info.project.name}): print media: 30 messages in heavy black borders, break-inside avoid, block layout, black on white, nothing cut off`);
		await ctx.close();
		return;
	}
	// Chromium: the real printout, then the computed styles.
	const whole = (pages: string[]) => texts.map((t) => pages.flatMap((p, n) => (squash(p).includes(squash(t)) ? [n + 1] : [])));
	const pages = await pdfPages(page, info.outputPath('a60-30.pdf'));
	const on = whole(pages);
	expect(
		on.flatMap((p, i) => (p.length === 1 ? [] : [`M${i + 1} on pages ${JSON.stringify(p)}`])),
		'messages not whole on exactly one page'
	).toEqual([]);
	expect(squash(pages.join('')), 'the PDF holds the texts and nothing else').toBe(squash(texts.join('')));
	console.log(`A60 (30): ${pages.length} PDF pages; the page each message is on: ${on.map((p, i) => `M${i + 1}:${p[0]}`).join(' ')}`);
	await checkPrinted(page, texts, 'A60 (30)');

	// The control: without break-inside, the same sheet splits a message.
	await page.addStyleTag({ content: '[data-testid="sheet"] li { break-inside: auto !important; page-break-inside: auto !important; }' });
	const naive = whole(await pdfPages(page, info.outputPath('a60-30-naive.pdf')));
	const split = naive.flatMap((p, i) => (p.length === 0 ? [`M${i + 1} (${lengths[i]} characters)`] : []));
	expect(split.length, 'messages split without break-inside').toBeGreaterThan(0);
	console.log(`A60 (30): without break-inside, split across pages: ${split.join(', ')}`);
	await ctx.close();
});

// R67: the sheet is kept per user. B's isn't shown to C in the same browser,
// and C's own doesn't touch B's.
test('print sheet is per user', async ({ browser, request }) => {
	const ref = uniqueRef();
	const x = await postId(request, '0100002', `QDTRAINING DRILL ${ref} FOR USER B`);
	const y = await postId(request, '0100003', `QDTRAINING DRILL ${ref} FOR USER C`);
	const [bId, cId] = [await meId(request, 'user'), await meId(request, 'userC')];
	expect(bId).not.toBe(cId);
	const ctx = await signedIn(browser, 'user');
	const page = await ctx.newPage();
	await page.goto(`/sheet?q=${ref}`);
	await tickOf(page, x).check();
	await expect(page.getByTestId('sheet-link')).toHaveAccessibleName('Print sheet, 1 message');

	// C signs in in the same browser.
	await ctx.clearCookies();
	await ctx.addCookies([sessionCookie('userC')]);
	await page.goto('/sheet');
	await expect(page.getByTestId('sheet-count')).toContainText('The print sheet is empty.');
	await expect(page.getByTestId('sheet-link')).toHaveAccessibleName('Print sheet, 0 messages');
	await page.goto(`/sheet?q=${ref}`);
	await expect(tickOf(page, x)).not.toBeChecked();
	await tickOf(page, y).check();
	await page.goto('/sheet');
	await expect.poll(() => sheetIds(page)).toEqual([y]);

	// And B again.
	await ctx.clearCookies();
	await ctx.addCookies([sessionCookie('user')]);
	await page.goto('/sheet');
	await expect.poll(() => sheetIds(page)).toEqual([x]);
	const stored = await storage(page);
	expect(JSON.parse(stored[sheetKey(bId)])).toEqual([x]);
	expect(JSON.parse(stored[sheetKey(cId)])).toEqual([y]);
	console.log(`print sheet per user: ${JSON.stringify(stored)}`);
	await ctx.close();
});

// R67: a tick on a print page result that arrived live; other tabs keep in
// step; a full sheet refuses more, saying so on the print page; messages
// that are gone leave the sheet with a note.
test('print sheet: live rows, other tabs, a full sheet and gone messages', async ({ browser, request }) => {
	const ref = uniqueRef();
	const userId = await meId(request, 'user');
	const ctx = await signedIn(browser, 'user');
	const list = await ctx.newPage();
	const other = await ctx.newPage();
	await other.goto('/sheet');
	await expect(other.getByTestId('sheet-count')).toContainText('The print sheet is empty.');
	await list.goto(`/sheet?q=${ref}`);
	await waitLive(list);
	const live = await postId(request, '0100001', `QDTRAINING DRILL ${ref} ARRIVED LIVE`);
	await expect(tickOf(list, live)).toBeVisible();
	await tickOf(list, live).focus();
	await list.keyboard.press('Space');
	await expect(tickOf(list, live)).toBeChecked();
	// The other tab follows: its link and its sheet.
	await expect(other.getByTestId('sheet-link')).toHaveAccessibleName('Print sheet, 1 message');
	await expect.poll(() => sheetIds(other)).toEqual([live]);
	await other.getByRole('button', { name: /^Remove the message from Station 1/ }).click();
	await expect(tickOf(list, live)).not.toBeChecked();
	// Closed now: an open sheet would drop the made-up IDs below at once.
	await other.close();

	// Full: 100 IDs (of messages that don't exist) fill it; another tick is
	// refused and the print page says why. The sheet's own request fails
	// meanwhile, so it keeps the made-up IDs (a failed request drops none).
	const fake = Array.from({ length: 100 }, (_, i) => 2_000_000_000 + i);
	await list.route('**/api/v1/messages/by-id?*', (r) => r.abort());
	await list.evaluate(([k, v]) => localStorage.setItem(k, v), [sheetKey(userId), JSON.stringify(fake)]);
	await list.reload();
	await expect(list.getByTestId('sheet-link')).toHaveAccessibleName('Print sheet, 100 messages');
	await tickOf(list, live).click();
	await expect(tickOf(list, live)).not.toBeChecked();
	await expect(list.getByTestId('sheet-full')).toHaveText('The print sheet is full: it holds 100 messages. Remove some before adding more.');
	expect(JSON.parse((await storage(list))[sheetKey(userId)])).toEqual(fake);
	await list.unroute('**/api/v1/messages/by-id?*');

	// Gone: none of the 100 exists, so they all leave, with a note.
	await list.reload();
	await expect(list.getByTestId('sheet-gone')).toHaveText('100 messages were no longer available and have been removed from the sheet.');
	await expect(list.getByTestId('sheet-link')).toHaveAccessibleName('Print sheet, 0 messages');

	// A real message deleted (as retention would, R45): it leaves too.
	const kept = await postId(request, '0100002', `QDTRAINING DRILL ${ref} KEPT`);
	const deleted = await postId(request, '0100003', `QDTRAINING DRILL ${ref} DELETED`);
	await list.goto(`/sheet?q=${ref}`);
	await tickOf(list, kept).check();
	await tickOf(list, deleted).check();
	sql(`DELETE FROM messages WHERE id = ${deleted};`);
	await list.goto('/sheet');
	await expect(list.getByTestId('sheet-gone')).toHaveText('1 message was no longer available and has been removed from the sheet.');
	await expect.poll(() => sheetIds(list)).toEqual([kept]);
	expect(JSON.parse((await storage(list))[sheetKey(userId)])).toEqual([kept]);
	await ctx.close();
});

// R67: with storage unavailable, the sheet still works, empty, for the
// page's life.
test('print sheet without storage', async ({ browser, request }) => {
	const ref = uniqueRef();
	const id = await postId(request, '0100001', `QDTRAINING DRILL ${ref} NO STORAGE`);
	const ctx = await signedIn(browser, 'user');
	await ctx.addInitScript(() => {
		for (const m of ['getItem', 'setItem', 'removeItem', 'key', 'clear'] as const) {
			Storage.prototype[m] = () => {
				throw new DOMException('Storage is off', 'SecurityError');
			};
		}
	});
	const page = await ctx.newPage();
	const errors: string[] = [];
	page.on('pageerror', (e) => errors.push(e.message));
	await page.goto(`/sheet?q=${ref}`);
	await tickOf(page, id).check();
	await expect(page.getByTestId('sheet-link')).toHaveAccessibleName('Print sheet, 1 message');
	await expect.poll(() => sheetIds(page)).toEqual([id]);
	await page.reload();
	await expect(page.getByTestId('sheet-count')).toContainText('The print sheet is empty.');
	expect(errors).toEqual([]);
	await ctx.close();
});
