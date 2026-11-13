/// <reference types="node" />
// Browser acceptance checks (SPEC.md section 9). scripts/e2e.sh starts the API
// on a fresh database with the standard test fixture, a client key, and a
// signed-in user and admin (their session tokens).
import { test, expect, type Browser, type BrowserContextOptions, type Locator, type Page, type APIRequestContext } from '@playwright/test';
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

// R8: the units field is shown as Appliances in a message's detail (owner,
// 2026-11-13); its name in the API stays units.
test('R8 units shown as Appliances', async ({ page, request }) => {
	const job = uniqueJob();
	await post(request, '0100001', `@@${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON UNITS T1 T2 #STN1`);
	const res = await request.get(`${URL}/api/v1/messages?job=${job}`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
	const m = (await res.json()).messages[0] as { id: number; units: string[] };
	expect(m.units).toEqual(['T1', 'T2']);
	await page.goto(`/messages/${m.id}`);
	const details = page.locator('.rest dl');
	await expect(details.locator('dt', { hasText: /^Appliances$/ })).toBeVisible();
	await expect(details.locator('dt', { hasText: /^Appliances$/ }).locator('xpath=following-sibling::dd[1]')).toHaveText('T1, T2');
	await expect(details).not.toContainText(/\bUnits\b/);
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

// R82e: the favicon loads before sign-in. From the root URL with no session,
// on the way to the sign-in page, and from a deep page (a relative link would
// break there). A file that isn't there still answers 200, with the app's page
// for the browser's router, so the content type is what proves it.
test('R82e the favicon loads before sign-in, from the root URL', async ({ browser, page }) => {
	const icons: [string, string, number | null][] = [
		['head link[rel="icon"][type="image/svg+xml"]', 'image/svg+xml', null],
		['head link[rel="icon"][type="image/png"]', 'image/png', 32],
		['head link[rel="apple-touch-icon"]', 'image/png', 180]
	];
	const ctx = await browser.newContext();
	const out = await ctx.newPage();
	await out.goto('/');
	await expect(out).toHaveURL(/\/signin/);
	expect((await ctx.cookies()).map((c) => c.name)).not.toContain('pa_session');
	const root = await (await ctx.request.get(`${URL}/`)).text();
	const head = root.slice(0, root.indexOf('</head>'));

	await page.goto('/account/profile');
	await expect(page.getByRole('heading', { level: 1, name: 'Your account' })).toBeVisible();

	for (const [link, type, size] of icons) {
		await expect(out.locator(link)).toHaveCount(1);
		const resolved = (from: Page) => from.locator(link).evaluate((l) => (l as HTMLLinkElement).href);
		const href = await resolved(out);
		expect(head).toContain(`href="${new globalThis.URL(href).pathname}"`);
		for (const from of [out, page]) {
			// Unsigned: this context has no session, whichever page's link it is.
			const res = await ctx.request.get(await resolved(from));
			expect(res.status(), `${href} from ${from.url()}`).toBe(200);
			expect(res.headers()['content-type']).toBe(type);
			if (size) {
				const png = await res.body();
				expect([png.readUInt32BE(16), png.readUInt32BE(20)]).toEqual([size, size]);
			}
		}
		// The sign-in page's Content-Security-Policy lets the browser show it.
		const shown = await out.evaluate(async (src) => {
			const img = new Image();
			img.src = src;
			await img.decode();
			return img.naturalWidth > 0;
		}, href);
		expect(shown).toBe(true);
	}
	await ctx.close();
});

// R44 screens: the account page shows who you are and who invited you; the
// users page lists users for admins, with their inviter.
test('account and users pages', async ({ page, browser }) => {
	await page.goto('/account/profile');
	await expect(page.getByRole('heading', { level: 1, name: 'Your account' })).toBeVisible();
	await expect(page.locator('dl')).toContainText('user@example.test');
	await expect(page.locator('dl')).toContainText('Test Admin (admin@example.test)');
	// Inviting has its own page since M13 (owner, 2026-11-02; ADR 0015).
	await page.goto('/account/invite');
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
const views = ['/', '/?q=SMOKE', '/capcodes', '/jobs/EX0000001', 'detail', 'alias', 'alias-request', '/capcodes/0999999', '/map', '/reports/capcode?capcode=0100001&from=2026-01-01&to=2026-12-31', '/admin', '/admin/clients', '/admin/users', 'admin-user', 'admin-tree-preview', '/admin/aliases', 'admin-alias', '/admin/templates', 'admin-template', '/admin/requests', '/admin/retention', '/admin/backups', '/admin/integration-hosts', '/integrations', 'integration-form', 'selection', '/requests', '/sheet', '/account/profile', '/account/invite', '/tools', '/reports', '/account', '/albums', 'album', '/large-events', 'large-event', '/admin/large-events', '/signin', '/signin?error=deactivated'];
for (const scheme of ['light', 'dark'] as const) {
	for (const width of [375, 1280]) {
		test(`axe: every view, ${scheme}, ${width}px`, async ({ browser, request }) => {
			test.setTimeout(360_000); // 35 views
			await post(request, '0100001', `@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
			await post(request, '0999999', 'QDPAGE TO AN UNKNOWN PAGER');
			const res = await request.get(`${URL}/api/v1/messages?capcode=0100001`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
			const aliasID = (await res.json()).messages[0].alias_id;
			const users = await request.get(`${URL}/api/v1/admin/users`, { headers: { Cookie: `pa_session=${SESSIONS.admin}` } });
			const userID = (await users.json()).find((u: { email: string }) => u.email === 'user@example.test').id;
			const templates = await request.get(`${URL}/api/v1/admin/templates`, { headers: { Cookie: `pa_session=${SESSIONS.admin}` } });
			const templateID = (await templates.json()).find((t: { name: string }) => t.name === 'Example Fire').id;
			// M14: an album with a message, its description and a note (R82f).
			const albumName = `Axe ${test.info().project.name} ${scheme} ${width} ${Date.now()}`;
			const album = await (await request.post(`${URL}/api/v1/albums`, { headers: { Cookie: `pa_session=${SESSIONS.user}` }, data: { name: albumName, description: 'For the axe check' } })).json();
			await request.put(`${URL}/api/v1/albums/${album.id}/messages/${(await res.json()).messages[0].id}`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
			await request.put(`${URL}/api/v1/albums/${album.id}/messages/${(await res.json()).messages[0].id}/note`, { headers: { Cookie: `pa_session=${SESSIONS.user}` }, data: { note: 'Why it was kept' } });
			// M22: a large event (R87c), from a made-up deployment code.
			await post(request, '0100002', `@@${uniqueJob()} GRS3 DG98${scheme === 'light' ? 1 : 2}${width === 375 ? 1 : 2} GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2`);
			const eventID = (await (await request.get(`${URL}/api/v1/large-events`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } })).json()).events[0].id;
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
				// R81 (M13): phones can't set up an integration, so its form is
				// checked on a desktop only.
				if (width === 375 && (view === 'integration-form' || view === 'selection')) continue;
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
						view === 'large-event'
							? `/large-events/${eventID}`
							: view === 'album'
							? `/albums/${album.id}`
							: view === 'alias'
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
					else if (path.startsWith('/reports/capcode')) await expect(page.getByTestId('report')).toBeVisible();
					else if (path.startsWith('/?') || path === '/' || path.startsWith('/aliases/') || path.startsWith('/capcodes/')) await waitLive(page);
					else if (path.startsWith('/albums/')) await expect(page.getByTestId('album-note')).toBeVisible();
					else if (path.startsWith('/large-events/')) await waitLive(page);
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

// M4. R38 in the browser (A11 itself runs against the API in Go): the
// report for a capcode, for an alias and for a range alias, each picked with
// the search box's suggestion control; the address names what was picked,
// so the report comes back when it's reloaded.
test('capcode report counts Emergency messages by category', async ({ page, request }, info) => {
	// Each browser project gets its own capcodes and aliases: they share one
	// database. scripts/e2e.sh gives alias "Report test n" to 030010n and
	// "Report range n" to 00700n0-00700n9.
	const n = projectN(info);
	const capcode = `030010${n}`;
	for (const t of ['HSF1 SMOKE', 'HSF2 SMOKE', 'GRS3 GRASS FIRE']) {
		await post(request, capcode, `@@${uniqueJob()} ${t} @ 12 FICTION ST, MADEUPTON #STN1`);
	}
	await post(request, capcode, 'QDTraining Tuesday 7pm at the station');
	await post(request, `00700${n}1`, `@@${uniqueJob()} HSF1 SMOKE @ 3 FICTION ST, MADEUPTON #STN1`);
	await post(request, `00700${n}2`, `@@${uniqueJob()} GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1`);
	const table = page.getByTestId('report');
	const box = page.getByRole('combobox', { name: 'Alias or capcode' });
	const options = page.getByRole('listbox', { name: 'Aliases and capcodes' });

	// An address from before the box (a capcode) still works.
	await page.goto(`/reports?capcode=${capcode}`);
	await expect(box).toHaveValue(`capcode:${capcode}`);
	await page.getByRole('button', { name: 'Run report' }).click();
	await expect(table.getByRole('row', { name: 'Structure fire 2' })).toBeVisible();
	await expect(table.getByRole('row', { name: 'Grass fire 1' })).toBeVisible();
	await expect(table.getByRole('row', { name: 'Total 3' })).toBeVisible();

	// A capcode typed, picked from the suggestions.
	await page.goto('/reports/capcode');
	await box.fill(capcode.slice(1));
	await options.getByRole('option', { name: `capcode:${capcode}` }).click();
	await page.getByRole('button', { name: 'Run report' }).click();
	await expect(page).toHaveURL(new RegExp(`/reports/capcode\\?capcode=${capcode}&from=`));
	await expect(table.getByRole('row', { name: 'Total 3' })).toBeVisible();

	// An alias, by part of its name, with the mouse.
	await page.goto('/reports/capcode');
	await box.fill(`report test ${n}`);
	await options.getByRole('option', { name: `alias:"Report test ${n}"` }).click();
	await expect(box).toHaveValue(`alias:"Report test ${n}" `);
	await page.getByRole('button', { name: 'Run report' }).click();
	await expect(page).toHaveURL(new RegExp(`/reports/capcode\\?alias=Report\\+test\\+${n}&from=`));
	await expect(table.locator('caption')).toContainText(`Alias Report test ${n} (${capcode})`);
	await expect(table.getByRole('row', { name: 'Structure fire 2' })).toBeVisible();
	await expect(table.getByRole('row', { name: 'Total 3' })).toBeVisible();
	await page.reload();
	await expect(box).toHaveValue(`alias:"Report test ${n}"`);
	await expect(table.getByRole('row', { name: 'Total 3' })).toBeVisible();

	// A range alias, by keyboard: every capcode it matches.
	await page.goto('/reports/capcode');
	await box.focus();
	await page.keyboard.type(`range ${n}`);
	await expect(options.getByRole('option')).toHaveCount(1);
	await page.keyboard.press('ArrowDown');
	await page.keyboard.press('Enter');
	await expect(box).toHaveValue(`alias:"Report range ${n}" `);
	await page.keyboard.press('Enter');
	await expect(page).toHaveURL(new RegExp(`alias=Report\\+range\\+${n}&`));
	await expect(table.locator('caption')).toContainText(`Alias Report range ${n} (00700${n}_)`);
	await page.screenshot({ path: info.outputPath('report-range-alias.png'), fullPage: true });
	await expect(table.getByRole('row', { name: 'Structure fire 1' })).toBeVisible();
	await expect(table.getByRole('row', { name: 'Grass fire 1' })).toBeVisible();
	await expect(table.getByRole('row', { name: 'Total 2' })).toBeVisible();

	// Something that's neither is refused, without asking the API.
	await box.fill('no such alias anywhere');
	await page.getByRole('button', { name: 'Run report' }).click();
	await expect(page.getByRole('alert')).toHaveText(`There's no alias "no such alias anywhere". Pick one from the suggestions.`);
});

// R78, A6: the Unknown capcodes page lists the most messages first, then
// the latest seen; capcodes with 5 or fewer messages are hidden until "Show
// them", which lists them too, in the same order.
test('R78 unknown capcodes: order, and the one-offs hidden', async ({ page, request }, info) => {
	const n = projectN(info);
	const c = (k: number) => `0978${n}0${k}`;
	const posts: [string, number][] = [
		[c(1), 7],
		[c(2), 6],
		[c(3), 6], // seen after c(2)
		[c(4), 5],
		[c(5), 1]
	];
	for (const [capcode, count] of posts) {
		for (let i = 0; i < count; i++) await post(request, capcode, `QDR78 page ${i} to ${capcode}`);
	}
	await page.goto('/capcodes');
	const rows = page.getByRole('row');
	const listed = async () => (await rows.allInnerTexts()).map((t) => t.split(/\s/)[0]).filter((t) => t.startsWith(`0978${n}`));
	await expect(page.getByRole('link', { name: c(1), exact: true })).toBeVisible();
	expect(await listed()).toEqual([c(1), c(3), c(2)]);
	const few = page.getByTestId('few');
	await expect(few).toContainText(/^And \d+ more with 5 or fewer messages:/);
	await expect(page.getByRole('link', { name: c(4), exact: true })).toHaveCount(0);
	await few.getByRole('button', { name: 'Show them' }).click();
	await expect(few).toHaveCount(0);
	await expect(page.getByRole('link', { name: c(4), exact: true })).toBeVisible();
	expect(await listed()).toEqual([c(1), c(3), c(2), c(4), c(5)]);
	await expect(page.getByRole('row').filter({ hasText: c(4) }).getByRole('cell').nth(1)).toHaveText('5');
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
		for (const path of ['/admin', '/admin/users', '/admin/users/1', '/admin/aliases', '/admin/aliases/1', '/admin/templates', '/admin/templates/1', '/admin/requests', '/admin/clients', '/admin/retention', '/admin/backups', '/admin/integration-hosts']) {
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
		await expect(screens.getByRole('link')).toHaveText(['Users', 'Aliases', 'Templates', 'Alias requests', 'Receiving clients', 'Retention', 'Backups', 'Integration addresses', 'Large event settings']);
		for (const [link, heading] of [
			['Users', 'Users'],
			['Aliases', 'Aliases'],
			['Templates', 'Templates'],
			['Alias requests', 'Alias requests'],
			['Receiving clients', 'Receiving clients'],
			['Retention', 'Retention'],
			['Backups', 'Backups'],
			['Integration addresses', 'Integration addresses'],
			['Large event settings', 'Large event settings']
		]) {
			await screens.getByRole('link', { name: link }).click();
			await expect(admin.getByRole('heading', { level: 1 })).toHaveText(heading);
			await expect(admin.getByRole('alert')).toHaveCount(0);
			await admin.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Administration' }).click();
		}

		// The Users page links to the invite form in Account (R41, R44b), a page
		// of its own since M13 (owner, 2026-11-02; ADR 0015).
		await screens.getByRole('link', { name: 'Users' }).click();
		await admin.getByRole('main').getByRole('link', { name: 'Invite someone' }).click();
		await expect(admin).toHaveURL(/\/account\/invite$/);
		await expect(admin.getByRole('heading', { level: 1, name: 'Invite someone' })).toBeInViewport();
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
	// R44b: from M13 the count is on the side menu's Administration group.
	const waitingName = (k: number) => (k === 0 ? 'Administration' : `Administration, ${k} alias request${k === 1 ? '' : 's'} waiting`);
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

// The range calendar (R33a's Dates, M21) can't make a To before the From: a
// second day before the first swaps them. It's the same control on the
// search page, the map and the capcode report. A range that is reversed in
// the URL is reported, and the API isn't asked (the report API refuses it;
// search and the map would find nothing).
for (const f of [
	{ name: 'reports', path: '/reports?capcode=0100001', api: '/api/v1/reports/', submit: 'Run report' },
	{ name: 'search', path: '/', api: '/api/v1/messages', submit: '' },
	{ name: 'map', path: '/map', api: '/api/v1/map', submit: '' }
]) {
	test(`${f.name}: To can't be before From`, async ({ page }) => {
		const asked: string[] = [];
		page.on('request', (r) => {
			const u = new globalThis.URL(r.url());
			if (u.pathname.startsWith(f.api) && u.searchParams.has('from')) asked.push(`${u.searchParams.get('from')}/${u.searchParams.get('to')}`);
		});
		await page.goto(f.path);
		const days = await pickBackwards(page, f.submit ? page.locator('main form') : page.getByRole('search', { name: 'Search messages' }));
		// The search box searches when the calendar closes; the report's form
		// is sent with its button.
		if (f.submit) await page.getByRole('button', { name: f.submit, exact: true }).click();
		await expect(page).toHaveURL(new RegExp(`from=${days.first}&to=${days.third}`));
		await expect.poll(() => asked).toContain(`${days.first}/${days.third}`);

		// A reversed range in the URL is reported, and the API isn't asked.
		await page.goto(`${f.path}${f.path.includes('?') ? '&' : '?'}from=2026-10-10&to=2026-10-01`);
		await expect(page.getByRole('alert').filter({ hasText: 'The To date is before the From date.' })).toBeVisible();
		await page.waitForTimeout(500);
		expect(asked.filter((a) => a === '2026-10-10/2026-10-01'), 'reversed ranges sent to the API').toEqual([]);
	});
}

/** The month A82 picks in: this one in the deployment's time zone (scripts/
 *  e2e.sh's TZ), or the one before when today is before its 3rd. Its 1st and
 *  3rd as YYYY-MM-DD, and its days' accessible names' ending ("November 2026"). */
function pickMonth() {
	const [y, m, d] = new Intl.DateTimeFormat('en-CA', { timeZone: 'Australia/Melbourne' }).format(new Date()).split('-').map(Number);
	const first = new Date(Date.UTC(y, m - 1 - (d < 3 ? 1 : 0), 1));
	const iso = (n: number) => new Date(first.getTime() + (n - 1) * 864e5).toISOString().slice(0, 10);
	const name = new Intl.DateTimeFormat('en-AU', { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(first);
	const short = new Intl.DateTimeFormat('en-AU', { month: 'short', timeZone: 'UTC' }).format(first);
	const thisYear = y === first.getUTCFullYear();
	return { first: iso(1), second: iso(2), third: iso(3), fourth: iso(4), name, label: `1 ${short}${thisYear ? '' : ` ${first.getUTCFullYear()}`} – 3 ${short}${thisYear ? '' : ` ${first.getUTCFullYear()}`}`, back: d < 3, today: Date.UTC(y, m - 1, d) };
}

/** Opens the calendar in scope and picks the 3rd, then the 1st, with the
 *  mouse; Done closes it. */
async function pickBackwards(page: Page, scope: Locator) {
	const days = pickMonth();
	await scope.getByRole('button', { name: /^Dates/ }).click();
	const calendar = page.getByRole('dialog', { name: 'Choose dates' });
	await expect(calendar).toBeVisible();
	if (days.back) await calendar.getByRole('button', { name: 'Previous month' }).click();
	await expect(calendar.getByRole('heading')).toHaveText(days.name);
	const cell = (n: number) => calendar.getByRole('gridcell', { name: new RegExp(` ${n} ${days.name}$`) });
	await cell(3).click();
	await cell(1).click();
	await expect(scope.getByRole('button', { name: `Dates: ${days.label}`, exact: true })).toBeVisible();
	await calendar.getByRole('button', { name: 'Done' }).click();
	await expect(calendar).toBeHidden();
	return days;
}

// R33a's Dates: the calendar's title switches to the year's months, and the
// year's to a grid of years; picking a year shows its months, a month its
// days. Years with no pages (from the archive's first and last pages) and
// months after this one are greyed out. By mouse, then by keyboard, and at
// phone width.
for (const width of [1280, 375]) {
	test(`calendar jumps to a month and a year, ${width}px`, async ({ page, request }, info) => {
		await page.setViewportSize({ width, height: 800 });
		const meta = await (await request.get(`${URL}/api/v1/meta`, { headers: asRole('user') })).json();
		const [y, m] = new Intl.DateTimeFormat('en-CA', { timeZone: 'Australia/Melbourne' }).format(new Date()).split('-').map(Number);
		const firstYear = Number(String(meta.first_page).slice(0, 4));
		const lastYear = Number(String(meta.last_page).slice(0, 4));
		const monthYear = (yy: number, mm: number) => new Intl.DateTimeFormat('en-AU', { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(new Date(Date.UTC(yy, mm - 1, 1)));
		await page.goto('/');
		const scope = page.getByRole('search', { name: 'Search messages' });
		await scope.getByRole('button', { name: /^Dates/ }).click();
		const calendar = page.getByRole('dialog', { name: 'Choose dates' });

		// Mouse: the title, a month, the year, a year.
		await calendar.getByRole('button', { name: monthYear(y, m), exact: true }).click();
		const months = calendar.getByRole('grid', { name: String(y) });
		await expect(months.getByRole('gridcell')).toHaveCount(12);
		await page.screenshot({ path: info.outputPath(`calendar-months-${width}.png`) });
		await expect(months.getByRole('gridcell', { name: monthYear(y, m) })).toBeFocused();
		if (m < 12) await expect(months.getByRole('gridcell', { name: monthYear(y, m + 1) })).toHaveAttribute('aria-disabled', 'true');
		await expect(months.getByRole('gridcell', { name: monthYear(y, 1) })).not.toHaveAttribute('aria-disabled', 'true');
		await months.getByRole('gridcell', { name: monthYear(y, 1) }).click();
		await expect(calendar.getByRole('heading')).toHaveText(monthYear(y, 1));
		await expect(calendar.getByRole('grid', { name: monthYear(y, 1) })).toBeVisible();

		await calendar.getByRole('button', { name: monthYear(y, 1), exact: true }).click();
		await calendar.getByRole('button', { name: String(y), exact: true }).click();
		const from = y - (y % 12);
		const years = calendar.getByRole('grid', { name: `${from} – ${from + 11}` });
		await expect(years.getByRole('gridcell')).toHaveCount(12);
		await page.screenshot({ path: info.outputPath(`calendar-years-${width}.png`) });
		for (let yy = from; yy < from + 12; yy++) {
			const cell = years.getByRole('gridcell', { name: String(yy), exact: true });
			if (yy >= firstYear && yy <= lastYear && yy <= y) await expect(cell).not.toHaveAttribute('aria-disabled', 'true');
			else await expect(cell, `${yy} has no pages`).toHaveAttribute('aria-disabled', 'true');
		}
		// A greyed year can't be picked; one with pages can.
		await years.getByRole('gridcell', { name: String(y + 1), exact: true }).click({ force: true });
		await expect(years).toBeVisible();
		await years.getByRole('gridcell', { name: String(y), exact: true }).click();
		await expect(calendar.getByRole('grid', { name: String(y) })).toBeVisible();
		// The arrows move a year in the months, and 12 in the years.
		await calendar.getByRole('button', { name: 'Previous year' }).click();
		await expect(calendar.getByRole('heading')).toHaveText(String(y - 1));
		await calendar.getByRole('button', { name: 'Next year' }).click();

		// Keyboard: Enter on the title, arrows, Enter on a month, Escape.
		await calendar.getByRole('button', { name: String(y), exact: true }).press('Enter');
		await expect(years.getByRole('gridcell', { name: String(y), exact: true })).toBeFocused();
		await page.keyboard.press('ArrowRight');
		await expect(years.getByRole('gridcell', { name: String(y + 1), exact: true })).toBeFocused();
		await page.keyboard.press('Enter'); // greyed: stays
		await expect(years).toBeVisible();
		await page.keyboard.press('ArrowLeft');
		await page.keyboard.press('Enter');
		const again = calendar.getByRole('grid', { name: String(y) });
		await expect(again.getByRole('gridcell', { name: monthYear(y, 1) })).toBeFocused();
		await page.keyboard.press('ArrowDown'); // 3 a row: April
		await expect(again.getByRole('gridcell', { name: monthYear(y, 4) })).toBeFocused();
		await page.keyboard.press('Enter');
		await expect(calendar.getByRole('heading')).toHaveText(monthYear(y, 4));
		await expect(calendar.getByRole('grid', { name: monthYear(y, 4) }).locator('[tabindex="0"]')).toBeFocused();
		await page.keyboard.press('Escape');
		await expect(calendar).toBeHidden();

		// Phone width: the grids fit the screen.
		await scope.getByRole('button', { name: /^Dates/ }).click();
		await calendar.getByRole('button', { name: monthYear(y, m), exact: true }).click();
		const box = await calendar.boundingBox();
		expect(box && box.x >= 0 && box.x + box.width <= width, `calendar inside ${width}px: ${JSON.stringify(box)}`).toBe(true);
		const results = await new AxeBuilder({ page }).include('[role="dialog"]').withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
		expect(results.violations.map((v) => v.id)).toEqual([]);
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

/** Searches with the search box on this page (the print page's or the
 * search page's): since M13 one box (R33a), so filters are typed in it
 * (job:EX0000001). */
async function searchWith(page: Page, text: string) {
	const form = page.getByRole('search', { name: 'Search messages' });
	await form.getByRole('combobox', { name: 'Search' }).fill(text);
	await form.getByRole('button', { name: 'Search', exact: true }).click();
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
	await searchWith(page, ref);
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
	await searchWith(page, `job:${job}`);
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
	await expect(page.getByTestId('sheet')).not.toContainText('0412 345');
	const below = await page.evaluate(() => {
		const results = document.querySelector('[data-testid="message-list"]')!.getBoundingClientRect();
		const sheet = document.querySelector('[data-testid="sheet"]')!.getBoundingClientRect();
		return sheet.top - results.bottom;
	});
	expect(below, 'the sheet is below the results').toBeGreaterThanOrEqual(0);
	// On screen only (the print checks below find nothing but the texts).
	await expect(page.getByTestId('print-hint')).toHaveText(`In the print dialog, untick "Headers and footers" to leave out the browser's own date, title and address.`);
	for (const scheme of ['light', 'dark'] as const) {
		// Since M13 the theme is in the account menu (ADR 0015, decision 5).
		await page.getByRole('button', { name: /^Account menu/ }).click();
		await page.getByLabel('Colour theme').selectOption(scheme);
		await page.keyboard.press('Escape');
		const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
		expect(results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join('; ')}`), scheme).toEqual([]);
		await page.screenshot({ path: info.outputPath(`a60-sheet-${scheme}.png`), fullPage: true });
	}

	// Printed (still in dark mode): the texts alone, black on white; the
	// search and its results don't print. Chromium: the real printout first.
	if (info.project.name === 'chromium') {
		const pages = await pdfPages(page, info.outputPath('a60.pdf'));
		expect(squash(pages.join('')), 'the PDF holds the texts and nothing else').toBe(squash(order.map((id) => texts[id]).join('')));
		expect(pages.join(''), 'the PDF').not.toContain('0412 345');
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
		// Since M13 one box (R33a), with no field per filter: the flag filters
		// are its flag: key, offered to admins only.
		await print.getByRole('combobox', { name: 'Search' }).fill('fl');
		await expect(print.getByRole('listbox', { name: 'Search suggestions' }).getByRole('option', { name: 'flag:' }), `${role}: flag filters`).toHaveCount(role === 'admin' ? 1 : 0);
		await print.getByRole('combobox', { name: 'Search' }).fill('');
		await expect(print.getByText('Flags (admins only)')).toHaveCount(role === 'admin' ? 1 : 0);
		const seen: string[] = [];
		for (const s of searches) {
			await list.goto(`/?${s}`);
			await print.goto(`/sheet?${s}`);
			for (const p of [list, print]) await expect(p.getByRole('status').filter({ hasText: 'Live' })).toBeVisible();
			await expect.poll(async () => (await listIds(print)).length, `${role} ${s}`).toBe((await listIds(list)).length);
			let want = await listIds(list);
			expect(await listIds(print), `${role} ${s}`).toEqual(want);
			// The same paging: older results with the button. On the search
			// page, scrolling to the button also loads them and removes it, so
			// it's pressed there without scrolling (a click scrolls first, and
			// in Firefox and WebKit then waits for a button that's gone).
			const more = (p: Page) => p.getByRole('button', { name: 'Load older messages' });
			while (await more(list).count()) {
				await expect(more(print), `${role} ${s}: more`).toHaveCount(1);
				await more(list).dispatchEvent('click');
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

// M13 (ADR 0015): the redesign. Screenshots of each check go to its output
// folder; with PA_M13_SHOTS set to a folder, the screenshots test writes the
// full set there.

/** The breadcrumbs as shown: each level's text, and where it links. */
function crumbsOf(page: Page) {
	return page.getByRole('navigation', { name: 'Breadcrumb' }).evaluate((nav) =>
		[...nav.querySelectorAll('li')].map((li) => {
			const a = li.querySelector('a');
			return { label: li.textContent?.trim() ?? '', href: a ? new globalThis.URL(a.href).pathname : null };
		})
	);
}

async function axeOf(page: Page) {
	// A tooltip fading in or out (app.css's .has-tip) is part-transparent, so
	// its contrast mid-fade is no one's settled view: once, in Firefox, A82's
	// scan caught Clear all's tooltip fading in. Every tooltip's transition,
	// its delay included, finishes before the scan.
	await page.waitForFunction(
		() =>
			document
				.getAnimations()
				.every((a) => !(a.effect instanceof KeyframeEffect && a.effect.target instanceof Element && a.effect.target.classList.contains('tooltip'))),
		undefined,
		{ timeout: 5_000 }
	);
	const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
	return results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join('; ')}`);
}

// A61. One search box: suggestions, partial and exact values, several values
// for a key, text with colons, the address, and flag: for admins only, with
// the keyboard alone.
test('A61 one search box', async ({ browser, request }, info) => {
	const marker = `FINDME${projectN(info)}${uniqueJob().slice(2)}`;
	await post(request, '0600001', `@@${marker} SMOKE AT HILLSIDE NORTH`);
	await post(request, '0600002', `QD${marker} DRILL AT HILLSIDE SOUTH`);
	await post(request, '0100001', `@@${marker} SMOKE AT STATION ONE`);
	await post(request, '0100001', `QD${marker} DRILL AT STATION ONE`);
	await post(request, '0100002', `@@${marker} SMOKE AT STATION TWO`);
	await post(request, '0100003', `@@${marker} SMOKE AT STATION THREE`);
	await post(request, '0100004', `QD${marker} MEET AT 14:05 AT STATION FOUR`);
	const page = await (await signedIn(browser, 'user')).newPage();
	await page.goto('/');
	await waitLive(page);
	const box = page.getByRole('combobox', { name: 'Search' });
	const options = page.getByRole('listbox', { name: 'Search suggestions' }).getByRole('option');
	const announced = page.getByRole('search', { name: 'Search messages' }).locator('[aria-live]');
	const list = page.getByTestId('message-list');
	const who = async () => (await list.locator('article .who').allTextContents()).sort();

	// alias:hill suggests both Hillside aliases, and no Station; it's said how many.
	await box.fill('alias:hill');
	await expect(options).toHaveText(['alias:"Hillside North"', 'alias:"Hillside South"']);
	await expect(announced).toHaveText('2 suggestions');
	// Searched without picking: pages from both.
	await box.fill(`alias:hill ${marker}`);
	await box.press('Enter');
	await expect(page).toHaveURL(new RegExp(`/\\?alias=hill&q=${marker}$`));
	await expect.poll(who).toEqual(['Hillside North', 'Hillside South']);

	// Station 1 picked from alias:sta's suggestions, with the keyboard; picking
	// a value searches (M21), and what's typed next stays in the box.
	await box.fill('alias:sta');
	await expect(options.first()).toHaveText('alias:"Station 1"');
	await expect(options.filter({ hasText: 'Hillside' })).toHaveCount(0);
	await box.press('ArrowDown');
	await expect(options.first()).toHaveAttribute('aria-selected', 'true');
	await box.press('Enter');
	await expect(box).toHaveValue(/^alias:"Station 1"\s*$/);
	await expect(options).toHaveCount(0);
	await page.keyboard.type(marker);
	await box.press('Enter');
	await expect.poll(who).toEqual(['Station 1', 'Station 1']);

	// Two aliases (either) and a priority (both).
	await box.fill(`alias:"Station 1" alias:"Station 2" priority:emergency ${marker}`);
	await box.press('Enter');
	await expect.poll(who).toEqual(['Station 1', 'Station 2']);
	await expect(list.locator('article .badge')).toHaveText(['Emergency priority', 'Emergency priority']);

	// 14:05 is text.
	await box.fill('14:05');
	await box.press('Enter');
	await expect(page).toHaveURL(/\/\?q=14%3A05$/);
	await expect(list.locator('article', { hasText: marker })).toHaveCount(1);
	for (const t of await list.locator('article p.text').allTextContents()) expect(t).toContain('14:05');

	// Text terms and or: (owner, 2026-11-02). Other tests' pages can match the
	// same words, so these carry a word of their own: those with it must be
	// exactly A61's, and every page listed must keep the rule.
	const tag = `TERMS${projectN(info)}${uniqueJob().slice(2)}`;
	for (const [capcode, text] of [
		['0100001', 'CHECK HOSES AT STATION, SMOKE SEEN'],
		['0100001', 'CHECK HOSES TODAY'],
		['0100001', 'HOSES CHECK, SMOKE SEEN'],
		['0100001', 'STRUCTURE FIRE, SMOKE SHOWING'],
		['0100002', 'GRASS FIRE'],
		['0100002', 'MEET AT THE SIDE DOOR OR: THE GATE']
	])
		await post(request, capcode, `QD${text} ${tag}`);
	const listed = async () => (await list.locator('article p.text').allTextContents()).map((t) => t.toUpperCase());
	const has = (t: string, ...terms: string[]) => terms.every((w) => t.includes(w.toUpperCase()));
	async function terms(search: string, want: string[], keeps: (text: string) => boolean) {
		await box.fill(search);
		await box.press('Enter');
		await expect
			.poll(async () => (await listed()).filter((t) => t.includes(tag)).map((t) => t.replace(` ${tag}`, '')).sort(), search)
			.toEqual([...want].sort());
		for (const t of await listed()) expect(keeps(t), `${search} listed: ${t}`).toBe(true);
	}
	await terms('"Check hoses" "Smoke"', ['CHECK HOSES AT STATION, SMOKE SEEN'], (t) => has(t, 'CHECK HOSES', 'SMOKE'));
	await terms('hoses smoke', ['CHECK HOSES AT STATION, SMOKE SEEN', 'HOSES CHECK, SMOKE SEEN'], (t) => has(t, 'HOSES', 'SMOKE'));
	await terms('"Smoke showing" or:"grass fire"', ['STRUCTURE FIRE, SMOKE SHOWING', 'GRASS FIRE'], (t) => has(t, 'SMOKE SHOWING') || has(t, 'GRASS FIRE'));
	await terms(
		'"Check hoses" "Smoke" or:"grass fire"',
		['CHECK HOSES AT STATION, SMOKE SEEN', 'GRASS FIRE'],
		(t) => has(t, 'CHECK HOSES', 'SMOKE') || has(t, 'GRASS FIRE')
	);
	await terms('alias:"Station 2" "Smoke showing" or:"grass fire"', ['GRASS FIRE'], (t) => has(t, 'SMOKE SHOWING') || has(t, 'GRASS FIRE'));
	expect(new Set(await list.locator('article .who').allTextContents())).toEqual(new Set(['Station 2']));
	await terms('"or:"', ['MEET AT THE SIDE DOOR OR: THE GATE'], (t) => has(t, 'OR:'));
	await expect(page).toHaveURL(/\/\?q=%22or%3A%22$/);
	// or: is a key: suggested as one, with no values of its own, and in the help.
	await box.fill('o');
	await expect(options.locator('code')).toHaveText(['or:']);
	await box.fill('or:');
	await expect(options).toHaveCount(0);
	await expect(page.getByRole('search', { name: 'Search messages' }).locator('.keys')).toContainText('or:"grass fire"');

	// A reload keeps the search.
	await box.fill('"or:"');
	await box.press('Enter');
	await page.reload();
	await expect(box).toHaveValue('"or:"');
	await expect.poll(async () => (await listed()).filter((t) => t.includes(tag)).length).toBe(1);

	// fl: no flag: for B, but an admin gets it; Escape closes the list. (Since
	// M21 a word with no key also suggests searching it as text, first.)
	await box.fill('fl');
	await expect(options.first()).toHaveText('Search text: fl');
	await expect(options.locator('code')).toHaveCount(0);
	const admin = await (await signedIn(browser, 'admin')).newPage();
	await admin.goto('/');
	await waitLive(admin);
	const adminBox = admin.getByRole('combobox', { name: 'Search' });
	await adminBox.fill('fl');
	const adminOptions = admin.getByRole('listbox', { name: 'Search suggestions' }).getByRole('option');
	await expect(adminOptions.locator('code')).toHaveText(['flag:']);
	await adminBox.press('ArrowDown');
	await adminBox.press('ArrowDown');
	await expect(adminOptions.nth(1)).toHaveAttribute('aria-selected', 'true');
	await adminBox.press('Enter');
	await expect(adminBox).toHaveValue('flag:');
	await expect(adminOptions).toHaveText(['flag:parse_failed', 'flag:address_unsplit', 'flag:location_mismatch']);
	await admin.screenshot({ path: info.outputPath('a61-suggestions-admin.png') });
	expect(await axeOf(admin), 'axe with the suggestions open').toEqual([]);
	await adminBox.press('Escape');
	await expect(adminOptions).toHaveCount(0);
	await page.context().close();
	await admin.context().close();
});

// A82. The search box (M21): every key on the first click, a key put in at
// the cursor, a plain word suggesting text first and then the filters
// containing it, the range calendar with a second day before the first, and
// Clear all, which Back undoes. Once with the mouse in light mode, once with
// the keyboard alone in dark mode, with axe at each step.
test('A82 search box', async ({ browser, request }, info) => {
	test.setTimeout(120_000);
	const marker = `BOXA${projectN(info)}${uniqueJob().slice(2)}`;
	await post(request, '0600001', `QD${marker} DRILL AT HILLSIDE NORTH`);
	await post(request, '0600002', `QD${marker} DRILL AT HILLSIDE SOUTH`);
	await post(request, '0100001', `QD${marker} DRILL AT STATION ONE`);
	const days = pickMonth();
	const keys = ['alias:', 'agency:', 'capcode:', 'group:', 'priority:', 'category:', 'job:', 'is:', 'or:'];
	const report: string[] = [];
	for (const [how, colorScheme] of [
		['mouse', 'light'],
		['keyboard', 'dark']
	] as const) {
		const page = await (await signedIn(browser, 'user', { colorScheme })).newPage();
		await page.goto('/');
		await waitLive(page);
		const form = page.getByRole('search', { name: 'Search messages' });
		const box = form.getByRole('combobox', { name: 'Search' });
		const options = form.getByRole('listbox', { name: 'Search suggestions' }).getByRole('option');
		const announced = form.locator('[aria-live]').last();
		const dates = form.getByRole('button', { name: /^Dates/ });
		const calendar = form.getByRole('dialog', { name: 'Choose dates' });
		const clear = form.getByRole('button', { name: 'Clear all', exact: true });
		const help = form.getByRole('button', { name: 'Search help', exact: true });
		const list = page.getByTestId('message-list');
		const axe = async (what: string) => expect(await axeOf(page), `${how}, ${colorScheme}: axe, ${what}`).toEqual([]);
		const shot = (what: string) => page.screenshot({ path: info.outputPath(`a82-${how}-${colorScheme}-${what}.png`) });

		// Help and Clear all are icons with names and tooltips; nothing is set,
		// so there's no Clear all yet.
		await expect(help).toBeVisible();
		await expect(clear).toBeHidden();
		if (how === 'mouse') {
			await help.hover();
			await expect(form.locator('.tooltip', { hasText: 'Search help' })).toHaveCSS('opacity', '1');
		}

		// Into the empty box: every key (no flag: for B); alias: picked goes in
		// with the cursor after it, and its values are suggested.
		if (how === 'mouse') await box.click();
		else await box.focus();
		await expect(options.locator('code')).toHaveText(keys);
		await expect(announced).toHaveText(`${keys.length} filter keys`);
		await shot('keys');
		await axe('the keys listed');
		if (how === 'mouse') await options.filter({ hasText: /^alias:/ }).click();
		else {
			await box.press('ArrowDown');
			await expect(options.first()).toHaveAttribute('aria-selected', 'true');
			await box.press('Enter');
		}
		await expect(box).toHaveValue('alias:');
		expect(await box.evaluate((el: HTMLInputElement) => [el.selectionStart, el.selectionEnd])).toEqual([6, 6]);
		await expect(options.filter({ hasText: 'alias:"Hillside North"' })).toHaveCount(1);

		// hill, with no key (after the marker, a text term): searching it as
		// text first, then both Hillside aliases.
		if (how === 'mouse') await box.fill(`${marker} hill`);
		else {
			for (let i = 0; i < 'alias:'.length; i++) await box.press('Backspace');
			await page.keyboard.type(`${marker} hill`);
		}
		await expect(options).toHaveText(['Search text: hill', 'alias:"Hillside North"', 'alias:"Hillside South"']);
		await expect(announced).toHaveText('3 suggestions');
		await shot('hill');
		await axe('hill suggesting filters');
		// The first alias picked: the word becomes that filter, which matches
		// exactly, and only its pages are listed.
		if (how === 'mouse') await options.nth(1).click();
		else {
			await box.press('ArrowDown');
			await box.press('ArrowDown');
			await expect(options.nth(1)).toHaveAttribute('aria-selected', 'true');
			await box.press('Enter');
		}
		await expect(box).toHaveValue(`${marker} alias:"Hillside North" `);
		await expect(page).toHaveURL(new RegExp(`/\\?alias=%22Hillside\\+North%22&q=${marker}$`));
		await expect.poll(async () => await list.locator('article .who').allTextContents()).toEqual(['Hillside North']);

		// The calendar: the 3rd, then the 1st, is the 1st to the 3rd, all three
		// days barred.
		if (how === 'mouse') await dates.click();
		else {
			await dates.focus();
			await page.keyboard.press('Enter');
		}
		await expect(calendar).toBeVisible();
		const cell = (n: number) => calendar.getByRole('gridcell', { name: new RegExp(` ${n} ${days.name}$`) });
		if (how === 'mouse') {
			if (days.back) await calendar.getByRole('button', { name: 'Previous month' }).click();
			await cell(3).click();
			// The bar is previewed as the pointer moves.
			await cell(1).hover();
			for (const n of [1, 2, 3]) await expect(cell(n)).toHaveAttribute('aria-selected', 'true');
			await cell(1).click();
		} else {
			// Today has the focus: arrows to the 3rd, Enter, two days back, Enter.
			const back = Math.round((days.today - Date.parse(`${days.third}T00:00:00Z`)) / 864e5);
			for (let i = 0; i < back; i++) await page.keyboard.press('ArrowLeft');
			await expect(cell(3)).toBeFocused();
			await page.keyboard.press('Enter');
			await page.keyboard.press('ArrowLeft');
			await page.keyboard.press('ArrowLeft');
			await expect(cell(1)).toBeFocused();
			await page.keyboard.press('Enter');
		}
		await expect(calendar.getByRole('heading')).toHaveText(days.name);
		for (const n of [1, 2, 3]) await expect(cell(n), `day ${n} is barred`).toHaveAttribute('aria-selected', 'true');
		await expect(cell(4)).toHaveAttribute('aria-selected', 'false');
		await expect(calendar.locator('button.day.in')).toHaveCount(3);
		await expect(form.getByRole('button', { name: `Dates: ${days.label}`, exact: true })).toBeVisible();
		await shot('calendar');
		await axe('the calendar open');
		if (how === 'mouse') await calendar.getByRole('button', { name: 'Done' }).click();
		else await page.keyboard.press('Escape');
		await expect(calendar).toBeHidden();
		if (how === 'keyboard') await expect(dates).toBeFocused();
		const searched = new RegExp(`/\\?alias=%22Hillside\\+North%22&q=${marker}&from=${days.first}&to=${days.third}$`);
		await expect(page).toHaveURL(searched);

		// Help, opened and closed.
		if (how === 'mouse') await help.click();
		else {
			await help.focus();
			await page.keyboard.press('Enter');
		}
		await expect(form.locator('.keys')).toBeVisible();
		await expect(form.locator('.keys')).toContainText('or:"grass fire"');
		await shot('help');
		await axe('the help open');
		if (how === 'mouse') await help.click();
		else {
			await page.keyboard.press('Escape');
			await expect(help).toBeFocused();
		}
		await expect(form.locator('.keys')).toBeHidden();

		// Clear all: the box and the dates are empty, and the plain list is
		// shown; Back brings the search back.
		await expect(clear).toBeVisible();
		if (how === 'mouse') await clear.click();
		else {
			await dates.focus();
			await page.keyboard.press('Tab');
			await expect(clear).toBeFocused();
			await page.keyboard.press('Enter');
		}
		await expect(page).toHaveURL(/\/$/);
		await expect(box).toHaveValue('');
		await expect(box).toBeFocused();
		await expect(form.getByRole('button', { name: 'Dates', exact: true })).toBeVisible();
		await expect(clear).toBeHidden();
		await expect(list.locator('article').first()).toBeVisible();
		await page.goBack();
		await expect(page).toHaveURL(searched);
		await expect(box).toHaveValue(`alias:"Hillside North" ${marker}`);
		await expect(form.getByRole('button', { name: `Dates: ${days.label}`, exact: true })).toBeVisible();
		await expect(clear).toBeVisible();
		// With the mouse, the pointer is still where Clear all was clicked, so
		// its tooltip fades in as the button comes back. The pointer is put
		// there for certain and the tooltip waited for until it's fully shown,
		// so axe checks it as it's seen, not part-way through the fade (A82
		// once failed in Firefox on .clear > .tooltip's contrast, right here).
		if (how === 'mouse') {
			await clear.hover();
			await expect(form.locator('.clear > .tooltip')).toHaveCSS('opacity', '1');
		}
		await shot('back');
		await axe('after Back');

		// Escape closes the list.
		await box.press('ArrowDown');
		await expect(options.first()).toBeVisible();
		await box.press('Escape');
		await expect(options).toHaveCount(0);
		report.push(`${how} (${colorScheme}): keys on entry, alias: at the cursor, hill -> text then both Hillside aliases, the 3rd then the 1st -> ${days.label}, Clear all and Back`);
		await page.context().close();
	}
	console.log(`A82 (${info.project.name}): ${report.join('; ')}; axe clean at every step`);
});

// A68. The side menu and the breadcrumbs, at 1,920 px.
test('A68 side menu and breadcrumbs', async ({ browser, request }, info) => {
	test.setTimeout(180_000);
	const desk = { viewport: { width: 1920, height: 1080 } };
	const origins = new Set<string>();
	const watch = (p: Page) => p.on('request', (r) => !r.url().startsWith('data:') && origins.add(new globalThis.URL(r.url()).origin));
	const expected = {
		Tools: ['Print sheet', 'Unknown capcodes', 'Large events'],
		Reports: ['Capcode report'],
		Account: ['Profile', 'Integrations', 'My requests', 'Invite someone'],
		Administration: ['Users', 'Aliases', 'Templates', 'Alias requests', 'Receiving clients', 'Retention', 'Backups', 'Integration addresses', 'Large event settings']
	};
	for (const role of ['user', 'admin'] as const) {
		const page = await (await signedIn(browser, role, desk)).newPage();
		watch(page);
		await page.goto('/');
		await waitLive(page);
		const nav = page.getByRole('navigation', { name: 'Main' });
		const groups = Object.keys(expected).filter((g) => role === 'admin' || g !== 'Administration');
		for (const g of Object.keys(expected)) await expect(nav.getByRole('link', { name: g, exact: true }), `${role}: ${g}`).toHaveCount(groups.includes(g) ? 1 : 0);
		// Every group and page has its icon.
		const bare = await nav.locator('a.item').evaluateAll((as) => as.filter((a) => !a.querySelector('svg.ui-icon')).map((a) => a.textContent));
		expect(bare, `${role}: links without an icon`).toEqual([]);
		// M14: Albums, a group whose pages are the person's albums (R77), one click to its start page.
		await expect(nav.getByRole('link', { name: 'Albums', exact: true })).toHaveAttribute('href', '/albums');
		await expect(nav.getByRole('link', { name: 'Albums', exact: true }).locator('svg.ui-icon')).toHaveCount(1);
		await page.screenshot({ path: info.outputPath(`a68-menu-${role}.png`) });
		// Each page in at most two clicks: its group's toggle, if closed, then the page.
		for (const g of groups) {
			for (const label of expected[g as keyof typeof expected]) {
				await page.goto('/');
				// Administration is drawn once the page knows who's signed in.
				await expect(nav.getByRole('link', { name: new RegExp(`^${g}`) })).toBeVisible();
				let clicks = 0;
				const link = nav.getByRole('link', { name: label, exact: true }).or(nav.getByRole('link', { name: new RegExp(`^${label},`) }));
				if (!(await link.isVisible())) {
					await nav.getByRole('button', { name: `${g}: show its pages` }).click();
					clicks++;
				}
				await link.click();
				clicks++;
				await expect(page.locator('main h1').first(), `${role}: ${g} > ${label}`).toBeVisible();
				console.log(`A68 (${role}): ${g} > ${label} in ${clicks} click(s), ${Date.now() % 100000}`);
				expect(clicks, `${g} > ${label}`).toBeLessThanOrEqual(2);
				expect(await crumbsOf(page), `${g} > ${label}: its breadcrumbs`).toEqual(
					[{ label: g, href: g === 'Administration' ? '/admin' : `/${g.toLowerCase()}` }, { label, href: null }]
				);
			}
		}
		await page.context().close();
	}

	// Shrunk: the group icons only, each named, with a tooltip; hovering or
	// focusing a group opens its pages' panel.
	const page = await (await signedIn(browser, 'admin', desk)).newPage();
	watch(page);
	await page.goto('/');
	const nav = page.getByRole('navigation', { name: 'Main' });
	await nav.getByRole('button', { name: 'Shrink menu' }).click();
	await expect(nav.locator('.label').first()).toBeHidden();
	for (const g of ['Messages', 'Albums', 'Tools', 'Reports', 'Account', 'Administration']) await expect(nav.getByRole('link', { name: new RegExp(`^${g}`) }).first()).toBeVisible();
	// A tooltip says what's there, not the name again (owner, 2026-11-02).
	await nav.getByRole('link', { name: 'Messages', exact: true }).hover();
	await expect(nav.locator('.tip', { hasText: 'Every page, newest first, as it arrives' })).toBeVisible();
	await nav.getByRole('link', { name: 'Tools' }).hover();
	await expect(nav.locator('#menu-tools .flyhead')).toHaveText('Printing for training, capcodes with no alias, and large events');
	await expect(nav.getByRole('link', { name: /^Print sheet/ })).toBeVisible();
	await page.screenshot({ path: info.outputPath('a68-shrunk-hover.png') });
	await nav.getByRole('button', { name: 'Widen the menu' }).hover();
	await expect(nav.locator('.tip', { hasText: "Show the pages' names again" })).toBeVisible();
	await page.mouse.move(1500, 600);
	await expect(nav.getByRole('link', { name: /^Print sheet/ })).toBeHidden();
	await nav.getByRole('link', { name: 'Reports' }).focus();
	await expect(nav.getByRole('link', { name: 'Capcode report' })).toBeVisible();
	await page.keyboard.press('Tab');
	await expect(nav.getByRole('link', { name: 'Capcode report' })).toBeFocused();
	await page.screenshot({ path: info.outputPath('a68-shrunk-focus.png') });
	expect(await axeOf(page), 'axe, shrunk, a panel open').toEqual([]);
	await nav.getByRole('button', { name: 'Widen the menu' }).click();
	await page.context().close();

	// The same alias view from the menu, a search result and a pasted address:
	// the same breadcrumbs, each level above a link.
	const job = uniqueJob();
	await post(request, '0100001', `@@${job} HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN1`);
	const user = await (await signedIn(browser, 'user', desk)).newPage();
	watch(user);
	const seen: Awaited<ReturnType<typeof crumbsOf>>[] = [];
	const box = user.getByRole('combobox', { name: 'Search' });
	// From the menu: Messages, then the alias on a row.
	await user.goto('/account/profile');
	await user.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Messages', exact: true }).click();
	await user.getByTestId('message-list').getByRole('link', { name: 'Station 1', exact: true }).first().click();
	await expect(user).toHaveURL(/\/aliases\/\d+$/);
	await expect(user.getByRole('heading', { level: 1, name: 'Station 1' })).toBeVisible();
	seen.push(await crumbsOf(user));
	const address = user.url();
	// From a search result.
	await user.goto('/requests');
	await box.fill(`job:${job}`);
	await box.press('Enter');
	await expect(user).toHaveURL(`/?job=${job}`);
	await expect(user.getByTestId('message-list').locator('article')).toHaveCount(1);
	await user.getByTestId('message-list').getByRole('link', { name: 'Station 1', exact: true }).click();
	await expect(user.getByRole('heading', { level: 1, name: 'Station 1' })).toBeVisible();
	seen.push(await crumbsOf(user));
	// From a pasted address.
	await user.goto(address);
	await expect(user.getByRole('heading', { level: 1, name: 'Station 1' })).toBeVisible();
	seen.push(await crumbsOf(user));
	console.log(`A68: the alias view's breadcrumbs from the menu, a search result and a pasted address: ${JSON.stringify(seen)}`);
	expect(seen[0]).toEqual([{ label: 'Messages', href: '/' }, { label: 'Station 1', href: null }]);
	expect(seen[1]).toEqual(seen[0]);
	expect(seen[2]).toEqual(seen[0]);
	expect(await axeOf(user), 'axe on the alias view').toEqual([]);
	await user.context().close();
	expect([...origins], 'origins requested, icons included').toEqual([URL]);
});

// A69. Using the width: a message opens beside the list, which keeps its
// search, dates and place; coming back from an alias by the breadcrumbs
// restores them (R81a).
test('A69 using the width', async ({ browser, request }, info) => {
	test.setTimeout(120_000);
	const marker = `WIDTH${projectN(info)}${uniqueJob().slice(2)}`;
	for (let n = 1; n <= 40; n++) await post(request, `010000${(n % 4) + 1}`, `QD${marker} ROW ${String(n).padStart(2, '0')} AT THE STATION`);
	const search = `q=${marker}&from=2020-01-01&to=2099-12-31`;
	for (const width of [1920, 1280, 375]) {
		const page = await (await signedIn(browser, 'user', { viewport: { width, height: width === 375 ? 740 : 1000 } })).newPage();
		await page.goto(`/?${search}`);
		await waitLive(page);
		const list = page.getByTestId('message-list');
		await expect(list.locator('article')).toHaveCount(40);
		await page.evaluate(() => window.scrollTo(0, 900));
		const row = list.locator('article').nth(12);
		// Mid-screen, clear of the top bar, so the click needs no more scrolling.
		await row.evaluate((el) => el.scrollIntoView({ block: 'center' }));
		const y = await page.evaluate(() => window.scrollY);
		expect(y, 'scrolled down the list').toBeGreaterThan(0);
		// Its place: where the row opened sits on the screen. (When the pane
		// opens beside it the list narrows and its rows may wrap, so the
		// browser keeps that row in place rather than the scroll offset.)
		const opened = list.locator(`article[data-message-id="${await row.getAttribute('data-message-id')}"]`);
		const top = (await opened.boundingBox())!.y;
		await row.locator('a.open').click();
		await expect(page).toHaveURL(/\/messages\/\d+$/);
		await expect(page.getByRole('heading', { name: 'Received by' })).toBeVisible();
		await page.screenshot({ path: info.outputPath(`a69-${width}-message.png`) });
		if (width >= 1280) {
			// Beside the list, which is where it was, with its search.
			await expect(opened).toBeInViewport();
			const moved = (await opened.boundingBox())!.y - top;
			console.log(`A69 (${width} px): the opened row moved ${moved} px on the screen when the pane opened (scrollY ${y} -> ${await page.evaluate(() => window.scrollY)})`);
			expect(Math.abs(moved), `${width}: the list kept its place`).toBeLessThan(40);
			await expect(page.getByRole('combobox', { name: 'Search' })).toHaveValue(marker);
			const edges = await page.evaluate(() => {
				const side = document.querySelector('nav.side')!.getBoundingClientRect();
				const l = document.querySelector('[data-testid="message-list"]')!.getBoundingClientRect();
				const pane = document.querySelector('section.pane')!.getBoundingClientRect();
				return { menu: side.width, left: l.left - side.right, right: document.documentElement.clientWidth - pane.right };
			});
			console.log(`A69 (${width} px): menu ${edges.menu} px; empty margin left of the list ${edges.left} px, right of the pane ${edges.right} px`);
			expect(edges.left).toBeLessThanOrEqual(edges.menu);
			expect(edges.right).toBeLessThanOrEqual(edges.menu);
			expect(Math.max(edges.left, edges.right)).toBeLessThanOrEqual(1);
		} else {
			await page.goBack();
			await expect(opened).toBeVisible();
			console.log(`A69 (${width} px): back from the message: the opened row ${Math.round((await opened.boundingBox())!.y - top)} px from where it was; scrollY ${y} before, ${await page.evaluate(() => window.scrollY)} now, top bar ${await page.evaluate(() => document.querySelector('header.top')!.getBoundingClientRect().height)}`);
		}
		// Its alias, then back to the list by the breadcrumbs.
		await list.locator('article').nth(12).getByRole('link', { name: /^Station \d$/ }).click();
		await expect(page).toHaveURL(/\/aliases\/\d+$/);
		await expect(page.getByRole('heading', { level: 1, name: /^Station \d$/ })).toBeVisible();
		await page.getByRole('navigation', { name: 'Breadcrumb' }).getByRole('link', { name: 'Messages' }).click();
		await expect(page).toHaveURL(`/?${search}`);
		await expect(list.locator('article')).toHaveCount(40);
		await expect(opened).toBeInViewport();
		await expect.poll(async () => Math.abs((await opened.boundingBox())!.y - top), `${width}: the list's place`).toBeLessThan(40);
		await expect(page.getByRole('combobox', { name: 'Search' })).toHaveValue(marker);
		await expect(page.getByRole('button', { name: 'Dates: 1 Jan 2020 – 31 Dec 2099', exact: true })).toBeVisible();
		await page.screenshot({ path: info.outputPath(`a69-${width}-back.png`) });
		console.log(`A69 (${width} px): back at the list by the breadcrumbs: the opened row ${Math.round((await opened.boundingBox())!.y - top)} px from where it was (scrollY ${y} before, ${await page.evaluate(() => window.scrollY)} now), search and dates kept`);
		await page.context().close();
	}
});

// A70. Phones: the bottom bar, More without the desktop-only pages, the
// integration switch, and the print page from its address.
test('A70 phones', async ({ browser, request }, info) => {
	await request.put(`${URL}/api/v1/admin/integration-hosts`, { headers: asRole('admin'), data: { entries: ['127.0.0.0/8'] } });
	const name = `Phone switch ${Date.now()}`;
	const made = await request.post(`${URL}/api/v1/integrations`, {
		headers: asRole('user'),
		data: { name, kind: 'webhook', capcodes: ['0500009'], secrets: { url: 'http://127.0.0.1:9/hook' } }
	});
	expect(made.status()).toBe(201);
	const id = (await made.json()).id as number;
	try {
		for (const role of ['user', 'admin'] as const) {
			const page = await (await signedIn(browser, role, { viewport: { width: 375, height: 740 }, hasTouch: true })).newPage();
			await page.goto('/');
			await waitLive(page);
			const bar = page.getByRole('navigation', { name: 'Main' });
			await expect(bar.getByRole('link', { name: 'Messages' })).toBeVisible();
			await expect(bar.getByRole('link', { name: 'Map' })).toBeVisible();
			await expect(bar.getByRole('link', { name: 'Albums' })).toBeVisible(); // M14 (R81)
			await bar.getByRole('button', { name: 'More' }).click();
			const more = page.getByRole('dialog', { name: 'More' });
			await expect(more).toBeVisible();
			for (const there of ['Unknown capcodes', 'Large events', 'Capcode report', 'Profile', 'Integrations', 'My requests', 'Invite someone']) await expect(more.getByRole('link', { name: there, exact: true })).toBeVisible();
			for (const gone of ['Print sheet', 'Administration', 'Users', 'Aliases', 'Templates', 'Alias requests', 'Receiving clients', 'Retention', 'Backups', 'Integration addresses', 'Large event settings'])
				await expect(more.getByRole('link', { name: gone })).toHaveCount(0);
			await expect(more.getByText(/set up|new integration/i)).toHaveCount(0);
			await page.screenshot({ path: info.outputPath(`a70-more-${role}.png`) });
			expect(await axeOf(page), `${role}: axe with More open`).toEqual([]);
			if (role === 'user') {
				await more.getByRole('link', { name: 'Integrations', exact: true }).click();
				await expect(page.getByRole('heading', { level: 1, name: 'Integrations' })).toBeVisible();
				await expect(page.getByRole('button', { name: 'New integration' })).toHaveCount(0);
				const sw = page.getByRole('switch', { name: `${name}: sending` });
				await expect(sw).toHaveAttribute('aria-checked', 'true');
				await sw.tap();
				await expect(sw).toHaveAttribute('aria-checked', 'false');
				await expect(sw).toContainText('Paused');
				const saved = (await (await request.get(`${URL}/api/v1/integrations`, { headers: asRole('user') })).json()).integrations.find((i: { id: number }) => i.id === id);
				expect(saved.status).toBe('paused');
				await page.screenshot({ path: info.outputPath('a70-integrations.png') });
				await page.goto('/sheet');
				await expect(page.getByRole('heading', { level: 1, name: 'Print sheet' })).toBeVisible();
				await waitLive(page);
				console.log(`A70: paused "${name}" with its switch on a 375 px phone (API: ${saved.status}); /sheet opened from its address`);
			}
			await page.context().close();
		}
	} finally {
		await request.delete(`${URL}/api/v1/integrations/${id}`, { headers: asRole('user') });
	}
});

// WCAG 2.1 AA: 4.5:1 for text (1.4.3), 3:1 for focus rings, control borders
// and the marks that show priority (1.4.11), each token as the browser
// resolves it, light and dark. Priorities carry their names in words, so
// none relies on colour alone (1.4.1).
const colourTokens = [
	...['bg', 'surface', 'surface-2', 'fg', 'muted', 'border', 'control-border', 'accent', 'accent-fg', 'accent-hover', 'accent-pressed', 'accent-soft', 'accent-soft-fg'],
	...['hover', 'focus', 'warn-bg', 'warn-fg', 'ok-fg', 'danger', 'danger-fg', 'danger-hover', 'danger-text', 'heartbeat-fg', 'highlight'],
	...['emergency', 'nonemergency', 'admin'].flatMap((p) => ['bg', 'edge', 'dot', 'row', 'row-hover', 'badge-bg', 'badge-fg'].map((t) => `${p}-${t}`))
];
test('M13: WCAG AA contrast of Harbour, light and dark', async ({ browser }) => {
	const report: string[] = [];
	const failures: string[] = [];
	for (const scheme of ['light', 'dark'] as const) {
		const page = await (await signedIn(browser, 'user', { colorScheme: scheme })).newPage();
		await page.goto('/');
		await waitLive(page);
		const t: Record<string, string> = await page.evaluate((names) => {
			const root = getComputedStyle(document.documentElement);
			const probe = document.createElement('span');
			document.body.append(probe);
			const out: Record<string, string> = {};
			for (const n of names) {
				probe.style.color = `var(--${n})`;
				out[n] = root.getPropertyValue(`--${n}`).trim() === 'transparent' ? 'transparent' : getComputedStyle(probe).color;
			}
			probe.remove();
			return out;
		}, colourTokens);
		// A transparent row is drawn on the page or on a card.
		const on = (names: string[]) => names.flatMap((n) => (t[n] === 'transparent' ? ['bg', 'surface'] : [n]));
		const rgb = (n: string) => t[n].match(/[\d.]+/g)!.slice(0, 3).map(Number);
		const lum = (c: number[]) =>
			c.map((v) => v / 255).map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)).reduce((s, v, i) => s + v * [0.2126, 0.7152, 0.0722][i], 0);
		const ratio = (a: string, b: string) => {
			const [x, y] = [lum(rgb(a)), lum(rgb(b))].sort((p, q) => q - p);
			return (x + 0.05) / (y + 0.05);
		};
		const pairs: { what: string; fg: string; on: string[]; min: number }[] = [
			{ what: 'Text', fg: 'fg', on: ['bg', 'surface', 'surface-2', 'hover', 'highlight'], min: 4.5 },
			{ what: 'Secondary text', fg: 'muted', on: ['bg', 'surface', 'surface-2', 'hover'], min: 4.5 },
			{ what: 'Links', fg: 'accent', on: ['bg', 'surface', 'surface-2'], min: 4.5 },
			{ what: 'Button text', fg: 'accent-fg', on: ['accent', 'accent-hover', 'accent-pressed'], min: 4.5 },
			{ what: 'Current page, pressed', fg: 'accent-soft-fg', on: ['accent-soft'], min: 4.5 },
			{ what: 'Danger button text', fg: 'danger-fg', on: ['danger', 'danger-hover'], min: 4.5 },
			{ what: 'Error text', fg: 'danger-text', on: ['bg', 'surface', 'surface-2'], min: 4.5 },
			{ what: 'Live, saved', fg: 'ok-fg', on: ['bg', 'surface'], min: 3 },
			{ what: 'Tooltip', fg: 'bg', on: ['fg'], min: 4.5 },
			{ what: 'Warnings and flags', fg: 'warn-fg', on: ['warn-bg'], min: 4.5 },
			{ what: 'Heartbeat label', fg: 'heartbeat-fg', on: ['bg', 'surface'], min: 4.5 },
			{ what: 'Focus ring', fg: 'focus', on: ['bg', 'surface', 'surface-2'], min: 3 },
			{ what: 'Control borders', fg: 'control-border', on: ['bg', 'surface', 'surface-2'], min: 3 },
			{ what: 'Switch on', fg: 'accent', on: ['surface-2'], min: 3 }
		];
		for (const p of ['emergency', 'nonemergency', 'admin']) {
			const rows = on([`${p}-row`, `${p}-row-hover`]);
			pairs.push(
				{ what: `${p}: label`, fg: `${p}-badge-fg`, on: on([`${p}-badge-bg`]), min: 4.5 },
				{ what: `${p}: dot`, fg: `${p}-dot`, on: on([`${p}-badge-bg`]), min: 3 },
				{ what: `${p}: map marker`, fg: `${p}-bg`, on: ['bg', 'surface'], min: 3 },
				{ what: `${p}: text on its row`, fg: 'fg', on: rows, min: 4.5 },
				{ what: `${p}: secondary text on its row`, fg: 'muted', on: rows, min: 4.5 },
				{ what: `${p}: links on its row`, fg: 'accent', on: rows, min: 4.5 }
			);
			if (t[`${p}-edge`] !== 'transparent') pairs.push({ what: `${p}: row edge`, fg: `${p}-edge`, on: rows, min: 3 });
		}
		report.push(`Harbour, ${scheme}`);
		for (const p of pairs) {
			const each = p.on.map((b) => ({ b, r: ratio(p.fg, b) }));
			const worst = each.reduce((w, e) => (e.r < w.r ? e : w));
			report.push(`  ${p.what}: ${worst.r.toFixed(2)}:1 at worst (--${p.fg} on --${worst.b}; needs ${p.min}:1) [${each.map((e) => `${e.b} ${e.r.toFixed(2)}`).join(', ')}]`);
			if (worst.r < p.min) failures.push(`${scheme}: ${p.what}: --${p.fg} on --${worst.b} is ${worst.r.toFixed(2)}:1, needs ${p.min}:1`);
		}
		await page.context().close();
	}
	console.log(report.join('\n'));
	expect(failures).toEqual([]);
});

// The evidence for ADR 0015: with PA_M13_SHOTS set to a folder, screenshots
// at 1,920, 1,280 and 375 px, light and dark: the menu open and shrunk, a
// message open in the pane, the phone with its bottom bar and More, and an
// admin's view with Administration. Chromium only.
test('M13 screenshots', async ({ browser, request }, info) => {
	const dir = process.env.PA_M13_SHOTS;
	test.skip(!dir || info.project.name !== 'chromium', 'only with PA_M13_SHOTS set, in Chromium');
	test.setTimeout(300_000);
	await post(request, '0100001', `@@${uniqueJob()} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1`);
	const shot = async (page: Page, name: string) => page.screenshot({ path: join(dir!, `${name}.png`) });
	for (const scheme of ['light', 'dark'] as const) {
		for (const width of [1920, 1280, 375]) {
			const size = { viewport: { width, height: width === 375 ? 812 : 1080 }, colorScheme: scheme, hasTouch: width === 375 };
			for (const role of ['user', 'admin'] as const) {
				const page = await (await signedIn(browser, role, size)).newPage();
				const base = `${scheme}-${width}-${role}`;
				await page.goto('/');
				await waitLive(page);
				await shot(page, `${base}-list`);
				await page.getByTestId('message-list').locator('article a.open').first().click();
				await expect(page.getByRole('heading', { name: 'Received by' })).toBeVisible();
				await expect(page.getByTestId('map')).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
				await page.waitForTimeout(800);
				await shot(page, `${base}-message`);
				if (width === 375) {
					await page.goto('/');
					await waitLive(page);
					await page.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: 'More' }).click();
					await shot(page, `${base}-more`);
				} else {
					const nav = page.getByRole('navigation', { name: 'Main' });
					if (role === 'admin') {
						await page.goto('/admin');
						await expect(page.getByRole('heading', { level: 1, name: 'Administration' })).toBeVisible();
						await shot(page, `${base}-administration`);
						await page.goto('/admin/templates');
						await page.getByTestId('template-table').getByRole('link', { name: 'Example Fire' }).click();
						await expect(page.getByRole('heading', { level: 1 })).toHaveText('Template “Example Fire”');
						await shot(page, `${base}-template`);
					}
					await page.goto('/');
					await waitLive(page);
					await nav.getByRole('button', { name: 'Shrink menu' }).click();
					await nav.getByRole('link', { name: 'Account' }).hover();
					await shot(page, `${base}-shrunk`);
					await nav.getByRole('button', { name: 'Widen the menu' }).click();
				}
				await page.context().close();
			}
		}
	}
});

// M14 (ADR 0026). Each browser project n (projectN) uses its own alias, "Scrubber n" on
// 07100n1, and its own album names, so the projects' runs don't mix.
const TZ = 'Australia/Melbourne'; // scripts/e2e.sh's TZ

/** The date ago days before today, in the deployment's time zone, as the API's marks start it. */
function melbourneDay(ago: number): string {
	const d = new Date(Date.now() - ago * 864e5);
	const p = Object.fromEntries(
		new Intl.DateTimeFormat('en-CA', { timeZone: TZ, year: 'numeric', month: '2-digit', day: '2-digit' }).formatToParts(d).map((x) => [x.type, x.value])
	);
	return `${p.year}-${p.month}-${p.day}`;
}
const dayName = (ago: number, opts: Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat('en-AU', { timeZone: TZ, ...opts }).format(new Date(Date.now() - ago * 864e5));

/** The text of the list's first row in view, below the top bar: where a jump left it. */
async function firstInView(page: Page) {
	return page.evaluate(() => {
		const below = document.querySelector('header.top')?.getBoundingClientRect().bottom ?? 0;
		const row = [...document.querySelectorAll('[data-testid="message-list"] article')].find((r) => r.getBoundingClientRect().bottom > below + 1);
		return row?.querySelector('p.text')?.textContent?.trim() ?? '';
	});
}

/** Brings the hidden scrubber out: the pointer at the list's right edge (owner, 2026-11-13). */
async function reveal(page: Page) {
	const box = (await page.getByTestId('message-list').boundingBox())!;
	await page.mouse.move(box.x + box.width - 2, 450, { steps: 2 });
	await expect(page.getByTestId('scrubber')).toHaveAttribute('data-shown', 'true');
}

/** The scrubber's marks as drawn: kind, start, count and dots. */
async function marksOf(page: Page) {
	return page
		.getByTestId('scrubber')
		.locator('.mark')
		.evaluateAll((ms) =>
			ms.map((m) => ({
				kind: (m as HTMLElement).dataset.kind ?? '',
				start: (m as HTMLElement).dataset.start ?? '',
				count: Number((m as HTMLElement).dataset.count),
				dots: m.querySelectorAll('.dot').length
			}))
		);
}

// A71. The scrubber: 40 days of made-up pages, one day ten times as busy.
test('A71 scrubber', async ({ browser, request }, info) => {
	test.setTimeout(180_000);
	const n = projectN(test.info());
	const capcode = `07100${n}1`;
	const alias = `Scrubber ${n}`;
	// 40 days back from 2 days ago: 3 pages a day at 12:00, 12:10 and 12:20;
	// 12 days ago, 30. Yesterday: three in its 14:00 hour, at 14:05, 14:20 and 14:50.
	sql(`
INSERT INTO aliases (pattern, name, template_id) SELECT '${capcode}', '${alias}', id FROM templates WHERE name = 'Example Fire' ON CONFLICT DO NOTHING;
DELETE FROM messages WHERE capcode = '${capcode}';
WITH midnight AS (SELECT date_trunc('day', now() AT TIME ZONE '${TZ}') AS d)
INSERT INTO messages (capcode, text, priority, sent_at, dedup_key, alias_id)
SELECT '${capcode}', 'QDSCRUBBER ${n} DAY ' || g.day || ' PAGE ' || p.i, 'Admin',
       ((SELECT d FROM midnight) - g.day * interval '1 day' + interval '12 hours' + p.i * interval '10 minutes') AT TIME ZONE '${TZ}',
       sha256(convert_to('scrubber ${n} ' || g.day || ' ' || p.i, 'UTF8')), (SELECT id FROM aliases WHERE pattern = '${capcode}')
FROM generate_series(2, 41) AS g (day)
CROSS JOIN LATERAL generate_series(0, CASE WHEN g.day = 12 THEN 29 ELSE 2 END) AS p (i)
UNION ALL
SELECT '${capcode}', 'QDSCRUBBER ${n} YESTERDAY PAGE ' || p.i, 'Admin',
       ((SELECT d FROM midnight) - interval '1 day' + interval '14 hours' + (ARRAY[5, 20, 50])[p.i + 1] * interval '1 minute') AT TIME ZONE '${TZ}',
       sha256(convert_to('scrubber ${n} yesterday ' || p.i, 'UTF8')), (SELECT id FROM aliases WHERE pattern = '${capcode}')
FROM generate_series(0, 2) AS p (i);`);
	const page = await (await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } })).newPage();
	const counted = page.waitForResponse((r) => r.url().includes('/api/v1/messages/counts'));
	const asked = Date.now();
	await page.goto(`/?alias=${encodeURIComponent(`"${alias}"`)}`);
	await waitLive(page);
	const res = await counted;
	const scrubber = page.getByTestId('scrubber');
	await expect(scrubber.locator('.mark').first()).toBeVisible();
	console.log(`A71: the scrubber's counts answered ${Date.now() - asked} ms after the page was asked for (${(await res.request().timing()).responseEnd.toFixed(0)} ms for the request)`);
	let marks = await marksOf(page);
	// The last 30 days: 28 days and today's and yesterday's hours; nothing older.
	expect(marks.filter((m) => m.kind === 'day')).toHaveLength(28);
	expect(marks.filter((m) => m.kind !== 'day' && m.kind !== 'hour')).toHaveLength(0);
	expect(marks.at(-1)!.start.slice(0, 10)).toBe(melbourneDay(29));
	expect(marks.reduce((s, m) => s + m.count, 0), 'counted: 27 days of 3, the busy day 30 and yesterday 3').toBe(27 * 3 + 30 + 3);
	// The busy day shows more dots than its neighbours.
	const dayMark = (ago: number) => marks.find((m) => m.kind === 'day' && m.start.startsWith(melbourneDay(ago)))!;
	expect(dayMark(12).dots).toBeGreaterThan(dayMark(11).dots);
	expect(dayMark(12).dots).toBeGreaterThan(dayMark(13).dots);
	await page.screenshot({ path: info.outputPath('a71-scrubber.png') });
	expect(await page.evaluate(() => getComputedStyle(document.documentElement).scrollbarWidth), 'with a mouse the scroll bar stays').not.toBe('none');
	expect(await axeOf(page), 'axe with the scrubber').toEqual([]);

	// Dragging to 20 days ago: while dragging the mark is named; the list
	// then shows that day's messages.
	const target = scrubber.locator(`.mark[data-kind="day"][data-start^="${melbourneDay(20)}"]`);
	await reveal(page);
	const from = (await scrubber.locator('.mark').first().boundingBox())!;
	const to = (await target.boundingBox())!;
	await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2);
	await page.mouse.down();
	await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, { steps: 8 });
	await expect(scrubber.locator('.bubble')).toContainText(dayName(20, { weekday: 'long', day: 'numeric', month: 'long' }));
	// Scrubbing: while the button is held, the list is already there.
	await expect.poll(() => firstInView(page)).toBe(`SCRUBBER ${n} DAY 20 PAGE 2`);
	await page.mouse.up();
	const list = page.getByTestId('message-list');
	await expect.poll(() => firstInView(page)).toBe(`SCRUBBER ${n} DAY 20 PAGE 2`);
	await expect(target).toHaveClass(/current/);
	await expect(page.getByRole('button', { name: 'Load newer messages' })).toBeVisible();

	// Yesterday's 14:00 hour, then zoom: twelve 5-minute blocks, and picking
	// the 14:20 one moves the list there.
	await reveal(page);
	await scrubber.locator('.mark[data-kind="hour"]').filter({ has: page.locator('.dot') }).first().click();
	await expect.poll(() => firstInView(page)).toBe(`SCRUBBER ${n} YESTERDAY PAGE 2`);
	await page.getByRole('button', { name: /^Zoom into Yesterday 14:00/ }).click();
	await expect(scrubber.locator('.mark[data-kind="block"]')).toHaveCount(12);
	marks = await marksOf(page);
	expect(marks.map((m) => m.count)).toEqual([0, 1, 0, 0, 0, 0, 0, 1, 0, 0, 1, 0]);
	await scrubber.locator('.mark[data-kind="block"]').nth(7).click(); // 14:20 to 14:25
	await reveal(page);
	await expect.poll(() => firstInView(page)).toBe(`SCRUBBER ${n} YESTERDAY PAGE 1`);
	await expect(page.getByRole('button', { name: 'The hour before' })).toBeEnabled();
	await page.screenshot({ path: info.outputPath('a71-zoom.png') });
	expect(await axeOf(page), 'axe zoomed in').toEqual([]);
	await page.getByRole('button', { name: 'Back to the days' }).click();
	await expect(scrubber.locator('.mark[data-kind="block"]')).toHaveCount(0);

	// The keyboard: Home and End go to the ends, Page Up a week newer.
	const slider = scrubber.getByRole('slider', { name: 'Jump to a date' });
	// Alt+J brings it out with the keyboard on it, from anywhere on the page (owner, 2026-11-13).
	await page.mouse.move(400, 450);
	await page.getByRole('link', { name: 'Messages', exact: true }).first().focus();
	await page.keyboard.press('Alt+KeyJ');
	await expect(slider).toBeFocused();
	await expect(scrubber).toHaveAttribute('data-shown', 'true');
	await page.keyboard.press('Home');
	await expect(slider).toHaveAttribute('aria-valuetext', /^Today /);
	await page.keyboard.press('End');
	await expect(slider).toHaveAttribute('aria-valuetext', new RegExp(dayName(29, { day: 'numeric', month: 'long' })));
	await expect.poll(() => firstInView(page)).toBe(`SCRUBBER ${n} DAY 29 PAGE 2`);
	await page.keyboard.press('PageUp');
	await expect(slider).toHaveAttribute('aria-valuetext', new RegExp(dayName(22, { day: 'numeric', month: 'long' })));
	await expect.poll(() => firstInView(page)).toBe(`SCRUBBER ${n} DAY 22 PAGE 2`);
	// Escape puts the keyboard back on the list, on the row at the top of the view.
	await page.keyboard.press('Escape');
	await expect(page.getByTestId('message-list').locator('a.open:focus')).toContainText(`SCRUBBER ${n} DAY 22 PAGE 2`);
	// The skip link at the top of the page goes there too.
	await page.getByRole('link', { name: 'Skip to the date scrubber (Alt+J)' }).focus();
	await page.keyboard.press('Enter');
	await expect(slider).toBeFocused();
	await page.keyboard.press('Escape');

	// A 6-month range is in weeks, 2 years in months.
	for (const [months, kind] of [
		[6, 'week'],
		[24, 'month']
	] as const) {
		const start = new Date();
		start.setMonth(start.getMonth() - months);
		await page.goto(`/?alias=${encodeURIComponent(`"${alias}"`)}&from=${melbourneDay(Math.round((Date.now() - start.getTime()) / 864e5))}&to=${melbourneDay(0)}`);
		await waitLive(page);
		await expect(scrubber.locator(`.mark[data-kind="${kind}"]`).first()).toBeVisible();
		marks = await marksOf(page);
		expect(marks.filter((m) => m.kind === 'day'), `${months} months: no days`).toHaveLength(0);
		expect(marks.filter((m) => m.kind === kind).length, `${months} months`).toBeGreaterThanOrEqual(months === 6 ? 26 : 24);
		expect(marks.reduce((s, m) => s + m.count, 0), `${months} months: every page`).toBe(39 * 3 + 30 + 3);
	}

	// alias:"Station 1": the counts are only Station 1's, as the API counts them.
	await post(request, '0100001', `@@${uniqueJob()} HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN1`);
	await page.goto('/?alias=%22Station+1%22');
	await waitLive(page);
	await expect(scrubber.locator('.mark').first()).toBeVisible();
	const sum = (c: { marks: { count: number }[] }) => c.marks.reduce((s, m) => s + m.count, 0);
	const station1 = await (await request.get(`${URL}/api/v1/messages/counts?alias_like=${encodeURIComponent('Station 1')}`, { headers: asRole('user') })).json();
	const all = await (await request.get(`${URL}/api/v1/messages/counts`, { headers: asRole('user') })).json();
	await expect.poll(async () => (await marksOf(page)).reduce((s, m) => s + m.count, 0)).toBe(sum(station1));
	expect(sum(station1)).toBeLessThan(sum(all));
	// A live page to Station 1 adds to the current hour's mark (R81c).
	const before = (await marksOf(page))[0].count;
	await post(request, '0100001', `@@${uniqueJob()} HSF1 SMOKE FROM SHED @ 4 FICTION ST, MADEUPTON #STN1`);
	await expect.poll(async () => (await marksOf(page))[0].count).toBe(before + 1);
	await page.context().close();
});

// The scrubber on a touch screen (owner, 2026-11-13): at 375 and 1,024 px,
// nothing until the list scrolls, then a grab tab with no marks; dragging it
// scrubs the list while the finger moves, with a bubble naming the day; it
// fades after the scrolling stops. (Firefox can't emulate a touch screen.)
test('Touch scrubber', async ({ browser, browserName }, info) => {
	test.skip(browserName === 'firefox', 'Firefox has no isMobile');
	test.setTimeout(120_000);
	const n = projectN(test.info());
	const capcode = `07200${n}1`;
	const alias = `Touch ${n}`;
	sql(`
INSERT INTO aliases (pattern, name, template_id) SELECT '${capcode}', '${alias}', id FROM templates WHERE name = 'Example Fire' ON CONFLICT DO NOTHING;
DELETE FROM messages WHERE capcode = '${capcode}';
WITH midnight AS (SELECT date_trunc('day', now() AT TIME ZONE '${TZ}') AS d)
INSERT INTO messages (capcode, text, priority, sent_at, dedup_key, alias_id)
SELECT '${capcode}', 'QDTOUCH ${n} DAY ' || g.day || ' PAGE ' || p.i, 'Admin',
       ((SELECT d FROM midnight) - g.day * interval '1 day' + interval '12 hours' + p.i * interval '10 minutes') AT TIME ZONE '${TZ}',
       sha256(convert_to('touch ${n} ' || g.day || ' ' || p.i, 'UTF8')), (SELECT id FROM aliases WHERE pattern = '${capcode}')
FROM generate_series(2, 41) AS g (day) CROSS JOIN generate_series(0, 2) AS p (i);`);
	for (const width of [375, 1024]) {
		const ctx = await signedIn(browser, 'user', { viewport: { width, height: 800 }, hasTouch: true, isMobile: true });
		const page = await ctx.newPage();
		await page.goto(`/?alias=${encodeURIComponent(`"${alias}"`)}`);
		await waitLive(page);
		const scrubber = page.getByTestId('scrubber');
		await expect(scrubber).toHaveAttribute('data-touch', 'true');
		await expect(scrubber).toHaveAttribute('data-shown', 'false');
		await expect(scrubber.locator('.mark'), 'no marks on a touch screen').toHaveCount(0);
		await page.evaluate(() => window.scrollBy(0, 400));
		await expect(scrubber).toHaveAttribute('data-shown', 'true');
		const tab = scrubber.getByRole('slider', { name: 'Jump to a date' });
		const box = (await tab.boundingBox())!;
		expect(box.width, 'a target wide').toBeGreaterThanOrEqual(44);
		expect(box.height, 'a target tall').toBeGreaterThanOrEqual(44);
		// Wide enough to find with a thumb, off the very edge, and the page's own
		// scroll bar hidden while the tab stands in for it (owner, 2026-11-13).
		expect((await tab.locator('.grip').boundingBox())!.width, 'the tab as drawn').toBeGreaterThanOrEqual(50);
		expect(box.x + box.width, 'off the edge').toBeLessThanOrEqual(width - 12);
		expect(await page.evaluate(() => getComputedStyle(document.documentElement).scrollbarWidth)).toBe('none');
		await page.screenshot({ path: info.outputPath(`touch-${width}-shown.png`) });
		const before = await firstInView(page);
		// Tap and drag the tab down a quarter of the rail, a step at a time.
		const rail = (await scrubber.boundingBox())!;
		const x = box.x + box.width / 2;
		const y0 = box.y + box.height / 2;
		const touch = { pointerId: 7, pointerType: 'touch', isPrimary: true, button: 0, buttons: 1, clientX: x };
		await tab.dispatchEvent('pointerdown', { ...touch, clientY: y0 });
		for (let k = 1; k <= 8; k++) {
			await tab.dispatchEvent('pointermove', { ...touch, clientY: y0 + (k * rail.height) / 32 });
			await page.waitForTimeout(60);
		}
		const bubble = scrubber.locator('.tab-bubble');
		await expect(bubble).toBeVisible();
		const day = (await bubble.textContent())!.trim();
		expect(day, 'the day, as "Fri, 23 Oct"').toMatch(/^\w{3},? \d{1,2} \w{3}$/);
		// While the finger is down the list has already moved there.
		await expect.poll(() => firstInView(page)).not.toBe(before);
		await page.screenshot({ path: info.outputPath(`touch-${width}-dragging.png`) });
		await tab.dispatchEvent('pointerup', { ...touch, buttons: 0, clientY: y0 + rail.height / 4 });
		const first = page.getByTestId('message-list').locator('article').filter({ hasText: `TOUCH ${n} DAY` });
		await expect
			.poll(async () => {
				const below = await page.evaluate(() => document.querySelector('header.top')!.getBoundingClientRect().bottom);
				for (const r of await first.all()) {
					const b = (await r.boundingBox())!;
					if (b.y + b.height > below + 1) return (await r.locator('time .date').textContent())!.trim().split(' ').slice(0, 2).join(' ');
				}
				return '';
			})
			.toBe(day.replace(',', '').split(' ').slice(1).join(' '));
		// It fades once the scrolling has stopped.
		await expect(scrubber).toHaveAttribute('data-shown', 'false', { timeout: 5000 });
		expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'no horizontal scroll').toBe(true);
		await ctx.close();
	}
});

// A72. Albums in the browser: made, added to from the detail view, in the
// menu, private, with a description and notes, and on a phone.
test('A72 albums', async ({ browser, request }, info) => {
	test.setTimeout(180_000);
	const n = projectN(test.info());
	const big = `Big storm ${n}`;
	const notes = `Storm notes ${n}`;
	// An earlier run's albums of these names go first.
	sql(`DELETE FROM albums WHERE lower(name) IN (lower('${big}'), lower('${big} renamed'), lower('${notes}')) AND owner_id = (SELECT id FROM users WHERE subject = 'e2e-user');`);
	const stamp = Date.now();
	const texts = [1, 2, 3].map((i) => `QDALBUM ${n} PAGE ${i} ${stamp}`);
	const ids: number[] = [];
	for (const t of texts) {
		await post(request, '0100003', t);
		const r = await request.get(`${URL}/api/v1/messages?q=${encodeURIComponent(`"${t.slice(2)}"`)}`, { headers: asRole('user') });
		ids.push((await r.json()).messages[0].id);
	}
	const page = await (await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } })).newPage();
	// B creates "Big storm" from the Albums page.
	await page.goto('/albums');
	await page.getByLabel('Name').fill(big);
	await page.getByRole('button', { name: 'Create album' }).click();
	await expect(page.getByRole('heading', { level: 1, name: big })).toBeVisible();
	const bigId = Number(new globalThis.URL(page.url()).pathname.split('/').pop());
	expect(await crumbsOf(page)).toEqual([
		{ label: 'Albums', href: '/albums' },
		{ label: big, href: null }
	]);
	// And adds 3 messages, each from its detail view.
	for (const id of ids) {
		await page.goto(`/messages/${id}`);
		await page.getByRole('button', { name: 'Add to album' }).click();
		const dialog = page.getByRole('dialog', { name: 'Add to album' });
		await dialog.getByRole('checkbox', { name: new RegExp(`^${big}`) }).check();
		await expect(dialog.getByRole('status')).toHaveText(`Added to ${big}.`);
		if (id === ids[0]) expect(await axeOf(page), 'axe: Add to album').toEqual([]);
		await dialog.getByRole('button', { name: 'Close' }).click();
		await expect(page.getByTestId('in-albums')).toHaveText('In 1 album');
	}
	// It's under Albums in the menu, with 3 messages.
	const nav = page.getByRole('navigation', { name: 'Main' });
	await expect(nav.getByRole('link', { name: `${big}, 3 messages` })).toBeVisible();
	await nav.getByRole('link', { name: `${big}, 3 messages` }).click();
	await expect(page.getByTestId('album-count')).toHaveText('3 messages');
	const list = page.getByTestId('message-list');
	await expect(list.locator('article p.text')).toHaveText([...texts].reverse().map((t) => t.slice(2)));
	// User C and an admin are refused by the API.
	for (const who of ['userC', 'admin'] as const) {
		expect((await request.get(`${URL}/api/v1/albums/${bigId}`, { headers: asRole(who) })).status(), who).toBe(404);
		expect((await request.get(`${URL}/api/v1/messages?album=${bigId}`, { headers: asRole(who) })).status(), who).toBe(404);
	}

	// "Storm notes": a description, 2 messages, a note on one.
	await page.goto('/albums');
	await page.getByLabel('Name').fill(notes);
	await page.getByLabel(/^Description/).fill('Pages from the made-up storm');
	await page.getByRole('button', { name: 'Create album' }).click();
	await expect(page.getByRole('heading', { level: 1, name: notes })).toBeVisible();
	const notesId = Number(new globalThis.URL(page.url()).pathname.split('/').pop());
	for (const id of ids.slice(0, 2)) expect((await request.put(`${URL}/api/v1/albums/${notesId}/messages/${id}`, { headers: asRole('user') })).status()).toBe(204);
	await page.reload();
	await expect(page.getByTestId('album-description')).toHaveText('Pages from the made-up storm');
	const row = list.locator('article', { hasText: texts[0].slice(2) });
	await row.getByRole('button', { name: 'Add note' }).click();
	await row.getByLabel(/^Note on this message/).fill('First page of the storm');
	await row.getByRole('button', { name: 'Save note' }).click();
	await expect(row.getByTestId('album-note')).toHaveText('First page of the storm');
	await page.reload();
	await expect(page.getByTestId('album-description')).toHaveText('Pages from the made-up storm');
	await expect(list.locator('article', { hasText: texts[0].slice(2) }).getByTestId('album-note')).toHaveText('First page of the storm');
	await expect(list.locator('article', { hasText: texts[1].slice(2) }).getByTestId('album-note')).toHaveCount(0);
	await page.screenshot({ path: info.outputPath('a72-album.png'), fullPage: true });
	expect(await axeOf(page), 'axe: album view').toEqual([]);
	// User C can read neither.
	expect((await request.get(`${URL}/api/v1/albums/${notesId}`, { headers: asRole('userC') })).status()).toBe(404);
	expect((await request.get(`${URL}/api/v1/messages?album=${notesId}`, { headers: asRole('userC') })).status()).toBe(404);
	// In Big storm, the same message has no note.
	await page.goto(`/albums/${bigId}`);
	await expect(list.locator('article')).toHaveCount(3);
	await expect(list.getByTestId('album-note')).toHaveCount(0);
	// The search box's storm finds nothing from a description or a note.
	await page.goto('/?q=storm');
	await waitLive(page);
	await expect(page.getByText('No messages match this search.')).toBeVisible();

	// Retention took both of its messages (as the nightly run does, in
	// TestA72Albums): kept, empty, with its description and the line.
	sql(`DELETE FROM messages WHERE id IN (${ids[0]}, ${ids[1]}); UPDATE albums SET emptied_at = now() WHERE id = ${notesId};`);
	await page.goto(`/albums/${notesId}`);
	await expect(page.getByTestId('album-count')).toHaveText('0 messages');
	await expect(page.getByTestId('album-description')).toHaveText('Pages from the made-up storm');
	await expect(page.getByTestId('album-emptied')).toContainText('Its messages have passed the retention period');

	// Renaming, from the album view, reaches the menu.
	await page.goto(`/albums/${bigId}`);
	await page.getByRole('button', { name: 'Rename or describe' }).click();
	await page.getByLabel('Name').fill(`${big} renamed`);
	await page.getByRole('button', { name: 'Save' }).click();
	await expect(page.getByRole('heading', { level: 1, name: `${big} renamed` })).toBeVisible();
	await expect(nav.getByRole('link', { name: `${big} renamed, 1 message` })).toBeVisible();

	// On a phone: the bottom bar's Albums tab, and the album view.
	const phone = await (await signedIn(browser, 'user', { viewport: { width: 375, height: 812 }, hasTouch: true })).newPage();
	await phone.goto('/');
	await waitLive(phone);
	const bar = phone.getByRole('navigation', { name: 'Main' });
	await bar.getByRole('link', { name: 'Albums' }).tap();
	await expect(phone.getByRole('heading', { level: 1, name: 'Albums' })).toBeVisible();
	await expect(bar.getByRole('link', { name: 'Albums' })).toHaveAttribute('aria-current', 'page');
	expect(await axeOf(phone), 'axe: albums on a phone').toEqual([]);
	await phone.getByRole('link', { name: notes }).tap();
	await expect(phone.getByTestId('album-description')).toBeVisible();
	await expect(phone.getByTestId('scrubber'), 'no scrubber in an album (owner, 2026-11-13)').toHaveCount(0);
	expect(await phone.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'no horizontal scroll').toBe(true);
	await phone.screenshot({ path: info.outputPath('a72-phone-album.png'), fullPage: true });
	expect(await axeOf(phone), 'axe: album on a phone').toEqual([]);
	await phone.goto('/');
	await waitLive(phone);
	// The list on a phone (owner, 2026-11-13): with a mouse no scrubber; with a
	// finger only the grab tab, hidden until the list scrolls ("Touch scrubber").
	await expect(phone.getByTestId('message-list').locator('article').first()).toBeVisible();
	if (await phone.evaluate(() => matchMedia('(hover: none), (pointer: coarse)').matches))
		await expect(phone.getByTestId('scrubber')).toHaveAttribute('data-shown', 'false');
	else await expect(phone.getByTestId('scrubber')).toHaveCount(0);
	expect(await phone.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'no horizontal scroll').toBe(true);
	await phone.screenshot({ path: info.outputPath('a72-phone-list.png') });

	// Deleting it.
	await page.goto(`/albums/${bigId}`);
	await page.getByRole('button', { name: 'Delete album' }).click();
	await page.getByRole('group', { name: /^Delete/ }).getByRole('button', { name: 'Delete album' }).click();
	await expect(page).toHaveURL(/\/albums$/);
	await expect(nav.getByRole('link', { name: new RegExp(`^${big}`) })).toHaveCount(0);
	expect((await request.get(`${URL}/api/v1/albums/${bigId}`, { headers: asRole('user') })).status()).toBe(404);
	await page.context().close();
	await phone.context().close();
});

// A84-A86 in the browser (M22, R87c, R87d): a large-event integration set up
// in the form fires once at its level, and the Large events page under Tools
// lists the event; its view shows its pages with the scrubber, at 1,280 and
// 375 px, with axe. Made-up deployment codes, one set per browser project.
test('A86 large events in the browser', async ({ browser, request }, info) => {
	test.setTimeout(120_000);
	const http = await import('node:http');
	const received: Record<string, unknown>[] = [];
	const receiver = http.createServer((req, res) => {
		let data = '';
		req.on('data', (c) => (data += c));
		req.on('end', () => {
			received.push(JSON.parse(data || '{}'));
			res.writeHead(200, { 'Content-Type': 'text/plain' }).end('received');
		});
	});
	await new Promise<void>((r) => receiver.listen(0, '127.0.0.1', r));
	const port = (receiver.address() as { port: number }).port;
	const n = ['chromium', 'firefox', 'webkit'].indexOf(info.project.name) + 1;
	const codes = [`DG9${n}01`, `DG9${n}02`];
	const jobs = [uniqueJob(), uniqueJob()];
	let integrationID = 0;
	try {
		const allow = await request.put(`${URL}/api/v1/admin/integration-hosts`, { headers: { Cookie: `pa_session=${SESSIONS.admin}` }, data: { entries: ['127.0.0.0/8'] } });
		expect(allow.status()).toBe(200);
		// Earlier tests' pages may have opened an event within 12 hours: start
		// from none, so this one opens and reaches its levels here.
		sql('DELETE FROM large_events;');

		const desk = await (await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } })).newPage();
		await desk.goto('/integrations');
		await desk.getByRole('button', { name: 'New integration' }).click();
		const name = `Several deployments ${Date.now()}`;
		await desk.getByLabel('Name', { exact: true }).fill(name);
		await desk.getByRole('radio', { name: /^Large events: a summary/ }).check();
		// A summary only: no capcodes, filter or content to choose.
		await expect(desk.getByRole('combobox', { name: 'Capcodes and aliases' })).toHaveCount(0);
		await expect(desk.getByText('Full: the text and every field')).toHaveCount(0);
		await desk.getByRole('radio', { name: /^Several deployments:/ }).check();
		await desk.getByLabel('URL', { exact: true }).fill(`http://127.0.0.1:${port}/hook`);
		await desk.getByRole('button', { name: 'Save integration' }).click();
		await expect(desk.getByRole('status').filter({ hasText: `Saved “${name}”` })).toBeVisible();
		const card = desk.getByTestId('integration').filter({ hasText: name });
		await expect(card).toContainText('Large events at level Several deployments');
		const list = await (await request.get(`${URL}/api/v1/integrations`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } })).json();
		integrationID = list.integrations.find((i: { name: string }) => i.name === name).id;

		// The first code opens an event at level deployment: nothing for this
		// integration. The second reaches several deployments: one delivery.
		await post(request, '0100001', `@@${jobs[0]} GRS3 ${codes[0]} GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1`);
		await desk.waitForTimeout(1500);
		expect(received.filter((r) => r.event_type === 'large_event')).toHaveLength(0);
		const start = Date.now();
		await post(request, '0100002', `@@${jobs[1]} GRS3 ${codes[1]} GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2`);
		await expect.poll(() => received.filter((r) => r.event_type === 'large_event').length, { timeout: 5000 }).toBe(1);
		console.log(`A86 (browser): the delivery came ${Date.now() - start} ms after the second code`);
		const got = received.find((r) => r.event_type === 'large_event')!;
		const ev = got.large_event as { level: string; deployment_codes: string[]; emergency_pages: number; url: string; id: number };
		expect(ev.level).toBe('several_deployments');
		expect(ev.deployment_codes).toEqual(codes);
		expect(ev.emergency_pages).toBe(2);
		expect(ev).not.toHaveProperty('units');
		expect(JSON.stringify(got)).not.toContain('GRASS FIRE');
		expect(got.text).toBeUndefined();

		// The Large events page, from Tools: newest first, its first and last pages, its levels (never
		// "ongoing": the app can't know an incident is over; owner, 2026-11-13).
		await desk.goto('/tools');
		await desk.getByRole('main').getByRole('link', { name: 'Large events' }).click();
		await expect(desk.getByRole('heading', { level: 1, name: 'Large events' })).toBeVisible();
		expect(await crumbsOf(desk)).toEqual([
			{ label: 'Tools', href: '/tools' },
			{ label: 'Large events', href: null }
		]);
		const first = desk.getByTestId('large-event').first();
		await expect(first).toHaveAttribute('data-event-id', String(ev.id));
		await expect(first).not.toContainText(/ongoing/i);
		// Its first and last pages' times, each on its own labelled line (owner, 2026-11-13).
		await expect(first.locator('time')).toHaveCount(2);
		await expect(first).toContainText('First page');
		await expect(first).toContainText('Last page');
		await expect(first).toContainText('Deployment');
		await expect(first).toContainText('Several deployments');
		// The number of Emergency pages, never an appliance count (owner, 2026-11-13).
		await expect(first).toContainText('2 deployment codes · 2 jobs · 2 Emergency pages');
		await expect(first).not.toContainText(/unit|appliance/i);
		await desk.screenshot({ path: info.outputPath('large-events-list-1280.png') });
		expect(await axeOf(desk), 'axe: Large events').toEqual([]);

		// Its view: the codes, the jobs and their pages, with the scrubber.
		await first.getByRole('link').click();
		await expect(desk).toHaveURL(new RegExp(`/large-events/${ev.id}$`));
		await expect(desk.getByTestId('event-codes')).toHaveText(codes.join(', '));
		await expect(desk.getByTestId('event-about')).toContainText('2 deployment codes · 2 jobs · 2 Emergency pages');
		await expect(desk.getByTestId('event-about')).not.toContainText(/unit|appliance/i);
		await expect(desk.getByTestId('event-about').getByRole('term')).toContainText(['First page', 'Last page', 'Levels']);
		const rows = desk.getByTestId('message-list').locator('article');
		await expect(rows).toHaveCount(2);
		await expect(rows.first()).toContainText(jobs[1]);
		await expect(rows.nth(1)).toContainText(jobs[0]);
		await expect(desk.getByTestId('scrubber')).toBeVisible();
		await waitLive(desk);
		const crumbs = await crumbsOf(desk);
		expect(crumbs.slice(0, 2)).toEqual([
			{ label: 'Tools', href: '/tools' },
			{ label: 'Large events', href: '/large-events' }
		]);
		expect(crumbs[2].label).toMatch(/^Large event, /);
		// A page of one of its jobs joins it, live.
		await post(request, '0100003', `@@${jobs[0]} GRS3 MORE CREWS NEEDED @ SAMPLE RD, MADEUPTON #STN3`);
		await expect(rows).toHaveCount(3, { timeout: 2000 });
		await desk.screenshot({ path: info.outputPath('large-event-view-1280.png'), fullPage: true });
		expect(await axeOf(desk), 'axe: a large event').toEqual([]);

		// A phone: both pages fit, with no horizontal scroll.
		const phone = await (await signedIn(browser, 'user', { viewport: { width: 375, height: 740 } })).newPage();
		const overflow = () => phone.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
		await phone.goto('/large-events');
		await expect(phone.getByTestId('large-event').first()).toContainText('Several deployments');
		expect(await overflow()).toBeLessThanOrEqual(0);
		await phone.screenshot({ path: info.outputPath('large-events-list-375.png') });
		expect(await axeOf(phone), 'axe: Large events on a phone').toEqual([]);
		await phone.getByTestId('large-event').first().getByRole('link').click();
		await expect(phone.getByTestId('message-list').locator('article')).toHaveCount(3);
		await waitLive(phone);
		expect(await overflow()).toBeLessThanOrEqual(0);
		await phone.screenshot({ path: info.outputPath('large-event-view-375.png'), fullPage: true });
		expect(await axeOf(phone), 'axe: a large event on a phone').toEqual([]);
		// Another code joining doesn't fire it again.
		await post(request, '0100004', `@@${uniqueJob()} GRS3 DG9${n}03 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN4`);
		await phone.waitForTimeout(1500);
		expect(received.filter((r) => r.event_type === 'large_event')).toHaveLength(1);
	} finally {
		if (integrationID) await request.delete(`${URL}/api/v1/integrations/${integrationID}`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
		receiver.close();
	}
});

// R87: the admins' threshold for a big job, in Administration.
test('A85 large event settings in Administration', async ({ browser }) => {
	const admin = await (await signedIn(browser, 'admin', { viewport: { width: 1280, height: 900 } })).newPage();
	await admin.goto('/admin');
	await admin.getByTestId('admin-screens').getByRole('link', { name: 'Large event settings' }).click();
	const field = admin.getByLabel('Emergency pages for a big job');
	await expect(field).toHaveValue('20');
	await field.fill('25');
	await admin.getByRole('button', { name: 'Save' }).click();
	await expect(admin.getByRole('status').filter({ hasText: 'has 25 Emergency pages' })).toBeVisible();
	await admin.reload();
	await expect(admin.getByLabel('Emergency pages for a big job')).toHaveValue('25');
	expect(await axeOf(admin), 'axe: large event settings').toEqual([]);
	await admin.getByLabel('Emergency pages for a big job').fill('20');
	await admin.getByRole('button', { name: 'Save' }).click();
	await expect(admin.getByRole('status').filter({ hasText: 'has 20 Emergency pages' })).toBeVisible();
	// A user is refused.
	const user = await (await signedIn(browser, 'user')).newPage();
	await user.goto('/admin/large-events');
	await expect(user.getByRole('alert')).toHaveText('This page is for admins only.');
});
