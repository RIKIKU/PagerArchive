/// <reference types="node" />
// Browser acceptance checks (SPEC.md section 9). scripts/e2e.sh starts the API
// on a fresh database with the standard test fixture, a client key, and a
// signed-in user and admin (their session tokens).
import { test, expect, type Browser, type BrowserContextOptions, type Page, type APIRequestContext } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { execFileSync } from 'node:child_process';

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
	execFileSync('docker', ['exec', '-i', process.env.PA_E2E_DB ?? '', 'psql', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres'], {
		input: `INSERT INTO users (subject, email, email_key, name, role, invited_by)
  SELECT 'e2e-${marker}', '${email}', '${email}', 'Demoted ${n}', 'admin', id FROM users WHERE subject = 'e2e-admin';
INSERT INTO sessions (token_hash, user_id, expires_at)
  SELECT sha256(convert_to('${token}', 'UTF8')), id, now() + interval '1 day' FROM users WHERE email_key = '${email}';`
	});
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
const views = ['/', '/?q=SMOKE', '/capcodes', '/jobs/EX0000001', 'detail', 'alias', 'alias-request', '/capcodes/0999999', '/map', '/reports?capcode=0100001&from=2026-01-01&to=2026-12-31', '/admin', '/admin/clients', '/admin/users', 'admin-user', 'admin-tree-preview', '/admin/aliases', 'admin-alias', '/admin/requests', '/admin/retention', '/admin/integration-hosts', '/integrations', 'integration-form', 'selection', '/requests', '/account', '/signin', '/signin?error=deactivated'];
for (const scheme of ['light', 'dark'] as const) {
	for (const width of [375, 1280]) {
		test(`axe: every view, ${scheme}, ${width}px`, async ({ browser, request }) => {
			test.setTimeout(240_000); // 26 views
			await post(request, '0100001', `@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
			await post(request, '0999999', 'QDPAGE TO AN UNKNOWN PAGER');
			const res = await request.get(`${URL}/api/v1/messages?capcode=0100001`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
			const aliasID = (await res.json()).messages[0].alias_id;
			const users = await request.get(`${URL}/api/v1/admin/users`, { headers: { Cookie: `pa_session=${SESSIONS.admin}` } });
			const userID = (await users.json()).find((u: { email: string }) => u.email === 'user@example.test').id;
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
		for (const path of ['/admin', '/admin/users', '/admin/users/1', '/admin/aliases', '/admin/aliases/1', '/admin/requests', '/admin/clients', '/admin/retention', '/admin/integration-hosts']) {
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
		await expect(screens.getByRole('link')).toHaveText(['Users', 'Aliases', 'Alias requests', 'Receiving clients', 'Retention', 'Integration addresses']);
		for (const [link, heading] of [
			['Users', 'Users'],
			['Aliases', 'Aliases'],
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
	execFileSync('docker', ['exec', '-i', process.env.PA_E2E_DB ?? '', 'psql', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres'], {
		input: `INSERT INTO users (email, email_key, name, role, invited_by, created_at)
  SELECT '${tr2.x.email}', '${tr2.x.email}', '${tr2.x.name}', 'user', id, clock_timestamp() FROM users WHERE email_key = '${tr2.d.email}';
UPDATE invitations SET status = 'used', used_at = now(), used_by = (SELECT id FROM users WHERE email_key = '${tr2.x.email}')
  WHERE email_key = '${tr2.x.email}' AND status = 'pending';`
	});
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
