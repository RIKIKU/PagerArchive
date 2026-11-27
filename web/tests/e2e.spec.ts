/// <reference types="node" />
// Browser acceptance checks (SPEC.md section 9). scripts/e2e.sh starts the API
// on a fresh database with the standard test fixture, a client key, and a
// signed-in user and admin (their session tokens).
import { test, expect, type Browser, type BrowserContext, type BrowserContextOptions, type Locator, type Page, type APIRequestContext, type TestInfo } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { execFileSync, spawn } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const URL = process.env.PA_URL ?? 'http://127.0.0.1:18080';
// userC: a second user, for the print sheet kept per user (M11).
const SESSIONS = {
	user: process.env.PA_USER_SESSION ?? '',
	admin: process.env.PA_ADMIN_SESSION ?? '',
	userC: process.env.PA_USER_C_SESSION ?? ''
};
type Role = keyof typeof SESSIONS;

// R77d: the menu's print sheet entry and the version's tag, as ArchiveUx's
// Menu draws them, by their accessible names (R67, R77).
const SHEET_LINK = /^Print sheet, [0-9]+ messages?$/;
const VERSION_TAG = /^Release notes for v/;

const sessionCookie = (role: Role) => ({ name: 'pa_session', value: SESSIONS[role], url: URL });

// R36a (A153): every securitypolicyviolation event on the pages a test opens,
// as "<directive> <blocked URI> on <path>". The test fails on any, after it
// has run, but those it says it causes (expectCspViolation).
let cspViolations: string[] = [];

/** Watches ctx's pages for CSP violations, from before their first script. */
async function watchCsp(ctx: BrowserContext) {
	await ctx.exposeBinding('__paCspViolation', (_source, v: string) => void cspViolations.push(v));
	await ctx.addInitScript(() => {
		document.addEventListener('securitypolicyviolation', (e) =>
			(window as unknown as { __paCspViolation: (v: string) => void }).__paCspViolation(`${e.effectiveDirective} ${e.blockedURI} on ${location.pathname}`)
		);
	});
	return ctx;
}

/** A test causes violations starting "<directive> <blocked URI>" on purpose. */
function expectCspViolation(info: TestInfo, violation: string) {
	info.annotations.push({ type: 'csp-violation-expected', description: violation });
}

/** A new browser context, watched for CSP violations. */
async function newContext(browser: Browser, opts: BrowserContextOptions = {}) {
	return watchCsp(await browser.newContext(opts));
}

/** A browser context signed in as role. */
async function signedIn(browser: Browser, role: Role = 'user', opts: BrowserContextOptions = {}) {
	const ctx = await newContext(browser, opts);
	await ctx.addCookies([sessionCookie(role)]);
	return ctx;
}

/** R82d: saves role's look on their account, through the API. */
async function setLook(request: APIRequestContext, role: Role, look: 'harbour' | 'console') {
	const res = await request.patch(`${URL}/api/v1/me`, { headers: { Cookie: `pa_session=${SESSIONS[role]}` }, data: { look } });
	expect(res.status(), `${role} chooses ${look}`).toBe(200);
}

/** The user's and the admin's look: a test that sets Console puts Harbour back after, pass or fail. */
async function setLooks(request: APIRequestContext, look: 'harbour' | 'console') {
	await setLook(request, 'user', look);
	await setLook(request, 'admin', look);
}

// The page fixture is signed in as the user, and watched for CSP violations.
test.beforeEach(async ({ context }) => {
	cspViolations = [];
	await watchCsp(context);
	await context.addCookies([sessionCookie('user')]);
});
test.afterEach(async ({}, info) => {
	const expected = info.annotations.filter((a) => a.type === 'csp-violation-expected').map((a) => a.description ?? '');
	const unexpected = cspViolations.filter((v) => !expected.some((e) => v.startsWith(e)));
	cspViolations = [];
	expect(unexpected, 'Content-Security-Policy violations on the pages this test opened (R36a)').toEqual([]);
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
	await Promise.all([a.goto('/?callsign=STN2'), b.goto('/')]);
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

// A128 (R8, ADR 0047): the call signs and roles a page names are shown as
// Call signs, after Appliances, in its detail view, on a phone and beside the
// list on a desktop; its row shows the paged unit's call sign only (R31).
test('A128 call signs apart from appliances', async ({ browser, request }) => {
	const job = uniqueJob();
	await post(request, '0100001', `@@${job} HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON UNITS T1 STN2 STN2T DO01 #STN1`);
	const res = await request.get(`${URL}/api/v1/messages?job=${job}`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
	const m = (await res.json()).messages[0] as { id: number; units: string[]; call_signs: string[]; call_sign: string };
	expect([m.units, m.call_signs, m.call_sign]).toEqual([['T1', 'STN2T'], ['STN2', 'DO01'], 'STN1']);
	for (const viewport of [{ width: 375, height: 740 }, { width: 1440, height: 900 }]) {
		const ctx = await signedIn(browser, 'user', { viewport });
		const page = await ctx.newPage();
		await page.goto(`/?job=${job}`);
		const row = page.getByTestId('message-list').locator('article').first();
		await expect(row.locator('.callsign')).toHaveText(/^Call sign\sSTN1$/);
		await expect(row).not.toContainText('Call signs');
		await page.goto(`/messages/${m.id}`);
		const details = page.locator('.rest dl');
		await expect(details.locator('dt:text-is("Appliances") + dd')).toHaveText('T1, STN2T');
		await expect(details.locator('dt:text-is("Call signs") + dd')).toHaveText('STN2, DO01');
		await expect(details.locator('dt:text-is("Call sign") + dd')).toHaveText('STN1');
		const terms = await details.locator('dt').allTextContents();
		expect(terms.indexOf('Call signs'), `${viewport.width} px: Call signs after Appliances`).toBe(terms.indexOf('Appliances') + 1);
		const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
		expect(overflow, `${viewport.width} px: no horizontal scroll`).toBeLessThanOrEqual(0);
		await ctx.close();
	}
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

// A1 in the browser: the job view lists every call sign's copy.
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

// A147, R35h: a page that's probably noise shows the badge, with its reason
// as the tooltip and the accessible description, on its row and its detail
// view; a page that isn't shows none. One receiver here, so the mark is set
// in the database (the rule's legs are A147's Go checks).
test('A147 a probably-noise page shows the badge with its reason', async ({ page, request }, info) => {
	const marker = `NOISE-${projectN(info)}-${Date.now()}`;
	await post(request, '0100001', `${marker} NOISY`);
	await post(request, '0100001', `${marker} CLEAN`);
	sql(`UPDATE messages SET probably_noise = true WHERE text = '${marker} NOISY';`);
	const why = 'Heard by one receiver only, matched no format, nothing else to its capcode was really heard';

	await page.goto(`/?q=${marker}`);
	const list = page.getByTestId('message-list');
	await expect(list.locator('article')).toHaveCount(2);
	const noisy = list.locator('article').filter({ hasText: `${marker} NOISY` });
	const clean = list.locator('article').filter({ hasText: `${marker} CLEAN` });
	// ArchiveUx's neutral state badge, in a span (data-noise) holding the tooltip.
	const badge = noisy.locator('[data-noise]');
	await expect(badge).toBeVisible();
	await expect(badge).toHaveAttribute('title', why);
	await expect(badge.locator('.state-badge[data-state="neutral"]')).toContainText('Probably noise');
	await expect(badge).toContainText(why); // read out after its name
	await expect(clean.getByText('Probably noise')).toHaveCount(0);

	await noisy.getByRole('link', { name: `${marker} NOISY` }).click();
	await expect(page).toHaveURL(/\/messages\/\d+/);
	const detail = page.locator('article.detail [data-noise]');
	await expect(detail.first()).toBeVisible();
	await expect(detail.first()).toHaveAttribute('title', why);
	await expect(detail.first()).toContainText('Probably noise');
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
	const ctx = await newContext(browser);
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
	const page = await (await newContext(browser)).newPage();
	await page.goto('/jobs/EX0000001');
	await expect(page).toHaveURL(/\/signin\?error=directory_unavailable$/);
	await expect(page.getByRole('alert')).toContainText("couldn't be reached");
	await expect(page.getByRole('link', { name: 'Sign in' })).toBeVisible();
});

// A117 (R40b): no Authentik runs here, so signing out can't end its
// session: the app's session still ends at once, and the sign-in page says
// Authentik's sign-out couldn't be reached. A session of its own, so the
// other tests' stay signed in.
test("A117 signing out with Authentik unreachable still ends the app's session, and says so", async ({ browser }) => {
	const token = `e2e-signout-${Date.now()}-${Math.random().toString(36).slice(2)}`;
	sql(`INSERT INTO sessions (token_hash, user_id, expires_at)
  SELECT sha256(convert_to('${token}', 'UTF8')), id, now() + interval '1 day' FROM users WHERE subject = 'e2e-user';`);
	const ctx = await newContext(browser);
	await ctx.addCookies([{ name: 'pa_session', value: token, url: URL }]);
	const page = await ctx.newPage();
	await page.goto('/account/profile');
	await page.getByRole('button', { name: 'Sign out' }).click();
	await expect(page).toHaveURL(/\/signin\?signed_out=1&authentik=unreachable$/, { timeout: 10_000 });
	await expect(page.getByRole('status').filter({ hasText: "You've signed out." })).toContainText("couldn't be reached");
	expect((await ctx.request.get(`${URL}/api/v1/me`)).status(), 'signed out of the app').toBe(401);
	expect((await ctx.cookies()).map((c) => c.name)).not.toContain('pa_session');
	await ctx.close();
});

// R44: while the first admin can still be set up, a signed-out visitor is
// sent to the sign-in page, which offers it, not straight to Authentik. An
// admin exists here, so the status is answered for the test; the stack test
// checks it for real from the root URL.
test('R44 signed-out visitors on a new install see first-admin set-up', async ({ browser }) => {
	const page = await (await newContext(browser)).newPage();
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
	const ctx = await newContext(browser);
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
const views = ['/', '/?q=SMOKE', '/capcodes', '/jobs/EX0000001', 'detail', 'alias', 'alias-request', '/capcodes/0999999', '/map', '/reports/capcode?capcode=0100001&from=2026-01-01&to=2026-12-31', '/admin', '/admin/clients', '/admin/users', 'admin-user', 'admin-tree-preview', '/admin/aliases', 'admin-alias', '/admin/templates', 'admin-template', '/admin/requests', '/admin/retention', '/admin/backups', '/admin/upgrades', '/admin/reparse', '/admin/integration-hosts', '/integrations', 'integration-form', 'selection', '/requests', '/sheet', '/account/profile', '/account/invite', '/tools', '/reports', '/account', '/albums', 'album', '/large-events', 'large-event', '/admin/large-events', '/signin', '/signin?error=deactivated'];
for (const look of ['harbour', 'console'] as const) {
	for (const scheme of ['light', 'dark'] as const) {
		for (const width of [375, 1280]) {
			test(`axe: every view${look === 'console' ? ', Console' : ''}, ${scheme}, ${width}px`, async ({ browser, request }) => {
				test.setTimeout(360_000); // 35 views
				// M15: in each look (R82d), on the accounts the views are seen as.
				await setLooks(request, look);
				try {
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
					// M22: a large event (R87c), from a made-up code.
					await post(request, '0100002', `@@${uniqueJob()} GRS3 DG9${look === 'console' ? 7 : 8}${scheme === 'light' ? 1 : 2}${width === 375 ? 1 : 2} GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2`);
					const eventID = (await (await request.get(`${URL}/api/v1/large-events`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } })).json()).events[0].id;
					// M6: a waiting alias request, so the review screen and My requests
					// are checked with one; rejected at the end so it doesn't linger.
					const reqPattern = `049${['chromium', 'firefox', 'webkit'].indexOf(test.info().project.name) + 1}${scheme === 'light' ? 1 : 2}${width === 375 ? 1 : 2}${look === 'console' ? 2 : 1}`;
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
						// The integration form and its selection field are checked at
						// 375 px too: phones set up integrations (owner, 2026-11-14).
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
						// The look the view is drawn in: the account's, or Harbour before sign-in.
						if (view.startsWith('/signin')) expect(await page.locator('html').getAttribute('data-look'), view).toBeNull();
						else await expect(page.locator('html'), view).toHaveAttribute('data-look', look);
						const results = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
						const summary = results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join('; ')}`);
						expect(summary, `${view} (${look}, ${scheme}, ${width}px)`).toEqual([]);
					}
					// Each context holds a live stream and, since M4, a map with a WebGL
					// context; left open they slow the following tests down.
					await request.post(`${URL}/api/v1/admin/alias-requests/${reqID}/reject`, {
						headers: { Cookie: `pa_session=${SESSIONS.admin}` },
						data: { feedback: 'Wrong station: axe test' }
					});
					await Promise.all([ctx.close(), userCtx.close()]);
				} finally {
					await setLooks(request, 'harbour');
				}
			});
		}
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


// R35f, A34a. Every map has a full-screen button. Where the browser can put
// an element full screen (Chromium at 1280 px), the map and its strip are;
// on phones (375 px) and where it can't, they're an overlay over the whole
// viewport that Back closes. The strip says what the map is for, the map
// still works inside, and closing goes back to the same place with the same
// view. Headless Firefox has no WebGL, so no map and no button there.
async function fullScreenCheck(page: Page, info: TestInfo, opts: { width: number; strip: RegExp; shots?: string; expect: 'native' | 'overlay' | 'either' }) {
	const map = page.getByTestId('map');
	await expect(map).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
	if ((await map.getAttribute('data-ready')) !== 'true') {
		expect(info.project.name, 'only headless Firefox has no map').toBe('firefox');
		await expect(page.getByRole('button', { name: 'Full screen map' })).toHaveCount(0);
		return;
	}
	const frame = page.getByTestId('map-frame');
	const open = page.getByRole('button', { name: 'Full screen map' });
	await expect(map).toHaveAttribute('data-view', /./);
	const before = { view: await map.getAttribute('data-view'), url: page.url(), scroll: await page.evaluate(() => scrollY) };
	if (opts.shots) await page.screenshot({ path: info.outputPath(`${opts.shots}-closed.png`) });
	// The button: a target's size (R82c), and reached and pressed by keyboard.
	const target = (await open.boundingBox())!;
	expect(Math.min(target.width, target.height)).toBeGreaterThanOrEqual(36);
	await open.focus();
	// Where the page is once the button has focus (focus may scroll to it).
	before.scroll = await page.evaluate(() => scrollY);
	await page.keyboard.press('Enter');
	await expect(frame).toHaveAttribute('data-fullscreen', /^(native|overlay)$/);
	const how = (await frame.getAttribute('data-fullscreen'))!;
	console.log(`R35f ${info.project.name} ${opts.width}px: ${how}`);
	if (opts.expect !== 'either') expect(how).toBe(opts.expect);
	const close = page.getByRole('button', { name: 'Exit full screen' });
	await expect(close).toBeVisible();
	await expect(close).toBeFocused();
	// The map and strip fill the screen; the strip says what the map is for.
	const vp = page.viewportSize()!;
	const strip = page.getByTestId('map-strip');
	await expect(strip).toHaveText(opts.strip);
	await expect
		.poll(async () => {
			const b = (await frame.boundingBox())!;
			return [Math.round(b.x), Math.round(b.y), Math.round(b.width), Math.round(b.height)];
		})
		.toEqual([0, 0, vp.width, vp.height]);
	await expect
		.poll(
			async () => {
				const s = (await strip.boundingBox())!;
				const m = (await map.boundingBox())!;
				return Math.abs(m.y - (s.y + s.height)) <= 1 && Math.abs(m.y + m.height - vp.height) <= 1 && Math.round(m.width) === vp.width;
			},
			{ message: 'the map fills the screen below the strip' }
		)
		.toBe(true);
	if (how === 'overlay') {
		expect(await page.evaluate(() => getComputedStyle(document.documentElement).overflow), 'the page behind does not scroll').toBe('hidden');
		const behind = await page.evaluate(() => [...document.body.children].filter((e) => !e.matches('[data-testid="map-frame"]') && !e.hasAttribute('inert')).map((e) => e.tagName));
		expect(behind, 'the page behind is inert').toEqual([]);
	}
	// The same view as before, resized to the screen.
	await expect(map).toHaveAttribute('data-view', before.view!);
	if (opts.shots) await page.screenshot({ path: info.outputPath(`${opts.shots}-open.png`) });
	const axe = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
	expect(axe.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join('; ')}`), 'axe, full screen').toEqual([]);
	// It works as before: zoom in with the map's own control.
	const zoom = (v: string | null) => Number(v!.split(',')[2]);
	await map.getByRole('button', { name: 'Zoom in', exact: true }).click();
	await expect.poll(async () => zoom(await map.getAttribute('data-view'))).toBeGreaterThan(zoom(before.view));
	const inside = await map.getAttribute('data-view');
	// Close: Back on a phone (the overlay's own history entry), else the button.
	if (how === 'overlay') await page.goBack();
	else await close.click();
	await expect(frame).not.toHaveAttribute('data-fullscreen');
	await expect(open).toBeFocused();
	expect(page.url(), 'still on the same page').toBe(before.url);
	await expect.poll(() => page.evaluate(() => scrollY)).toBe(before.scroll);
	await expect(map).toHaveAttribute('data-view', inside!);
	expect(await page.evaluate(() => getComputedStyle(document.documentElement).overflow)).not.toBe('hidden');
	const axeClosed = await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
	expect(axeClosed.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join('; ')}`), 'axe, closed').toEqual([]);
	if (how === 'overlay') {
		await open.click();
		await expect(frame).toHaveAttribute('data-fullscreen', 'overlay');
		// Portrait and landscape.
		await page.setViewportSize({ width: vp.height, height: vp.width });
		await expect.poll(async () => [Math.round((await frame.boundingBox())!.width), Math.round((await frame.boundingBox())!.height)]).toEqual([vp.height, vp.width]);
		await page.setViewportSize(vp);
		await expect.poll(async () => Math.round((await frame.boundingBox())!.width)).toBe(vp.width);
		// Escape closes it too, and takes its history entry with it.
		await page.keyboard.press('Escape');
		await expect(frame).not.toHaveAttribute('data-fullscreen');
		await expect.poll(() => page.url()).toBe(before.url);
	}
}

for (const width of [375, 1280]) {
	const viewport = { width, height: width === 375 ? 812 : 900 };
	test(`R35f full-screen map in the detail view, ${width}px`, async ({ browser, request }, info) => {
		const job = uniqueJob();
		await post(request, '0100001', `@@${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
		const res = await request.get(`${URL}/api/v1/messages?q=${job}`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
		const id = (await res.json()).messages[0].id;
		const ctx = await signedIn(browser, 'user', { viewport });
		const page = await ctx.newPage();
		await page.goto('/capcodes');
		await page.goto(`/messages/${id}`);
		await expect(page.getByRole('heading', { name: 'Received by' })).toBeVisible();
		// Scrolled a little, to come back to the same place.
		await page.evaluate(() => scrollTo(0, 40));
		const how = width === 375 ? 'overlay' : info.project.name === 'chromium' ? 'native' : 'either';
		await fullScreenCheck(page, info, { width, strip: /^Station 1 \(0100001\), .*\d$/, shots: `r35f-detail-${width}`, expect: how });
		// Back now leaves the message, as before: full screen left nothing in the history.
		await page.goBack();
		await expect(page).toHaveURL(/\/capcodes$/);
		await ctx.close();
	});

	test(`R35f full-screen map on the map page, ${width}px`, async ({ browser, request }, info) => {
		await post(request, '0100001', `@@${uniqueJob()} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
		const ctx = await signedIn(browser, 'user', { viewport });
		const page = await ctx.newPage();
		await page.goto('/map');
		const how = width === 375 ? 'overlay' : info.project.name === 'chromium' ? 'native' : 'either';
		await fullScreenCheck(page, info, { width, strip: /^Located messages, the last 24 hours$/, shots: `r35f-map-${width}`, expect: how });
		await ctx.close();
	});
}

// R35f: a desktop browser without element full screen (as iPhone Safari) gets the overlay.
test('R35f full-screen map without the Fullscreen API', async ({ browser, request }, info) => {
	await post(request, '0100001', `@@${uniqueJob()} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
	const ctx = await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } });
	await ctx.addInitScript(() => {
		delete (Element.prototype as Partial<Element>).requestFullscreen;
	});
	const page = await ctx.newPage();
	await page.goto('/map?q=SMOKE');
	await fullScreenCheck(page, info, { width: 1280, strip: /^Located messages, the last 24 hours: SMOKE$/, expect: 'overlay' });
	await ctx.close();
});

// R35f: the busiest localities' report map (R38c), its heat layer inside.
for (const width of [375, 1280]) {
	test(`R35f full-screen map in the busiest localities report, ${width}px`, async ({ browser, request }, info) => {
		const n = projectN(info);
		await reportWeek(request, n);
		const ctx = await signedIn(browser, 'user', { viewport: { width, height: width === 375 ? 812 : 900 } });
		const page = await ctx.newPage();
		await page.goto(`/reports/localities?${reportRange(n)}`);
		await expect(page.getByTestId('report')).toBeVisible();
		const how = width === 375 ? 'overlay' : info.project.name === 'chromium' ? 'native' : 'either';
		await fullScreenCheck(page, info, { width, strip: new RegExp(`^Busiest localities, .+ to .+, Report agency ${n}$`), shots: `r35f-report-${width}`, expect: how });
		await ctx.close();
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
	await expect(page.getByTestId('report-caveat')).toBeVisible();
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
	// R82e: under the box, which says it's invalid and takes focus; the top line only counts.
	await expect(page.locator('#report-box-error')).toHaveText(`There's no alias "no such alias anywhere". Pick one from the suggestions.`);
	await expect(box).toHaveAttribute('aria-invalid', 'true');
	await expect(box).toHaveAttribute('aria-describedby', 'report-box-error');
	await expect(box).toBeFocused();
	await expect(page.getByTestId('form-summary')).toHaveText('1 field needs attention');
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
		await page.getByLabel('Name required', { exact: true }).fill(name);
		await chooseInSelection(page, '0100001', 'Station 1 (0100001)');
		await page.getByLabel('URL required', { exact: true }).fill(`http://127.0.0.1:${port}/hook/T0PS3CRET`);
		await page.getByRole('button', { name: 'Save integration' }).click();
		await expect(page.getByRole('status').filter({ hasText: `Saved “${name}”` })).toBeVisible();
		const card = page.getByTestId('integration').filter({ hasText: name });
		await expect(card.getByTestId('integration-status')).toHaveText('Active');
		await expect(card).toContainText(`http://127.0.0.1:${port}/…`);
		await expect(page.locator('body')).not.toContainText('T0PS3CRET'); // the URL is a secret once saved

		// Send test (R51c): the owner sees whether it was delivered and the status,
		// never the reply's body (A107).
		await card.getByRole('button', { name: 'Send test' }).click();
		const answer = card.getByTestId('test-answer');
		await expect(answer).toContainText('Delivered.');
		await expect(answer).toContainText('HTTP 200 OK');
		await expect(answer).not.toContainText('received');
		await expect(answer.locator('pre')).toHaveCount(0);
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
	await page.getByLabel('Name required', { exact: true }).fill(name);
	await page.getByLabel('URL required', { exact: true }).fill('http://127.0.0.1:9/hook');
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
		for (const path of ['/admin', '/admin/users', '/admin/users/1', '/admin/aliases', '/admin/aliases/1', '/admin/templates', '/admin/templates/1', '/admin/requests', '/admin/clients', '/admin/retention', '/admin/backups', '/admin/upgrades', '/admin/reparse', '/admin/redaction', '/admin/integration-hosts']) {
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
		await expect(screens.getByRole('link')).toHaveText(['Users', 'Aliases', 'Templates', 'Alias requests', 'Receiving clients', 'Retention', 'Backups', 'Upgrades', 'Re-parse', 'Retroactive redaction', 'Integration addresses', 'Large event settings']);
		for (const [link, heading] of [
			['Users', 'Users'],
			['Aliases', 'Aliases'],
			['Templates', 'Templates'],
			['Alias requests', 'Alias requests'],
			['Receiving clients', 'Receiving clients'],
			['Retention', 'Retention'],
			['Backups', 'Backups'],
			['Upgrades', 'Upgrades'],
			['Re-parse', 'Re-parse'],
			['Retroactive redaction', 'Retroactive redaction'],
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
	await form.getByLabel('Note for the admins').fill('NOTE-MARKER new pager');
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
	// R82e: the empty feedback is caught before it's sent, under the field, which takes focus.
	await expect(card.locator('.field-error')).toHaveText("Say why it's rejected: they'll see this.");
	await expect(card.getByLabel('Feedback for the person who asked required')).toBeFocused();
	await expect(card.getByTestId('form-summary')).toHaveText('1 field needs attention');
	await card.getByLabel('Feedback for the person who asked required').fill('Wrong station');
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
		await card.getByLabel('Feedback for the person who asked required').fill('Wrong station: already done');
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
	await admin.getByLabel('Reason (only admins see it)').fill('spam REASON-MARKER');
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
	await form.getByLabel('Agency required', { exact: true }).fill('Example Rescue Service');
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
	const fileInput = admin.getByLabel(/^(Add an icon|Replace it with) required$/);
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
	// R36a: the one violation this test causes, the probe from another site
	// below (A153); a browser may report its address or only its origin.
	expectCspViolation(info, `img-src ${new globalThis.URL(probe).origin}`);
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
	await expect(page.getByRole('link', { name: SHEET_LINK })).toHaveAccessibleName('Print sheet, 0 messages');
	await page.getByRole('link', { name: SHEET_LINK }).click();
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
	await expect(page.getByRole('link', { name: SHEET_LINK })).toHaveAccessibleName('Print sheet, 3 messages');
	await expect.poll(() => sheetIds(page)).toEqual([b, c, a]);

	// A second search, for the job: d.
	await searchWith(page, `job:${job}`);
	await expect(page).toHaveURL(new RegExp(`/sheet\\?.*job=${job}`));
	await expect(page.getByTestId('message-list').locator('article')).toHaveCount(1);
	await tickOf(page, d).check();
	await expect(page.getByRole('link', { name: SHEET_LINK })).toHaveAccessibleName('Print sheet, 4 messages');
	await expect(page.getByRole('link', { name: SHEET_LINK })).toContainText('4');
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
	await expect(page.getByRole('link', { name: SHEET_LINK })).toHaveAccessibleName('Print sheet, 3 messages');
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
	await expect(page.getByRole('link', { name: SHEET_LINK })).toHaveAccessibleName('Print sheet, 0 messages');
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
	await expect(page.getByRole('link', { name: SHEET_LINK })).toHaveAccessibleName('Print sheet, 1 message');

	// C signs in in the same browser.
	await ctx.clearCookies();
	await ctx.addCookies([sessionCookie('userC')]);
	await page.goto('/sheet');
	await expect(page.getByTestId('sheet-count')).toContainText('The print sheet is empty.');
	await expect(page.getByRole('link', { name: SHEET_LINK })).toHaveAccessibleName('Print sheet, 0 messages');
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
	await expect(other.getByRole('link', { name: SHEET_LINK })).toHaveAccessibleName('Print sheet, 1 message');
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
	await expect(list.getByRole('link', { name: SHEET_LINK })).toHaveAccessibleName('Print sheet, 100 messages');
	await tickOf(list, live).click();
	await expect(tickOf(list, live)).not.toBeChecked();
	await expect(list.getByTestId('sheet-full')).toHaveText('The print sheet is full: it holds 100 messages. Remove some before adding more.');
	expect(JSON.parse((await storage(list))[sheetKey(userId)])).toEqual(fake);
	await list.unroute('**/api/v1/messages/by-id?*');

	// Gone: none of the 100 exists, so they all leave, with a note.
	await list.reload();
	await expect(list.getByTestId('sheet-gone')).toHaveText('100 messages were no longer available and have been removed from the sheet.');
	await expect(list.getByRole('link', { name: SHEET_LINK })).toHaveAccessibleName('Print sheet, 0 messages');

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
	await expect(page.getByRole('link', { name: SHEET_LINK })).toHaveAccessibleName('Print sheet, 1 message');
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
	const keys = ['alias:', 'agency:', 'capcode:', 'callsign:', 'priority:', 'category:', 'job:', 'is:', 'or:'];
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
		Reports: ['Capcode report', 'Busiest capcodes', 'Busiest localities', "When it's busy", 'Event type mix', 'Job size', 'Long-running jobs', 'Appliance activity', 'Large events over time'],
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
		// A group's name may say more after a comma: Tools' that a large event is new (R77c).
		for (const g of Object.keys(expected))
			await expect(nav.getByRole('link', { name: g, exact: true }).or(nav.getByRole('link', { name: new RegExp(`^${g},`) })), `${role}: ${g}`).toHaveCount(groups.includes(g) ? 1 : 0);
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
				// The list's own heading is there before the page has gone.
				await page.waitForURL((u) => u.pathname !== '/');
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
				// Less the scroll bar's room the page keeps beside a scrubber (R81b).
				const kept = parseFloat(getComputedStyle(document.documentElement).paddingRight) || 0;
				return { menu: side.width, left: l.left - side.right, right: document.documentElement.clientWidth - kept - pane.right };
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

// A70. Phones: the bottom bar, More without the print sheet or Administration,
// integrations set up, edited, tested, paused, resumed and deleted on a 375 px
// phone by touch (owner, 2026-11-14), and the print page from its address.
test('A70 phones', async ({ browser, request }, info) => {
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
	await request.put(`${URL}/api/v1/admin/integration-hosts`, { headers: asRole('admin'), data: { entries: ['127.0.0.0/8'] } });
	const stamp = Date.now();
	const name = `Phone webhook ${stamp}`;
	const renamed = `Phone webhook edited ${stamp}`;
	const events = `Phone large events ${stamp}`;
	type Mine = { id: number; name: string; status: string; capcodes: string[]; aliases: { name: string }[] };
	const mine = async (): Promise<Mine[]> => (await (await request.get(`${URL}/api/v1/integrations`, { headers: asRole('user') })).json()).integrations;
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
			for (const there of ['Unknown capcodes', 'Large events', 'Capcode report', 'Profile', 'Integrations', 'My requests', 'Invite someone']) await expect(more.getByRole('link', { name: there, exact: true }).or(more.getByRole('link', { name: new RegExp(`^${there},`) }))).toBeVisible();
			for (const gone of ['Print sheet', 'Administration', 'Users', 'Aliases', 'Templates', 'Alias requests', 'Receiving clients', 'Retention', 'Backups', 'Integration addresses', 'Large event settings'])
				await expect(more.getByRole('link', { name: gone })).toHaveCount(0);
			await page.screenshot({ path: info.outputPath(`a70-more-${role}.png`) });
			expect(await axeOf(page), `${role}: axe with More open`).toEqual([]);
			if (role === 'user') {
				await phoneIntegrations(page);
				await page.goto('/sheet');
				await expect(page.getByRole('heading', { level: 1, name: 'Print sheet' })).toBeVisible();
				await waitLive(page);
				console.log('A70: /sheet opened from its address');
			}
			await page.context().close();
		}
	} finally {
		receiver.close();
		for (const i of await mine()) if (i.name.endsWith(String(stamp))) await request.delete(`${URL}/api/v1/integrations/${i.id}`, { headers: asRole('user') });
	}

	/** How far the page scrolls sideways (0: not at all). */
	function sideways(page: Page) {
		return page.evaluate(() => Math.max(0, document.documentElement.scrollWidth - document.documentElement.clientWidth));
	}

	/** The form's controls that are too small for a finger (R82c's --target):
	 * buttons, fields, each radio's and checkbox's label, and suggestions. */
	async function tooSmall(form: Locator) {
		return form.evaluate((f) => {
			const probe = document.createElement('div');
			probe.style.height = 'var(--target)';
			document.body.append(probe);
			const target = probe.getBoundingClientRect().height;
			probe.remove();
			const small: string[] = [];
			const controls = f.querySelectorAll(
				'button, select, textarea, input:not([type="checkbox"]):not([type="radio"]), label:has(> input[type="checkbox"]), label:has(> input[type="radio"]), [role="option"]'
			);
			for (const el of controls) {
				const r = el.getBoundingClientRect();
				if (r.width === 0 && r.height === 0) continue; // not shown
				if (r.height < target - 0.5 || r.width < target - 0.5)
					small.push(`${el.tagName.toLowerCase()} "${(el.textContent || el.getAttribute('aria-label') || el.id).trim().slice(0, 40)}" ${Math.round(r.width)}x${Math.round(r.height)} px`);
			}
			return { target, small, measured: controls.length };
		});
	}

	async function phoneIntegrations(page: Page) {
		const more = page.getByRole('dialog', { name: 'More' });
		await more.getByRole('link', { name: 'Integrations', exact: true }).tap();
		await expect(page.getByRole('heading', { level: 1, name: 'Integrations' })).toBeVisible();

		// Set up: a webhook to the test receiver, its selection chosen by touch.
		await page.getByRole('button', { name: 'New integration' }).tap();
		const form = page.locator('form.integration-form');
		await expect(form.getByRole('heading', { name: 'New integration' })).toBeVisible();
		await form.getByLabel('Name required', { exact: true }).fill(name);
		const field = form.getByRole('combobox', { name: 'Capcodes and aliases' });
		const options = form.getByRole('listbox', { name: 'Suggestions' }).getByRole('option');
		const chosen = form.getByRole('list', { name: 'Chosen capcodes and aliases' }).getByTestId('chosen');
		for (const [typed, option] of [
			['0100001', 'Station 1 (0100001)'],
			['Station 2', 'Station 2 (0100002)'],
			['Station 3', 'Station 3 (0100003)'],
			['0100009', '0100009']
		]) {
			await field.fill(typed);
			await expect(options.filter({ hasText: option })).toHaveCount(1);
			if (typed === '0100009') {
				// The suggestions, open: each a target for a finger, nothing sideways.
				const open = await tooSmall(form);
				expect(open.small, `suggestions open (target ${open.target} px)`).toEqual([]);
				expect(await sideways(page), 'suggestions open: no sideways scroll').toBe(0);
			}
			await options.filter({ hasText: option }).tap();
			await expect(field).toHaveValue('');
		}
		await expect(chosen).toHaveCount(4);
		// A chip comes off with a tap.
		await form.getByRole('button', { name: 'Remove Station 3 (0100003)' }).tap();
		await expect(chosen).toHaveText([/^Station 1 \(0100001\)/, /^Station 2 \(0100002\)/, /^0100009\s*in Stations block/]);
		const url = form.getByLabel('URL required', { exact: true });
		await expect(url).toHaveAttribute('type', 'url');
		await expect(url).toHaveAttribute('autocapitalize', 'off');
		await url.fill(`http://127.0.0.1:${port}/hook/PH0NES3CRET`);
		// The whole form at 375 px: no sideways scroll, every control a target
		// for a finger, and axe.
		expect(await sideways(page), 'the form: no sideways scroll').toBe(0);
		const sizes = await tooSmall(form);
		expect(sizes.small, `the form (target ${sizes.target} px)`).toEqual([]);
		await page.screenshot({ path: info.outputPath('a70-integration-form.png'), fullPage: true });
		expect(await axeOf(page), 'axe: the form at 375 px').toEqual([]);
		await form.getByRole('button', { name: 'Save integration' }).tap();
		await expect(page.getByRole('status').filter({ hasText: `Saved “${name}”` })).toBeVisible();
		let card = page.getByTestId('integration').filter({ hasText: name });
		await expect(card).toContainText('Station 1, Station 2, 0100009 (in Stations block)');
		await expect(page.locator('body')).not.toContainText('PH0NES3CRET'); // the URL is a secret once saved
		expect(await sideways(page), 'the list: no sideways scroll').toBe(0);

		// Edit: a new name, and one fewer alias.
		await card.getByRole('button', { name: 'Edit' }).tap();
		const edit = card.locator('form.integration-form');
		await expect(edit.getByRole('heading', { name: `Edit “${name}”` })).toBeVisible();
		await expect(edit.getByLabel('URL', { exact: true })).toHaveValue(''); // never shown again (R51)
		await edit.getByLabel('Name required', { exact: true }).fill(renamed);
		await edit.getByRole('button', { name: 'Remove Station 2 (0100002)' }).tap();
		expect(await sideways(page), 'the edit form: no sideways scroll').toBe(0);
		expect((await tooSmall(edit)).small, 'the edit form').toEqual([]);
		await edit.getByRole('button', { name: 'Save changes' }).tap();
		await expect(page.getByRole('status').filter({ hasText: `Saved “${renamed}”` })).toBeVisible();
		card = page.getByTestId('integration').filter({ hasText: renamed });
		await expect(card).toContainText('Station 1, 0100009 (in Stations block)');
		const saved = (await mine()).find((i) => i.name === renamed)!;
		expect(saved.aliases.map((a) => a.name)).toEqual(['Station 1']);
		expect(saved.capcodes).toEqual(['0100009']);

		// Send test (R51c): the receiver gets it, marked "test": true, and B sees the answer.
		await card.getByRole('button', { name: 'Send test' }).tap();
		const answer = card.getByTestId('test-answer');
		await expect(answer).toContainText('Delivered.');
		await expect(answer).toContainText('HTTP 200 OK');
		expect(received.at(-1)?.test).toBe(true);

		// Pause and resume with the switch (R81).
		const sw = page.getByRole('switch', { name: `${renamed}: sending` });
		await expect(sw).toHaveAttribute('aria-checked', 'true');
		await sw.tap();
		await expect(sw).toHaveAttribute('aria-checked', 'false');
		await expect(sw).toContainText('Paused');
		expect((await mine()).find((i) => i.id === saved.id)?.status).toBe('paused');
		await sw.tap();
		await expect(sw).toHaveAttribute('aria-checked', 'true');
		await expect(sw).toContainText('Active');
		expect((await mine()).find((i) => i.id === saved.id)?.status).toBe('active');

		// A large-event integration (R87d), set up the same way.
		await page.getByRole('button', { name: 'New integration' }).tap();
		await form.getByLabel('Name required', { exact: true }).fill(events);
		const large = form.getByRole('radio', { name: /^Large events: a summary/ });
		await large.tap();
		await expect(large).toBeChecked();
		await expect(form.getByRole('combobox', { name: 'Capcodes and aliases' })).toHaveCount(0);
		await expect(page.getByTestId('large-event-help')).toContainText('once for each large event, when the event opens');
		await form.getByLabel('URL required', { exact: true }).fill(`http://127.0.0.1:${port}/events`);
		expect(await sideways(page), 'the large-event form: no sideways scroll').toBe(0);
		expect((await tooSmall(form)).small, 'the large-event form').toEqual([]);
		await form.screenshot({ path: info.outputPath('a70-large-event-form.png') });
		await form.getByRole('button', { name: 'Save integration' }).tap();
		await expect(page.getByRole('status').filter({ hasText: `Saved “${events}”` })).toBeVisible();
		await expect(page.getByTestId('integration').filter({ hasText: events })).toContainText('Large events, each when it opens');

		// The list: no sideways scroll, and axe.
		expect(await sideways(page), 'the list: no sideways scroll').toBe(0);
		await page.screenshot({ path: info.outputPath('a70-integrations.png'), fullPage: true });
		expect(await axeOf(page), 'axe: the list at 375 px').toEqual([]);

		// Delete both.
		for (const n of [renamed, events]) {
			const c = page.getByTestId('integration').filter({ hasText: n });
			await c.getByRole('button', { name: 'Delete…' }).tap();
			await c.getByRole('button', { name: 'Yes, delete it' }).tap();
			await expect(c).toHaveCount(0);
		}
		expect((await mine()).filter((i) => i.name.endsWith(String(stamp)))).toEqual([]);
		console.log(
			`A70: at 375 px by touch, set up "${name}", edited it to "${renamed}", sent a test (receiver: test ${received.at(-1)?.test}), paused and resumed it with its switch, set up "${events}", deleted both; every control at least ${sizes.target} px (${sizes.measured} in the form), no sideways scroll`
		);
	}
});

// R77, R81 (owner, 2026-11-14): the build's version, just its tag ("v" and
// what the API reports, such as v1.9.0 or v1.9.1-dev+abc1234), at the very
// bottom of the side menu at 1,280 px, open and shrunk, and at the bottom of
// More at 375 px; muted and small; for signed-in users only. Axe passes.
test('R77 R81 the version at the foot of the menu', async ({ browser, request }, info) => {
	const meta = await (await request.get(`${URL}/api/v1/meta`, { headers: asRole('user') })).json();
	expect(typeof meta.version, 'meta reports the version').toBe('string');
	expect(meta.version).not.toBe('');
	const tag = `v${meta.version}`;
	const RELEASES = 'https://github.com/RIKIKU/PagerArchive/releases';
	const hrefOf = (v: string) => (/^\d+\.\d+\.\d+$/.test(v) ? `${RELEASES}/tag/v${v}` : RELEASES);
	// A link to the release's page on GitHub, or the list of releases for a
	// build that isn't one (owner, 2026-11-15); a new tab, no opener or referrer.
	const links = async (el: Locator, v: string) => {
		await expect(el).toHaveAttribute('href', hrefOf(v));
		await expect(el).toHaveAttribute('target', '_blank');
		await expect(el).toHaveAttribute('rel', 'noopener noreferrer');
		await expect(el).toHaveAccessibleName(`Release notes for v${v}`);
	};
	const looks = async (el: Locator, than: Locator) => {
		const [mine, theirs] = await Promise.all([el, than].map((l) => l.evaluate((e) => ({ size: parseFloat(getComputedStyle(e).fontSize), color: getComputedStyle(e).color }))));
		expect(mine.size, 'smaller than the menu').toBeLessThan(theirs.size);
		return mine.color;
	};
	const muted = (page: Page) =>
		page.evaluate(() => {
			const probe = document.createElement('span');
			probe.style.color = 'var(--muted)';
			document.body.append(probe);
			const c = getComputedStyle(probe).color;
			probe.remove();
			return c;
		});
	for (const role of ['user', 'admin'] as const) {
		// Desktop: the last thing in the side menu, below Shrink menu.
		const page = await (await signedIn(browser, role, { viewport: { width: 1280, height: 900 } })).newPage();
		await page.goto('/');
		await waitLive(page);
		const nav = page.getByRole('navigation', { name: 'Main' });
		const version = nav.getByRole('link', { name: VERSION_TAG });
		await expect(version).toHaveText(tag);
		await links(version, meta.version);
		const shrink = nav.getByRole('button', { name: 'Shrink menu' });
		const [v, s] = [await version.boundingBox(), await shrink.boundingBox()];
		expect(v!.y, 'below Shrink menu').toBeGreaterThanOrEqual(s!.y + s!.height);
		expect(await version.evaluate((e) => e.parentElement!.parentElement!.lastElementChild === e.parentElement && !e.closest('.foot')!.nextElementSibling), 'the very bottom').toBe(true);
		expect(await looks(version, nav.getByRole('link', { name: 'Messages', exact: true }))).toBe(await muted(page));
		await page.screenshot({ path: info.outputPath(`version-menu-${role}.png`) });
		expect(await axeOf(page), `${role}: axe, the menu at 1,280 px`).toEqual([]);
		// Shrunk: still there, inside the narrow menu.
		await shrink.click();
		await expect(version).toHaveText(tag);
		const [vs, ns] = [await version.boundingBox(), await nav.boundingBox()];
		expect(vs!.x + vs!.width, 'inside the shrunk menu').toBeLessThanOrEqual(ns!.x + ns!.width + 1);
		await page.screenshot({ path: info.outputPath(`version-shrunk-${role}.png`) });
		expect(await axeOf(page), `${role}: axe, the shrunk menu`).toEqual([]);
		await nav.getByRole('button', { name: 'Widen the menu' }).click();
		await page.context().close();

		// Phone: the bottom of More.
		const phone = await (await signedIn(browser, role, { viewport: { width: 375, height: 740 }, hasTouch: true })).newPage();
		await phone.goto('/');
		await waitLive(phone);
		await phone.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: 'More' }).click();
		const more = phone.getByRole('dialog', { name: 'More' });
		const inMore = more.getByRole('link', { name: VERSION_TAG });
		await expect(inMore).toHaveText(tag);
		await links(inMore, meta.version);
		expect(await inMore.evaluate((e) => e.parentElement!.parentElement!.lastElementChild === e.parentElement), 'the bottom of More').toBe(true);
		expect(await looks(inMore, more.getByRole('link', { name: 'Profile', exact: true }))).toBe(await muted(phone));
		expect(await phone.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'no sideways scroll').toBe(true);
		await phone.screenshot({ path: info.outputPath(`version-more-${role}.png`) });
		expect(await axeOf(phone), `${role}: axe, More at 375 px`).toEqual([]);
		await phone.context().close();
	}
	// A release and a dev build, whatever this build is: the API's answer stood in for.
	for (const v of ['1.10.0', '0.0.0-dev', '1.9.1-dev+abc1234']) {
		const page = await (await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } })).newPage();
		await page.route('**/api/v1/meta', async (r) => r.fulfill({ json: { ...(await (await r.fetch()).json()), version: v } }));
		await page.goto('/');
		const link = page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: `Release notes for v${v}` });
		await expect(link).toHaveText(`v${v}`);
		await links(link, v);
		await link.focus();
		await expect(link).toBeFocused();
		await page.context().close();
	}
	// Signed out: no menu, no version.
	const out = await (await newContext(browser)).newPage();
	await out.goto('/signin');
	await expect(out.getByRole('link', { name: VERSION_TAG })).toHaveCount(0);
	await out.context().close();
	console.log(`R77/R81: ${tag} at the foot of the side menu (open and shrunk) and of More, for the user and the admin; none signed out; axe clean`);
});

// A125 (R77b; owner, 2026-11-18, 2026-11-19): "New release! Check out the
// release log", for 8 seconds counted down by a line along its foot, then into the version's tag (More on a phone), which lights
// up. The server's answer is stood in for a new release, 1.10.0; whether a
// user is told once is A125's API half (internal/acceptance/a125_test.go).
const NEW_RELEASE = '1.10.0';
type Landing = { how: string; at: number; x: number; y: number; width: number; height: number; transform: string; target: string; box: { left: number; top: number; right: number; bottom: number } };

/** A page signed in as the user, told by the stand-in that NEW_RELEASE is new to them. */
async function newReleasePage(browser: Browser, width: number, opts: BrowserContextOptions = {}) {
	const ctx = await signedIn(browser, 'user', { viewport: { width, height: width < 768 ? 740 : 900 }, hasTouch: width < 768, ...opts });
	const page = await ctx.newPage();
	let asked = 0;
	await page.route('**/api/v1/me/release-seen', (r) => (asked++, r.fulfill({ json: { version: NEW_RELEASE, show: true } })));
	await page.route('**/api/v1/meta', async (r) => r.fulfill({ json: { ...(await (await r.fetch()).json()), version: NEW_RELEASE } }));
	// When it was shown, by the page's own clock.
	await page.addInitScript(() => {
		const w = window as unknown as { noticeShownAt?: number };
		new MutationObserver((_, obs) => {
			if (!document.querySelector('[data-testid="release-notice"]')) return;
			w.noticeShownAt = performance.now();
			obs.disconnect();
		}).observe(document, { childList: true, subtree: true });
	});
	await page.goto('/');
	await waitLive(page);
	const notice = page.getByTestId('release-notice');
	await expect(notice).toBeVisible();
	expect(asked, 'asked once').toBe(1);
	return { page, notice, shownAt: await page.evaluate(() => (window as unknown as { noticeShownAt: number }).noticeShownAt) };
}

/** Waits for the notice to go, and stops its leaving at its last moment to measure where it ended, then lets it finish. */
function landing(page: Page): Promise<Landing> {
	return page.evaluate(
		() =>
			new Promise<Landing>((resolve) => {
				const el = document.querySelector<HTMLElement>('[data-testid="release-notice"]')!;
				const measure = () => {
					const a = el.getAnimations()[0];
					a.pause();
					a.currentTime = Number(a.effect!.getComputedTiming().duration) - 1;
					const r = el.getBoundingClientRect();
					const to = [...document.querySelectorAll<HTMLElement>('[data-release-target]')].find((t) => t.getClientRects().length > 0)!;
					const b = to.getBoundingClientRect();
					const out = {
						how: el.dataset.leaving!,
						at: performance.now(),
						x: r.left + r.width / 2,
						y: r.top + r.height / 2,
						width: r.width,
						height: r.height,
						transform: getComputedStyle(el).transform,
						target: to.textContent!.trim(),
						box: { left: b.left, top: b.top, right: b.right, bottom: b.bottom }
					};
					a.play();
					resolve(out);
				};
				if (el.dataset.leaving) return measure();
				new MutationObserver((_, obs) => {
					if (!el.dataset.leaving) return;
					obs.disconnect();
					measure();
				}).observe(el, { attributes: true, attributeFilter: ['data-leaving'] });
			})
	);
}

/** How much of its line the countdown still shows (1 when shown, 0 at the end). */
function countdownLeft(page: Page) {
	return page.getByTestId('release-countdown').evaluate((el) => new DOMMatrix(getComputedStyle(el).transform).a);
}

/** Its line shrinks over a moment. */
async function counting(page: Page, what: string) {
	const before = await countdownLeft(page);
	await page.waitForTimeout(500);
	const after = await countdownLeft(page);
	expect(after, `${what}: the countdown line shrinks`).toBeLessThan(before - 0.02);
	return after;
}

/** Held for longer than it would have been shown: still there, its line no shorter. */
async function heldFor(page: Page, what: string) {
	const before = await countdownLeft(page);
	await page.waitForTimeout(8_500);
	await expect(page.getByTestId('release-notice'), what).toBeVisible();
	expect(Math.abs((await countdownLeft(page)) - before), `${what}: its line waits too`).toBeLessThan(0.01);
}

/** It ended in the middle of what it lands on, no bigger than it, and that lit up. */
async function landedOn(page: Page, l: Landing, target: string) {
	expect(l.how, 'it flew').toBe('fly');
	expect(l.target).toBe(target);
	const near = 2;
	expect(l.x, 'over the target').toBeGreaterThanOrEqual(l.box.left - near);
	expect(l.x).toBeLessThanOrEqual(l.box.right + near);
	expect(l.y).toBeGreaterThanOrEqual(l.box.top - near);
	expect(l.y).toBeLessThanOrEqual(l.box.bottom + near);
	expect(l.height, 'shrunk to the target').toBeLessThanOrEqual(l.box.bottom - l.box.top + near);
	await expect(page.getByTestId('release-notice')).toHaveCount(0);
	const lit = page.locator('.release-landed');
	await expect(lit).toHaveCount(1);
	expect((await lit.textContent())!.trim()).toBe(target);
}

for (const width of [1280, 375]) {
	test(`A125 a new release told, then into the version at ${width} px`, async ({ browser, request }, info) => {
		test.setTimeout(180_000);
		const report: string[] = [];
		const target = width < 768 ? 'More' : `v${NEW_RELEASE}`;
		// Shown: the words, the link, announced politely, focus left alone.
		{
			const { page, notice, shownAt } = await newReleasePage(browser, width);
			const landed = landing(page);
			await expect(notice).toHaveText('New release! Check out the release log');
			const link = notice.getByRole('link', { name: 'Check out the release log' });
			await expect(link).toHaveAttribute('href', `https://github.com/RIKIKU/PagerArchive/releases/tag/v${NEW_RELEASE}`);
			await expect(link).toHaveAttribute('target', '_blank');
			await expect(link).toHaveAttribute('rel', 'noopener noreferrer');
			expect(await link.evaluate((a) => (a as HTMLAnchorElement).tabIndex), 'reached with Tab').toBe(0);
			expect(await notice.evaluate((n) => n.closest('[aria-live]')?.getAttribute('aria-live')), 'a polite live region').toBe('polite');
			expect(await notice.evaluate((n) => n.closest('[role="alert"], [aria-live="assertive"]')), 'not an alert').toBeNull();
			expect(await notice.evaluate((n) => !n.contains(document.activeElement)), 'focus not moved to it').toBe(true);
			await expect(page.getByTestId('release-countdown'), 'a countdown line along its foot').toBeVisible();
			const shown = await counting(page, 'shown');
			// After 8 seconds, into the tag (More on a phone).
			const l = await landed;
			const after = l.at - shownAt;
			expect(after, 'it stays 8 seconds').toBeGreaterThanOrEqual(7_950);
			expect(after).toBeLessThan(9_000);
			report.push(`its line at ${shown.toFixed(2)} after a moment`);
			await landedOn(page, l, target);
			report.push(`left after ${Math.round(after)} ms into ${target}`);
			await page.context().close();
		}
		// Held while the pointer is on it: light and dark, nothing sideways, it
		// covers none of the menu or the bottom bar, and axe passes. Then Esc
		// sends it at once.
		{
			const { page, notice } = await newReleasePage(browser, width);
			const b = (await notice.boundingBox())!;
			await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2);
			const frame = page.locator(width < 768 ? 'nav.bottom' : 'nav.side');
			for (const colorScheme of ['light', 'dark'] as const) {
				await page.emulateMedia({ colorScheme });
				const [n, f] = [(await notice.boundingBox())!, (await frame.boundingBox())!];
				const apart = n.x >= f.x + f.width || n.x + n.width <= f.x || n.y >= f.y + f.height || n.y + n.height <= f.y;
				expect(apart, `${width} px, ${colorScheme}: covers none of the menu or bottom bar`).toBe(true);
				expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'no sideways scroll').toBe(true);
				await page.screenshot({ path: info.outputPath(`release-notice-${width}-${colorScheme}.png`) });
				expect(await axeOf(page), `${width} px, ${colorScheme}: axe with the notice shown`).toEqual([]);
			}
			await heldFor(page, 'held while the pointer is on it');
			const landed = landing(page);
			const sent = Date.now();
			await page.keyboard.press('Escape');
			await landedOn(page, await landed, target);
			expect(Date.now() - sent, 'Esc sends it at once').toBeLessThan(2_000);
			await page.context().close();
		}
		// A click on it sends it at once too.
		{
			const { page, notice } = await newReleasePage(browser, width);
			const landed = landing(page);
			const sent = Date.now();
			await notice.click({ position: { x: 4, y: 4 } });
			await landedOn(page, await landed, target);
			expect(Date.now() - sent, 'a click sends it at once').toBeLessThan(2_000);
			await page.context().close();
		}
		// On a phone: a finger held on it holds it, and lifting it is a tap, which
		// sends it at once (owner, 2026-11-19: accepted). Rows of the list lie
		// behind it, so a tap that went to them (WebKit moves a tap to the
		// nearest thing that takes clicks) would open a message instead.
		if (width < 768) {
			const job = uniqueJob();
			for (let i = 0; i < 8; i++) await post(request, '0100001', `@@${job} A125 ROW BEHIND THE NOTICE ${i}`);
			const { page, notice } = await newReleasePage(browser, width);
			const touch = { pointerType: 'touch', isPrimary: true, pointerId: 2 };
			for (const type of ['pointerover', 'pointerenter', 'pointerdown']) await notice.dispatchEvent(type, touch);
			await heldFor(page, 'held while a finger is on it');
			const landed = landing(page);
			const sent = Date.now();
			await notice.tap({ position: { x: 4, y: 4 } });
			await landedOn(page, await landed, target);
			expect(Date.now() - sent, 'a tap sends it at once').toBeLessThan(2_000);
			expect(await page.evaluate(() => location.pathname),'the tap opened nothing behind it').toBe('/');
			await page.context().close();
		}
		// Reduced motion: a quick fade where it is, no flight; the tag still lights up.
		{
			const { page, notice } = await newReleasePage(browser, width, { reducedMotion: 'reduce' });
			const start = (await notice.boundingBox())!;
			await counting(page, 'reduced motion');
			const l = await landing(page);
			expect(l.how, 'it fades').toBe('fade');
			expect(l.transform, 'it moves nowhere').toBe('none');
			expect(Math.abs(l.x - (start.x + start.width / 2)), 'where it was').toBeLessThanOrEqual(1);
			expect(Math.abs(l.y - (start.y + start.height / 2))).toBeLessThanOrEqual(1);
			await expect(notice).toHaveCount(0);
			await expect(page.locator('.release-landed')).toHaveText(target);
			await page.context().close();
		}
		// Keyboard: held while focus is in it; Esc then leaves focus where it landed.
		{
			const { page, notice } = await newReleasePage(browser, width);
			await notice.getByRole('link').focus();
			await heldFor(page, 'held while focus is in it');
			await page.keyboard.press('Escape');
			await expect(notice).toHaveCount(0);
			const nav = page.getByRole('navigation', { name: 'Main' });
			await expect(width < 768 ? nav.getByRole('button', { name: 'More' }) : nav.getByRole('link', { name: VERSION_TAG })).toBeFocused();
			await page.context().close();
		}
		console.log(`A125 at ${width} px: ${report.join('; ')}; held by pointer, focus${width < 768 ? ' and finger' : ''}, its line waiting too; Esc, a click${width < 768 ? ' and a tap' : ''} send it; reduced motion fades, its line still counting; axe clean, light and dark`);
	});
}

// A125, R77b (owner, 2026-11-19: "It just needs to contrast"; WCAG AA for what
// must be noticed): the shown notice's own colours, as the browser draws them,
// in every look and theme: its text and link at least 4.5:1 on its fill, its
// edge at least 4.5:1 against the page; and the landing's ring at least 3:1
// against the menu (the bottom bar on a phone) it sits on.
test('A125 the notice and its landing contrast in every look and theme', async ({ browser, request }) => {
	test.setTimeout(120_000);
	const report: string[] = [];
	const failures: string[] = [];
	const rgb = (c: string) => c.match(/rgba?\(([^)]+)\)/)![1].split(',').slice(0, 3).map(Number);
	const lum = (c: number[]) => c.map((v) => v / 255).map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)).reduce((s, v, i) => s + v * [0.2126, 0.7152, 0.0722][i], 0);
	const ratio = (a: string, b: string) => {
		const [x, y] = [lum(rgb(a)), lum(rgb(b))].sort((p, q) => q - p);
		return (x + 0.05) / (y + 0.05);
	};
	for (const look of ['harbour', 'console'] as const) {
		await setLook(request, 'user', look);
		try {
			for (const colorScheme of ['light', 'dark'] as const) {
				for (const width of [1280, 375]) {
					const { page, notice } = await newReleasePage(browser, width, { colorScheme });
					await expect(page.locator('html')).toHaveAttribute('data-look', look);
					const c = await notice.evaluate((n) => {
						const s = getComputedStyle(n);
						return {
							fill: s.backgroundColor,
							text: s.color,
							link: getComputedStyle(n.querySelector('a')!).color,
							edge: s.borderTopColor,
							line: getComputedStyle(n.querySelector('[data-testid="release-countdown"]')!).backgroundColor,
							page: getComputedStyle(document.body).backgroundColor
						};
					});
					// The landing, held at its brightest.
					await page.keyboard.press('Escape');
					const lit = page.locator('.release-landed');
					await expect(lit).toHaveCount(1);
					const frame = width < 768 ? 'nav.bottom' : 'nav.side';
					const l = await lit.evaluate((el, frame) => {
						for (const a of el.getAnimations()) (a.pause(), (a.currentTime = 0));
						const s = getComputedStyle(el);
						return { ring: s.boxShadow, fill: s.backgroundColor, text: s.color, on: getComputedStyle(document.querySelector(frame)!).backgroundColor };
					}, frame);
					const checks = [
						{ what: 'text on its fill', r: ratio(c.text, c.fill), min: 4.5 },
						{ what: 'link on its fill', r: ratio(c.link, c.fill), min: 4.5 },
						{ what: 'edge against the page', r: ratio(c.edge, c.page), min: 4.5 },
						{ what: 'countdown line on its fill', r: ratio(c.line, c.fill), min: 3 },
						{ what: `landing ring against ${width < 768 ? 'the bottom bar' : 'the side menu'}`, r: ratio(l.ring, l.on), min: 3 },
						{ what: 'landing text on its fill', r: ratio(l.text, l.fill), min: 4.5 }
					];
					const where = `${look === 'console' ? 'Console' : 'Harbour'}, ${colorScheme}, ${width} px`;
					report.push(`${where}: ${checks.map((k) => `${k.what} ${k.r.toFixed(2)}:1`).join(', ')}`);
					for (const k of checks) if (k.r < k.min) failures.push(`${where}: ${k.what} is ${k.r.toFixed(2)}:1, needs ${k.min}:1`);
					await page.context().close();
				}
			}
		} finally {
			await setLook(request, 'user', 'harbour');
		}
	}
	console.log(report.join('\n'));
	expect(failures).toEqual([]);
});

// The server says whether to show the notice, and a build that isn't a release
// never is shown and records nothing (A125's API half, for 0.0.0-dev and
// 1.11.0-dev+abc1234: internal/acceptance/a125_test.go). The API under test
// runs this build's version: 0.0.0-dev unstamped, <next>-dev+<commit> in a CI
// run, but in the Release workflow's run the release's own (ADR 0024: what's
// tested is what's published). So the app is given each kind of build that
// isn't a release here, whatever this build is, and the real API's own answer
// is checked when this build isn't a release.
test('A125 no notice for a build that is not a release', async ({ browser, request }) => {
	const noNotice = async (page: Page, version: string) => {
		const asked = page.waitForResponse('**/api/v1/me/release-seen');
		await page.goto('/');
		await waitLive(page);
		expect(await (await asked).json(), `${version}: the answer`).toEqual({ version, show: false });
		await expect(page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: VERSION_TAG })).toHaveText(`v${version}`);
		await page.waitForTimeout(500);
		await expect(page.getByTestId('release-notice'), `${version}: no notice`).toHaveCount(0);
	};
	for (const version of ['0.0.0-dev', '1.11.0-dev+abc1234']) {
		const page = await (await signedIn(browser, 'user')).newPage();
		await page.route('**/api/v1/meta', async (r) => r.fulfill({ json: { ...(await (await r.fetch()).json()), version } }));
		await page.route('**/api/v1/me/release-seen', (r) => r.fulfill({ json: { version, show: false } }));
		await noNotice(page, version);
		await page.context().close();
	}
	const meta = await (await request.get(`${URL}/api/v1/meta`, { headers: asRole('user') })).json();
	if (/^\d+\.\d+\.\d+$/.test(meta.version)) {
		test.info().annotations.push({ type: 'A125', description: `this build is the release ${meta.version}: the real API's answer for a build that isn't one isn't checked here` });
		return;
	}
	const page = await (await signedIn(browser, 'user')).newPage();
	await noNotice(page, meta.version);
	await page.context().close();
});

// An admin's alert is the --warn-bg fill only, with no edge (owner,
// 2026-11-20: "The amber edge is very intense. I prefer it without.";
// ArchiveUx 0.1.1, ADR 0049). Its edge as drawn, each side's width and style.
const noEdge = ['0px none', '0px none', '0px none', '0px none'];
async function alertEdge(alert: Locator) {
	return alert.evaluate((a) => {
		const s = getComputedStyle(a);
		return ['top', 'right', 'bottom', 'left'].map((k) => `${s.getPropertyValue(`border-${k}-width`)} ${s.getPropertyValue(`border-${k}-style`)}`);
	});
}

// WCAG 2.1 AA: 4.5:1 for text (1.4.3), 3:1 for focus rings, control borders
// and the marks that show priority (1.4.11), each token as the browser
// resolves it, in each look (R82d), light and dark. Priorities carry their
// names in words, so none relies on colour alone (1.4.1).
const colourTokens = [
	...['bg', 'surface', 'surface-2', 'fg', 'muted', 'border', 'control-border', 'accent', 'accent-fg', 'accent-hover', 'accent-pressed', 'accent-soft', 'accent-soft-fg'],
	...['hover', 'focus', 'warn-bg', 'warn-fg', 'ok-fg', 'danger', 'danger-fg', 'danger-hover', 'danger-text', 'heartbeat-fg', 'highlight'],
	...['notice-edge', ...[0, 1, 2, 3, 4].flatMap((n) => [`heat-${n}`, `heat-${n}-fg`])],
	...['emergency', 'nonemergency', 'admin'].flatMap((p) => ['bg', 'edge', 'dot', 'row', 'row-hover', 'badge-bg', 'badge-fg', 'badge-border'].map((t) => `${p}-${t}`))
];
test('M13, M15: WCAG AA contrast of every look, light and dark', async ({ browser, request }) => {
	const report: string[] = [];
	const failures: string[] = [];
	for (const look of ['harbour', 'console'] as const) {
		await setLook(request, 'user', look);
		try {
			for (const scheme of ['light', 'dark'] as const) {
				const page = await (await signedIn(browser, 'user', { colorScheme: scheme })).newPage();
				await page.goto('/');
				await waitLive(page);
				await expect(page.locator('html')).toHaveAttribute('data-look', look);
				// Whether the look draws a dot in its labels at all.
				const dots = await page.evaluate(() => getComputedStyle(document.documentElement).getPropertyValue('--badge-dot').trim() !== 'none');
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
				// rgb(…) in 0-255, or color(srgb …) in 0-1 (a color-mix() result).
				const rgb = (n: string) =>
					t[n]
						.replace(/^color\(srgb/, '')
						.match(/[\d.]+/g)!
						.slice(0, 3)
						.map((v) => (t[n].startsWith('color(srgb') ? Number(v) * 255 : Number(v)));
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
					{ what: 'Switch on', fg: 'accent', on: ['surface-2'], min: 3 },
					// Must-notice: an edge at least 4.5:1 against the page (owner, 2026-11-19).
					{ what: 'Release notice edge', fg: 'notice-edge', on: ['bg'], min: 4.5 },
					// ArchiveUx's heat rule: each step's count on its own shade.
					...[0, 1, 2, 3, 4].map((n) => ({ what: `Heat grid step ${n}: its count`, fg: `heat-${n}-fg`, on: [`heat-${n}`], min: 4.5 }))
				];
				for (const p of ['emergency', 'nonemergency', 'admin']) {
					const rows = on([`${p}-row`, `${p}-row-hover`]);
					pairs.push(
						{ what: `${p}: label`, fg: `${p}-badge-fg`, on: on([`${p}-badge-bg`]), min: 4.5 },
						{ what: `${p}: map marker`, fg: `${p}-bg`, on: ['bg', 'surface'], min: 3 },
						{ what: `${p}: text on its row`, fg: 'fg', on: rows, min: 4.5 },
						{ what: `${p}: secondary text on its row`, fg: 'muted', on: rows, min: 4.5 },
						{ what: `${p}: links on its row`, fg: 'accent', on: rows, min: 4.5 }
					);
					if (t[`${p}-edge`] !== 'transparent') pairs.push({ what: `${p}: row edge`, fg: `${p}-edge`, on: rows, min: 3 });
					if (dots) pairs.push({ what: `${p}: dot`, fg: `${p}-dot`, on: on([`${p}-badge-bg`]), min: 3 });
					if (t[`${p}-badge-border`] !== 'transparent') pairs.push({ what: `${p}: label border`, fg: `${p}-badge-border`, on: rows, min: 3 });
				}
				report.push(`${look === 'console' ? 'Console' : 'Harbour'}, ${scheme}`);
				for (const p of pairs) {
					const each = p.on.map((b) => ({ b, r: ratio(p.fg, b) }));
					const worst = each.reduce((w, e) => (e.r < w.r ? e : w));
					report.push(`  ${p.what}: ${worst.r.toFixed(2)}:1 at worst (--${p.fg} on --${worst.b}; needs ${p.min}:1) [${each.map((e) => `${e.b} ${e.r.toFixed(2)}`).join(', ')}]`);
					if (worst.r < p.min) failures.push(`${look}, ${scheme}: ${p.what}: --${p.fg} on --${worst.b} is ${worst.r.toFixed(2)}:1, needs ${p.min}:1`);
				}
				await page.context().close();
			}
		} finally {
			await setLook(request, 'user', 'harbour');
		}
	}
	console.log(report.join('\n'));
	expect(failures).toEqual([]);
});

// A74. Choosing a look (R82d, ADR 0028). Given user B on Harbour, B chooses
// Console in the account menu on a PC, and the page changes without
// reloading; B then signs in on a phone (a new session, in a new browser in
// dark mode), and its first paint is already Console; user C still sees
// Harbour. axe and contrast in both looks, light and dark, are the all-views
// check and the contrast check above.
test('A74 choosing a look', async ({ browser, request }, info) => {
	test.setTimeout(120_000);
	const phoneToken = `e2e-a74-${info.project.name}-${Date.now()}`;
	await setLook(request, 'user', 'harbour');
	await setLook(request, 'userC', 'harbour');
	try {
		// B on a PC, on Harbour.
		const pc = await (await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } })).newPage();
		await pc.goto('/');
		await waitLive(pc);
		const root = pc.locator('html');
		await expect(root).toHaveAttribute('data-look', 'harbour');
		const menu = pc.getByRole('button', { name: /^Account menu/ });
		const drawn = async () => ({
			corner: await menu.evaluate((b) => getComputedStyle(b).borderTopLeftRadius),
			font: await pc.evaluate(() => getComputedStyle(document.body).fontFamily),
			bg: await pc.evaluate(() => getComputedStyle(document.body).backgroundColor)
		});
		const harbourDrawn = await drawn();
		// Gone if the page reloads.
		await pc.evaluate(() => ((window as unknown as { a74: boolean }).a74 = true));
		await menu.click();
		const choice = pc.getByRole('combobox', { name: 'Look', exact: true });
		await expect(choice).toHaveValue('harbour');
		await choice.selectOption('console');
		await expect(root).toHaveAttribute('data-look', 'console');
		const consoleDrawn = await drawn();
		expect(harbourDrawn.corner).toBe('10px');
		expect(consoleDrawn.corner).toBe('2px');
		expect(consoleDrawn.font).not.toBe(harbourDrawn.font);
		expect(consoleDrawn.bg).toBe('rgb(255, 255, 255)');
		expect(await pc.evaluate(() => (window as unknown as { a74?: boolean }).a74), 'no reload').toBe(true);
		// Saved on the account.
		await expect
			.poll(async () => (await (await request.get(`${URL}/api/v1/me`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } })).json()).look)
			.toBe('console');
		expect(await axeOf(pc), 'axe: the account menu, Console').toEqual([]);
		await pc.context().close();

		// B signs in on a phone: a new session, in a new browser, in dark mode.
		sql(
			`INSERT INTO sessions (token_hash, user_id, expires_at) SELECT sha256(convert_to('${phoneToken}', 'UTF8')), id, now() + interval '1 day' FROM users WHERE subject = 'e2e-user';`
		);
		// The page comes from the server already in Console...
		const sent = await (await request.get(`${URL}/`, { headers: { Cookie: `pa_session=${phoneToken}` } })).text();
		expect(sent).toMatch(/<html data-look="console"/);
		const phoneCtx = await newContext(browser, { viewport: { width: 375, height: 812 }, hasTouch: true, colorScheme: 'dark' });
		await phoneCtx.addCookies([{ name: 'pa_session', value: phoneToken, url: URL }]);
		// ...so the first frame is Console, and the look never changes after:
		// any change of data-look on the root is recorded.
		await phoneCtx.addInitScript(() => {
			const w = window as unknown as { looks: (string | null)[]; first?: string | null };
			w.looks = [];
			new MutationObserver((rs) => {
				for (const r of rs) w.looks.push((r.target as Element).getAttribute('data-look'));
			}).observe(document, { subtree: true, attributes: true, attributeFilter: ['data-look'] });
			requestAnimationFrame(() => (w.first = document.documentElement.getAttribute('data-look')));
		});
		const phone = await phoneCtx.newPage();
		await phone.goto('/');
		await waitLive(phone);
		const seen = await phone.evaluate(() => {
			const w = window as unknown as { looks: (string | null)[]; first?: string | null };
			return { looks: w.looks, first: w.first };
		});
		expect(seen.first, 'the first frame').toBe('console');
		expect(seen.looks.filter((l) => l !== 'console'), 'the look after the first frame').toEqual([]);
		expect(await phone.evaluate(() => getComputedStyle(document.body).backgroundColor), 'Console dark').toBe('rgb(20, 20, 20)');
		await phone.getByRole('button', { name: /^Account menu/ }).click();
		await expect(phone.getByRole('combobox', { name: 'Look', exact: true })).toHaveValue('console');
		await phoneCtx.close();

		// C signs in: still Harbour.
		const c = await (await signedIn(browser, 'userC', { colorScheme: 'dark' })).newPage();
		await c.goto('/');
		await waitLive(c);
		await expect(c.locator('html')).toHaveAttribute('data-look', 'harbour');
		expect(await c.evaluate(() => getComputedStyle(document.body).backgroundColor), 'Harbour dark').toBe('rgb(15, 22, 20)');
		await c.context().close();
	} finally {
		await setLook(request, 'user', 'harbour');
		sql(`DELETE FROM sessions WHERE token_hash = sha256(convert_to('${phoneToken}', 'UTF8'));`);
	}
	console.log(`A74 (${info.project.name}): Console chosen on a PC without reloading; a new phone session's page sent and first drawn in Console (dark); C on Harbour`);
});

// The evidence for M15 (ADR 0028): with PA_M15_SHOTS set to a folder,
// Console, light and dark, at 1,280 and 375 px: the message list, a message
// open, the map, Administration, and the account menu with the choice.
// Chromium only.
test('M15 screenshots', async ({ browser, request }, info) => {
	const dir = process.env.PA_M15_SHOTS;
	test.skip(!dir || info.project.name !== 'chromium', 'only with PA_M15_SHOTS set, in Chromium');
	test.setTimeout(300_000);
	// One page of each kind, the newest last: made-up pages (SPEC.md section 10).
	const pages: [string, string][] = [
		['0000050', 'Network heartbeat 000123'],
		['0100003', 'QDPAGER TEST, PLEASE IGNORE'],
		['0100002', `Hb${uniqueJob()} CREWS RETURNING #STN2`],
		['0100002', `@@${uniqueJob()} GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2`],
		['0100001', `@@${uniqueJob()} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1`]
	];
	for (const [capcode, text] of pages) await post(request, capcode, text);
	const shot = async (page: Page, name: string) => page.screenshot({ path: join(dir!, `${name}.png`) });
	await setLooks(request, 'console');
	try {
		for (const scheme of ['light', 'dark'] as const) {
			for (const width of [1280, 375]) {
				const size = { viewport: { width, height: width === 375 ? 812 : 900 }, colorScheme: scheme, hasTouch: width === 375 };
				const page = await (await signedIn(browser, 'admin', size)).newPage();
				const base = `console-${scheme}-${width}`;
				await page.goto('/');
				await waitLive(page);
				await expect(page.locator('html')).toHaveAttribute('data-look', 'console');
				await shot(page, `${base}-list`);
				await page.getByTestId('message-list').locator('article a.open').first().click();
				await expect(page.getByRole('heading', { name: 'Received by' })).toBeVisible();
				await expect(page.getByTestId('map')).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
				await page.waitForTimeout(800);
				await shot(page, `${base}-message`);
				await page.goto('/map');
				await expect(page.getByTestId('map')).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
				await page.waitForTimeout(800);
				await shot(page, `${base}-map`);
				await page.goto('/admin');
				await expect(page.getByRole('heading', { level: 1, name: 'Administration' })).toBeVisible();
				await shot(page, `${base}-administration`);
				await page.goto('/');
				await waitLive(page);
				await page.getByRole('button', { name: /^Account menu/ }).click();
				await expect(page.getByRole('combobox', { name: 'Look', exact: true })).toHaveValue('console');
				await shot(page, `${base}-account-menu`);
				await page.context().close();
			}
		}
	} finally {
		await setLooks(request, 'harbour');
	}
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
	// Newer messages are still there above: the button, or (as the list loads
	// newer pages early, 3 screens ahead) rows newer than the day already in.
	await expect
		.poll(() =>
			page.evaluate(() => {
				if ([...document.querySelectorAll('button')].some((b) => b.textContent?.trim() === 'Load newer messages')) return true;
				const first = document.querySelector('[data-testid="message-list"] article p.text')?.textContent ?? '';
				return first !== '' && !first.includes('DAY 20');
			})
		)
		.toBe(true);

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
		// Wide enough to find with a thumb, hard against the edge, and the page's
		// own scroll bar hidden while the tab stands in for it (owner, 2026-11-13).
		expect((await tab.locator('.grip').boundingBox())!.width, 'the tab as drawn').toBeGreaterThanOrEqual(50);
		expect(Math.abs(box.x + box.width - width), 'against the edge').toBeLessThanOrEqual(1);
		// The rail: the last 7 days on a phone, 30 on a tablet, whole days.
		await expect(scrubber.locator('.tab-place')).toHaveAttribute('data-steps', width < 768 ? '7' : '30');
		expect(await page.evaluate(() => getComputedStyle(document.documentElement).scrollbarWidth)).toBe('none');
		await page.screenshot({ path: info.outputPath(`touch-${width}-shown.png`) });
		expect(await axeOf(page), `axe with the tab at ${width} px`).toEqual([]);
		const before = await firstInView(page);
		// Tap and drag the tab down in small moves (4 px, as a thumb creeps), while
		// the list jumps and loads under it: the tab stays under the finger, and
		// the days in the bubble go steadily older, never back (owner, 2026-11-13:
		// it "bounces around under your thumb").
		const x = box.x + box.width / 2;
		const y0 = box.y + box.height / 2;
		const touch = { pointerId: 7, pointerType: 'touch', isPrimary: true, button: 0, buttons: 1, clientX: x };
		const bubble = scrubber.locator('.tab-bubble');
		const tabY = async () => {
			const b = (await tab.boundingBox())!;
			return b.y + b.height / 2;
		};
		await tab.dispatchEvent('pointerdown', { ...touch, clientY: y0 });
		const days: string[] = [];
		const off: number[] = [];
		// As far as the rail goes: past its end the tab stops and the finger goes on.
		const rail = (await scrubber.boundingBox())!;
		const room = Math.floor((rail.y + rail.height - (box.y + box.height)) / 4) - 1;
		for (let k = 1; k <= Math.min(60, room); k++) {
			const y = y0 + 4 * k;
			await tab.dispatchEvent('pointermove', { ...touch, clientY: y });
			await page.waitForTimeout(25);
			off.push(Math.abs((await tabY()) - y));
			if (await bubble.isVisible()) {
				const d = (await bubble.textContent())!.trim();
				if (days.at(-1) !== d) days.push(d);
			}
		}
		console.log(`touch ${width}: the bubble went ${days.join(' → ')}; the tab ${Math.max(...off).toFixed(1)} px from the finger at most `);
		expect(Math.max(...off), 'the tab stays under the finger').toBeLessThanOrEqual(1.5);
		expect(days.length, 'several days passed').toBeGreaterThanOrEqual(width < 768 ? 2 : 5);
		expect(new Set(days).size, 'no day came back: no flicker').toBe(days.length);
		const ms = (d: string) => Date.parse(`${d.replace(/^\w+,? /, '')} ${new Date().getFullYear()}`);
		for (let k = 1; k < days.length; k++) expect(ms(days[k]), `${days[k]} is older than ${days[k - 1]}`).toBeLessThan(ms(days[k - 1]));
		const day = days.at(-1)!;
		expect(day, 'the day, as "Fri, 23 Oct"').toMatch(/^\w{3},? \d{1,2} \w{3}$/);
		// While the finger is down the list has already moved there.
		await expect.poll(() => firstInView(page)).not.toBe(before);
		await page.screenshot({ path: info.outputPath(`touch-${width}-dragging.png`) });
		await tab.dispatchEvent('pointerup', { ...touch, buttons: 0, clientY: y0 + 240 });
		const first = page.getByTestId('message-list').locator('article').filter({ hasText: `TOUCH ${n} DAY` });
		await expect
			.poll(async () => {
				const below = await page.evaluate(() => document.querySelector('header.top')!.getBoundingClientRect().bottom);
				// (A row showing 4 px or less under the top bar is out of view: WebKit leaves a sliver.)
				for (const r of await first.all()) {
					const b = (await r.boundingBox())!;
					if (b.y + b.height > below + 4) return (await r.locator('time .date').textContent())!.trim().split(' ').slice(0, 2).join(' ');
				}
				return '';
			})
			.toBe(day.replace(',', '').split(' ').slice(1).join(' '));
		// It fades once the scrolling has stopped.
		await expect(scrubber).toHaveAttribute('data-shown', 'false', { timeout: 5000 });
		if (width < 768) {
			// The rail stops at 7 days; the list doesn't: scrolled on past them, the
			// tab waits at the bottom of the rail.
			for (let k = 0; k < 6; k++) await page.evaluate(() => window.scrollBy(0, 2500));
			await expect.poll(() => firstInView(page)).toMatch(/DAY (9|[1-9]\d) /);
			await expect.poll(async () => Math.round(rail.y + rail.height - ((await tab.boundingBox())!.y + (await tab.boundingBox())!.height))).toBeLessThanOrEqual(1);
			// It says where the list is.
			await expect(tab).toHaveAttribute('aria-valuetext', /^Earlier: \w{3},? \d{1,2} \w{3}$/);
			await page.screenshot({ path: info.outputPath(`touch-${width}-past-the-rail.png`) });
		}
		expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'no horizontal scroll').toBe(true);
		await ctx.close();
	}
});

// The scrubber owns the right edge (owner, 2026-11-13): on a page with one,
// the page's own scroll bar is hidden and the edge zone reaches the window's
// edge; the wheel and the keys still scroll; a page without one keeps its
// scroll bar; nothing moves sideways between them; no sideways scroll.
test('Scrubber owns the right edge', async ({ browser }, info) => {
	test.setTimeout(120_000);
	const n = projectN(test.info());
	const capcode = `07300${n}1`;
	const alias = `Edge ${n}`;
	sql(`
INSERT INTO aliases (pattern, name, template_id) SELECT '${capcode}', '${alias}', id FROM templates WHERE name = 'Example Fire' ON CONFLICT DO NOTHING;
DELETE FROM messages WHERE capcode = '${capcode}';
INSERT INTO messages (capcode, text, priority, sent_at, dedup_key, alias_id)
SELECT '${capcode}', 'QDEDGE ${n} PAGE ' || g.i, 'Admin', now() - g.i * interval '3 hours',
       sha256(convert_to('edge ${n} ' || g.i, 'UTF8')), (SELECT id FROM aliases WHERE pattern = '${capcode}')
FROM generate_series(1, 120) AS g (i);`);
	const scrollbar = (p: Page) => p.evaluate(() => getComputedStyle(document.documentElement).scrollbarWidth);
	const frame = (p: Page) =>
		p.evaluate(() => ({
			top: Math.round(document.querySelector('header.top')!.getBoundingClientRect().right),
			sideways: document.documentElement.scrollWidth > document.documentElement.clientWidth
		}));
	for (const width of [1280, 1440]) {
		const page = await (await signedIn(browser, 'user', { viewport: { width, height: 900 } })).newPage();
		await page.goto('/account/profile');
		await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
		// (Firefox under test reports every scroll bar as none, so the page's own rule is what's checked.)
		expect(await page.evaluate(() => document.documentElement.classList.contains('scrubber-page')), 'a page without a scrubber keeps its scroll bar').toBe(false);
		const plain = await frame(page);
		await page.screenshot({ path: info.outputPath(`edge-${width}-profile.png`) });
		await page.goto(`/?alias=${encodeURIComponent(`"${alias}"`)}`);
		await waitLive(page);
		await expect(page.getByTestId('scrubber').locator('.mark').first()).toBeAttached();
		expect(await scrollbar(page), 'no scroll bar beside the scrubber').toBe('none');
		const listed = await frame(page);
		expect(listed.top, 'nothing moved sideways').toBe(plain.top);
		expect(listed.sideways || plain.sideways, 'no sideways scroll').toBe(false);
		// The wheel and Page Down still scroll.
		await page.mouse.move(width / 2, 500);
		await page.mouse.wheel(0, 600);
		await expect.poll(() => page.evaluate(() => scrollY)).toBeGreaterThan(300);
		const y = await page.evaluate(() => scrollY);
		await page.locator('body').press('PageDown');
		await expect.poll(() => page.evaluate(() => scrollY)).toBeGreaterThan(y + 200);
		// Hidden until the pointer comes within 50 px of the window's edge, which
		// starts at its very edge; or until the keyboard goes there (Alt+J).
		const strip = page.getByTestId('scrubber');
		await expect(strip).toHaveAttribute('data-shown', 'false');
		await page.mouse.move(width - 70, 450, { steps: 2 });
		await page.waitForTimeout(200);
		await expect(strip, '70 px from the edge: still hidden').toHaveAttribute('data-shown', 'false');
		await page.mouse.move(width - 30, 450, { steps: 2 });
		await expect(strip).toHaveAttribute('data-shown', 'true');
		await page.mouse.move(width - 1, 450, { steps: 2 });
		await expect(strip).toHaveAttribute('data-shown', 'true');
		await page.screenshot({ path: info.outputPath(`edge-${width}-list.png`) });
		await page.mouse.move(width / 2, 450, { steps: 2 });
		await expect(strip, 'hidden again once the pointer leaves').toHaveAttribute('data-shown', 'false');
		await page.keyboard.press('Alt+KeyJ');
		await expect(strip.getByRole('slider', { name: 'Jump to a date' })).toBeFocused();
		await expect(strip).toHaveAttribute('data-shown', 'true');
		await page.keyboard.press('Escape');
		await expect(strip).toHaveAttribute('data-shown', 'false');
		// Back to a page without one: the scroll bar again, in the same place.
		await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Profile' }).click(); // in the app, not a new load
		await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
		expect(await page.evaluate(() => document.documentElement.classList.contains('scrubber-page'))).toBe(false);
		expect((await frame(page)).top).toBe(plain.top);
		await page.context().close();
	}
});

// Loading more as the list scrolls, each page 300 ms late: the rows come
// before the scroll reaches their end, and neither the side menu nor the
// scrubber moves (owner, 2026-11-13: it "hits the bottom and jolts"). The
// scrubber's line follows the list on every frame of the scroll.
test('Loading more: no jolt; the scrubber follows the scroll', async ({ browser }) => {
	test.setTimeout(120_000);
	const n = projectN(test.info());
	const capcode = `07400${n}1`;
	const alias = `Jolt ${n}`;
	sql(`
INSERT INTO aliases (pattern, name, template_id) SELECT '${capcode}', '${alias}', id FROM templates WHERE name = 'Example Fire' ON CONFLICT DO NOTHING;
DELETE FROM messages WHERE capcode = '${capcode}';
INSERT INTO messages (capcode, text, priority, sent_at, dedup_key, alias_id)
SELECT '${capcode}', 'QDJOLT ${n} PAGE ' || g.i, 'Admin', now() - g.i * interval '2 hours',
       sha256(convert_to('jolt ${n} ' || g.i, 'UTF8')), (SELECT id FROM aliases WHERE pattern = '${capcode}')
FROM generate_series(1, 320) AS g (i);`);
	const page = await (await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } })).newPage();
	await page.route(
		(u) => u.pathname === '/api/v1/messages',
		(r) => setTimeout(() => r.continue().catch(() => {}), 300)
	);
	await page.goto(`/?alias=${encodeURIComponent(`"${alias}"`)}`);
	await waitLive(page);
	await expect(page.getByTestId('scrubber').locator('.mark').first()).toBeAttached();
	await expect(page.getByTestId('message-list').locator('article').first()).toBeVisible();
	await page.evaluate(() => {
		const w = window as unknown as { frames_: object[] };
		w.frames_ = [];
		const each = () => {
			w.frames_.push({
				y: scrollY,
				h: document.documentElement.scrollHeight,
				side: document.querySelector('nav.side')!.getBoundingClientRect().top,
				strip: document.querySelector('[data-testid="scrubber"]')!.getBoundingClientRect().top,
				line: document.querySelector('[data-testid="scrubber"] .thumb')?.getBoundingClientRect().top ?? -1,
				more: !!document.querySelector('.more')
			});
			requestAnimationFrame(each);
		};
		requestAnimationFrame(each);
	});
	await page.mouse.move(640, 500);
	for (let k = 0; k < 120; k++) {
		await page.mouse.wheel(0, 120);
		await page.waitForTimeout(32); // a fast flick: about 3,700 px a second
	}
	await page.waitForTimeout(400);
	type F = { y: number; h: number; side: number; strip: number; line: number; more: boolean };
	const frames = (await page.evaluate(() => (window as unknown as { frames_: object[] }).frames_)) as F[];
	const scrolled = frames.filter((f) => f.y > 200);
	expect(new Set(scrolled.map((f) => Math.round(f.side))).size, 'the side menu never moved').toBe(1);
	expect(new Set(scrolled.map((f) => Math.round(f.strip))).size, 'the scrubber never moved').toBe(1);
	expect(frames.at(-1)!.h, 'older rows loaded on the way').toBeGreaterThan(frames[0].h * 3);
	// Frames at the end of the loaded rows, with more still to come: none.
	const stuck = frames.filter((f) => f.more && f.h - f.y - 900 < 2).length;
	expect(stuck, 'frames stopped at the end of the rows before more came').toBeLessThanOrEqual(2);
	// The line: downwards with the list, a little on most frames, never back.
	const lines = scrolled.map((f) => f.line);
	for (let k = 1; k < lines.length; k++) expect(lines[k], 'the line never goes back up').toBeGreaterThanOrEqual(lines[k - 1] - 0.5);
	expect(new Set(lines.map((l) => l.toFixed(1))).size, 'the line moved on many frames').toBeGreaterThan(40);
	await page.context().close();
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
	await expect(page.getByTestId('scrubber'), 'no scrubber in an album (owner, 2026-11-13)').toHaveCount(0);
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

// A84-A86 in the browser (M22, R87c, R87d; owner, 2026-11-13): a large-event
// integration set up in the form fires once, when the event opens, and the
// Large events page under Tools lists the event with its jobs, never levels
// or codes; its view shows its job numbers and pages with the scrubber, at
// 1,280 and 375 px, with axe. Made-up codes, one set per browser project.
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
		// from none, so this one opens here.
		sql('DELETE FROM large_events;');

		const desk = await (await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } })).newPage();
		await desk.goto('/integrations');
		await desk.getByRole('button', { name: 'New integration' }).click();
		const name = `Large events ${Date.now()}`;
		await desk.getByLabel('Name required', { exact: true }).fill(name);
		await desk.getByRole('radio', { name: /^Large events: a summary/ }).check();
		// A summary only, with no level: no capcodes, filter, content or level to choose.
		await expect(desk.getByRole('combobox', { name: 'Capcodes and aliases' })).toHaveCount(0);
		await expect(desk.getByText('Full: the text and every field')).toHaveCount(0);
		const form = desk.locator('form.integration-form');
		await expect(form.getByRole('radio')).toHaveCount(2);
		await expect(desk.getByTestId('large-event-help')).toContainText('once for each large event, when the event opens');
		await expect(form).not.toContainText(/deploy|level|big job/i);
		await desk.getByLabel('URL required', { exact: true }).fill(`http://127.0.0.1:${port}/hook`);
		await form.screenshot({ path: info.outputPath('large-event-integration-form-1280.png') });
		await desk.getByRole('button', { name: 'Save integration' }).click();
		await expect(desk.getByRole('status').filter({ hasText: `Saved “${name}”` })).toBeVisible();
		const card = desk.getByTestId('integration').filter({ hasText: name });
		await expect(card).toContainText('Large events, each when it opens');
		const list = await (await request.get(`${URL}/api/v1/integrations`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } })).json();
		integrationID = list.integrations.find((i: { name: string }) => i.name === name).id;

		// The first code opens the event: one delivery within 5 s. The second
		// joins it: nothing more.
		const start = Date.now();
		const opener = await postId(request, '0100001', `@@${jobs[0]} GRS3 ${codes[0]} GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1`);
		await expect.poll(() => received.filter((r) => r.event_type === 'large_event').length, { timeout: 5000 }).toBe(1);
		console.log(`A86 (browser): the delivery came ${Date.now() - start} ms after the page that opened the event`);
		await post(request, '0100002', `@@${jobs[1]} GRS3 ${codes[1]} GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2`);
		await desk.waitForTimeout(1500);
		expect(received.filter((r) => r.event_type === 'large_event')).toHaveLength(1);
		const got = received.find((r) => r.event_type === 'large_event')!;
		const ev = got.large_event as { id: number; started_at: string; opened_by: { id: number; sent_at: string }; jobs: string[]; emergency_pages: number; url: string };
		expect(Object.keys(ev).sort()).toEqual(['emergency_pages', 'id', 'jobs', 'opened_by', 'started_at', 'url']);
		expect(ev.opened_by.id).toBe(got.id);
		expect(ev.opened_by.id).toBe(opener);
		expect(ev.jobs).toEqual([jobs[0]]);
		expect(ev.emergency_pages).toBe(1);
		expect(JSON.stringify(got)).not.toMatch(/GRASS FIRE|DG9|deploy|level|units|appliance/i);
		expect(got.text).toBeUndefined();

		// The Large events page, from Tools: newest first, its first and last
		// pages (never "ongoing"), its jobs and Emergency pages, and no levels
		// or codes (owner, 2026-11-13).
		await desk.goto('/tools');
		await desk.getByRole('main').getByRole('link', { name: 'Large events' }).click();
		await expect(desk.getByRole('heading', { level: 1, name: 'Large events' })).toBeVisible();
		expect(await crumbsOf(desk)).toEqual([
			{ label: 'Tools', href: '/tools' },
			{ label: 'Large events', href: null }
		]);
		await expect(desk.getByRole('main')).not.toContainText(/deploy/i);
		const first = desk.getByTestId('large-event').first();
		await expect(first).toHaveAttribute('data-event-id', String(ev.id));
		await expect(first).not.toContainText(/ongoing/i);
		// Its first and last pages' times, each on its own labelled line (owner, 2026-11-13).
		await expect(first.locator('time')).toHaveCount(2);
		await expect(first).toContainText('First page');
		await expect(first).toContainText('Last page');
		// Jobs and Emergency pages: never an appliance count, a level or codes (owner, 2026-11-13).
		await expect(first).toContainText('2 jobs · 2 Emergency pages');
		await expect(first).not.toContainText(/unit|appliance|deploy|level|big job|code/i);
		await desk.screenshot({ path: info.outputPath('large-events-list-1280.png') });
		expect(await axeOf(desk), 'axe: Large events').toEqual([]);

		// Its view: its job numbers, each a link to its job, and its pages, with the scrubber.
		await first.getByRole('link').click();
		await expect(desk).toHaveURL(new RegExp(`/large-events/${ev.id}$`));
		const about = desk.getByTestId('event-about');
		await expect(about).toContainText('2 jobs · 2 Emergency pages');
		await expect(about).not.toContainText(/unit|appliance|deploy|level|code/i);
		await expect(about.getByRole('term')).toHaveText(['First page', 'Last page', 'Job numbers']);
		for (const j of jobs) await expect(about.getByRole('link', { name: j, exact: true })).toHaveAttribute('href', `/jobs/${j}`);
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
		await expect(phone.getByTestId('large-event').first()).toContainText('2 jobs · 3 Emergency pages');
		expect(await overflow()).toBeLessThanOrEqual(0);
		await phone.screenshot({ path: info.outputPath('large-events-list-375.png') });
		expect(await axeOf(phone), 'axe: Large events on a phone').toEqual([]);
		await phone.getByTestId('large-event').first().getByRole('link').click();
		await expect(phone.getByTestId('message-list').locator('article')).toHaveCount(3);
		await waitLive(phone);
		expect(await overflow()).toBeLessThanOrEqual(0);
		await phone.screenshot({ path: info.outputPath('large-event-view-375.png'), fullPage: true });
		expect(await axeOf(phone), 'axe: a large event on a phone').toEqual([]);
		// Another code and job joining doesn't fire it again.
		await post(request, '0100004', `@@${uniqueJob()} GRS3 DG9${n}03 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN4`);
		await phone.waitForTimeout(1500);
		expect(received.filter((r) => r.event_type === 'large_event')).toHaveLength(1);
	} finally {
		if (integrationID) await request.delete(`${URL}/api/v1/integrations/${integrationID}`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
		receiver.close();
	}
});

// R87: the admins' threshold of Emergency pages on one job, in Administration.
test('A85 large event settings in Administration', async ({ browser }) => {
	const admin = await (await signedIn(browser, 'admin', { viewport: { width: 1280, height: 900 } })).newPage();
	await admin.goto('/admin');
	await admin.getByTestId('admin-screens').getByRole('link', { name: 'Large event settings' }).click();
	const field = admin.getByLabel('Emergency pages on one job that open a large event');
	await expect(field).toHaveValue('20');
	await field.fill('25');
	await admin.getByRole('button', { name: 'Save' }).click();
	await expect(admin.getByRole('status').filter({ hasText: 'has 25 Emergency pages' })).toBeVisible();
	await admin.reload();
	await expect(admin.getByLabel('Emergency pages on one job that open a large event')).toHaveValue('25');
	expect(await axeOf(admin), 'axe: large event settings').toEqual([]);
	await admin.getByLabel('Emergency pages on one job that open a large event').fill('20');
	await admin.getByRole('button', { name: 'Save' }).click();
	await expect(admin.getByRole('status').filter({ hasText: 'has 20 Emergency pages' })).toBeVisible();
	// A user is refused.
	const user = await (await signedIn(browser, 'user')).newPage();
	await user.goto('/admin/large-events');
	await expect(user.getByRole('alert')).toHaveText('This page is for admins only.');
});

// R86a, Back up now: the button on Administration → Backups, as the backup
// service answers it. scripts/e2e.sh gives the API a status file and a request
// folder in PA_E2E_BACKUP_DIR, and this test plays the backup service: it
// starts the backup (running_since), then deletes the request and writes the
// result. The real service is scripts/backup-checks.sh's and A80's. Desktop and,
// as Administration still opens on a phone from a link (R81), phone width.
test('Back up now on the Backups page', async ({ browser }) => {
	const dir = process.env.PA_E2E_BACKUP_DIR;
	test.skip(!dir, 'PA_E2E_BACKUP_DIR is not set (run scripts/e2e.sh)');
	const statusFile = join(dir!, 'status', 'status.json');
	const request = join(dir!, 'requests', 'request.json');
	const s = (t: Date) => Math.floor(t.getTime() / 1000);
	const lastGood = new Date(Date.now() - 5 * 3600_000);
	const writeStatus = (fields: { running_since?: number; last_run_at?: number; last_run_ok?: boolean; error?: string } = {}) => {
		const f = { running_since: 0, last_run_at: s(lastGood), last_run_ok: true, error: '', ...fields };
		const good = f.last_run_ok ? f.last_run_at : s(lastGood);
		writeFileSync(
			statusFile,
			JSON.stringify({ since: s(lastGood) - 86400, last_night: '', ...f, last_good_at: good, last_good_night: '2026-11-14', last_good_bytes: 1048576, nights: 3 })
		);
	};
	mkdirSync(join(dir!, 'status'), { recursive: true });
	mkdirSync(join(dir!, 'requests'), { recursive: true });
	rmSync(request, { force: true });
	writeStatus();
	try {
		for (const viewport of [
			{ width: 1280, height: 900 },
			{ width: 375, height: 800 }
		]) {
			const page = await (await signedIn(browser, 'admin', { viewport })).newPage();
			const where = `${viewport.width}px`;
			await page.goto('/admin');
			await page.goto('/admin/backups');
			const button = page.getByRole('button', { name: 'Back up now' });
			const state = page.getByTestId('backup-now');
			await expect(button, where).toBeEnabled();
			await expect(page.getByTestId('backup-status')).toContainText('Working.');
			expect(await axeOf(page), `axe: backups, ${where}`).toEqual([]);

			await button.click();
			await expect(state, where).toContainText('Asked for at');
			await expect(state).toContainText('starts it within a minute');
			await expect(button).toBeDisabled();
			expect(existsSync(request), 'the request file').toBe(true);
			// One at a time.
			const again = await page.request.post('/api/v1/admin/backup');
			expect(again.status()).toBe(409);
			expect((await again.json()).code).toBe('backup_queued');
			expect(await axeOf(page), `axe: backups, queued, ${where}`).toEqual([]);

			// The service starts it, the page follows.
			const started = new Date();
			writeStatus({ running_since: s(started) });
			await expect(state, where).toContainText('Running since', { timeout: 10_000 });
			await expect(state).toContainText('then the check');
			await expect(button).toBeDisabled();
			expect((await (await page.request.post('/api/v1/admin/backup')).json()).code).toBe('backup_running');
			expect(await axeOf(page), `axe: backups, running, ${where}`).toEqual([]);

			// The result: good on the desktop, failed on the phone.
			rmSync(request, { force: true });
			if (viewport.width > 768) {
				writeStatus({ last_run_at: s(started) });
				await expect(state, where).toContainText('Done: the backup at', { timeout: 10_000 });
				await expect(state).toContainText('was good.');
			} else {
				writeStatus({ last_run_at: s(started), last_run_ok: false, error: 'the dump of pagerarchive failed' });
				await expect(state, where).toContainText('Done, but it failed: the dump of pagerarchive failed.', { timeout: 10_000 });
				await expect(page.getByTestId('backup-alert')).toContainText('The last backup failed');
				const edge = await alertEdge(page.getByTestId('backup-alert'));
				expect(edge, `${where}: the alert has no edge`).toEqual(noEdge);
				// The phone's width is used, not overflowed.
				expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), 'no sideways scroll').toBe(true);
			}
			await expect(button).toBeEnabled();
			expect(await axeOf(page), `axe: backups, done, ${where}`).toEqual([]);
			await page.context().close();
			writeStatus();
		}
	} finally {
		rmSync(request, { force: true });
		writeStatus();
	}
});

// R84h: Administration → Upgrades, and the notice on Administration's start
// page, from the automatic upgrades' status file, which scripts/e2e.sh gives
// the API in PA_E2E_BACKUP_DIR and this test writes in the host script's place
// (the real script is A90's, scripts/a90.sh): off, then on after an upgrade,
// then a failed upgrade rolled back with the database restored, then checks
// that stopped. At 1,280 and 375 px (Administration opens on a phone from a
// link, R81), with axe.
test('R84h automatic upgrades in Administration', async ({ browser }, info) => {
	const dir = process.env.PA_E2E_BACKUP_DIR;
	test.skip(!dir, 'PA_E2E_BACKUP_DIR is not set (run scripts/e2e.sh)');
	const file = join(dir!, 'auto-upgrade', 'status.json');
	mkdirSync(join(dir!, 'auto-upgrade'), { recursive: true });
	rmSync(file, { force: true });
	const s = (ago: number) => Math.floor((Date.now() - ago) / 1000);
	const upgraded = { at: s(26 * 3600_000), from: '1.9.0', to: '1.10.0' };
	const ok = {
		enabled: true, enabled_at: s(48 * 3600_000), state: 'idle', running_since: 0, last_check_at: s(600_000), last_check_result: 'up-to-date',
		last_check_note: '1.10.0 is the latest release', running_version: '1.10.0', latest_version: '1.10.0', failures: 0, rollbacks: 0,
		failed_versions: [], last_upgrade: upgraded, history: [{ ...upgraded, result: 'upgraded' }]
	};
	const failure = { at: s(300_000), from: '1.10.0', to: '1.11.0', step: 'health', reason: 'no page stored within 30 minutes of the restart, with 48 in the 120 minutes before', rolled_back: true, restored: true };
	const rolledBack = {
		...ok, last_check_at: s(300_000), last_check_result: 'rolled-back', last_check_note: failure.reason, latest_version: '1.11.0', failures: 1, rollbacks: 1,
		failed_versions: ['1.11.0'], last_failure: failure, history: [...ok.history, { at: failure.at, from: '1.10.0', to: '1.11.0', result: 'rolled-back' }]
	};
	try {
		for (const viewport of [
			{ width: 1280, height: 900 },
			{ width: 375, height: 800 }
		]) {
			const where = `${viewport.width}px`;
			const page = await (await signedIn(browser, 'admin', { viewport })).newPage();
			const noSideways = async () => expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), `no sideways scroll, ${where}`).toBe(true);
			rmSync(file, { force: true });

			// Off: said plainly, and how to switch it on.
			await page.goto('/admin');
			await expect(page.getByTestId('upgrade-tag'), where).toHaveText('off');
			await expect(page.getByTestId('upgrade-alert')).toHaveCount(0);
			await page.getByTestId('admin-screens').getByRole('link', { name: 'Upgrades' }).click();
			await expect(page).toHaveURL(/\/admin\/upgrades$/);
			await expect(page.getByTestId('upgrade-status')).toContainText('Automatic upgrades are off.');
			await expect(page.getByText('deploy/auto-upgrade/auto-upgrade.sh enable')).toBeVisible();
			expect(await axeOf(page), `axe: upgrades, off, ${where}`).toEqual([]);

			// On, after an upgrade: the last check and upgrade, and the history; no notice.
			writeFileSync(file, JSON.stringify(ok));
			await page.reload();
			const status = page.getByTestId('upgrade-status');
			await expect(status, where).toContainText('On: checking for a new release every hour.');
			await expect(status).toContainText('1.10.0 is the latest release');
			await expect(status).toContainText('1.9.0 to 1.10.0');
			await expect(page.getByTestId('upgrade-history').locator('tbody tr')).toHaveText([/1\.9\.0\s*1\.10\.0\s*Upgraded/]);
			await expect(page.getByTestId('upgrade-alert')).toHaveCount(0);
			expect(await axeOf(page), `axe: upgrades, on, ${where}`).toEqual([]);

			// Failed and rolled back: the notice here and on the start page.
			writeFileSync(file, JSON.stringify(rolledBack));
			await page.reload();
			const alert = page.getByTestId('upgrade-alert');
			await expect(alert, where).toContainText('The automatic upgrade to 1.11.0 failed');
			await expect(alert).toContainText('no page stored within 30 minutes of the restart');
			await expect(alert).toContainText('rolled back to 1.10.0, and the database restored from the backup made just before');
			await expect(alert).toContainText("1.11.0 won't be tried again");
			const edge = await alertEdge(alert);
			expect(edge, `${where}: the alert has no edge`).toEqual(noEdge);
			await expect(status).toContainText('On, but the last upgrade failed.');
			await expect(status).toContainText('1 upgrade, 1 rolled back');
			await expect(page.getByTestId('upgrade-history').locator('tbody tr').first()).toContainText('Failed, rolled back');
			await noSideways();
			await page.screenshot({ path: info.outputPath(`upgrades-rolled-back-${viewport.width}.png`), fullPage: true });
			expect(await axeOf(page), `axe: upgrades, rolled back, ${where}`).toEqual([]);
			await page.goto('/admin');
			await expect(page.getByTestId('upgrade-alert')).toContainText('The automatic upgrade to 1.11.0 failed');
			await expect(page.getByTestId('upgrade-tag')).toHaveText('failed');
			expect(await axeOf(page), `axe: Administration with the notice, ${where}`).toEqual([]);

			// The checks stopped: 4 hours since the last.
			writeFileSync(file, JSON.stringify({ ...ok, last_check_at: s(4 * 3600_000) }));
			await page.reload();
			await expect(page.getByTestId('upgrade-alert')).toContainText("haven't checked for a release for more than 3 hours");
			await expect(page.getByTestId('upgrade-tag')).toHaveText('not checking');
			await page.context().close();
		}
		// Admins only.
		const user = await (await signedIn(browser, 'user')).newPage();
		await user.goto('/admin/upgrades');
		await expect(user.getByRole('alert')).toHaveText('This page is for admins only.');
		await user.context().close();
	} finally {
		rmSync(file, { force: true });
	}
	console.log('R84h: Upgrades off, on, rolled back and stale, with the notice on Administration, at 1,280 and 375 px; axe clean');
});

// R64 (owner, 2026-11-14): template names are unique ignoring case. Adding one
// that differs from "Example Fire" only in case is refused, naming it; a pair
// from before the rule (put straight into the database, as an upgrade leaves
// it) is listed on Templates and tagged on Administration's start page.
test('A89 template names unique ignoring case', async ({ browser }, info) => {
	const admin = await (await signedIn(browser, 'admin', { viewport: { width: 1280, height: 900 } })).newPage();
	await admin.goto('/admin/templates');
	await admin.getByRole('button', { name: 'Add template' }).click();
	await admin.getByLabel('Name required', { exact: true }).fill('EXAMPLE FIRE');
	await admin.getByLabel('Agency required', { exact: true }).fill('Example Fire Service');
	await admin.getByRole('button', { name: 'Add template' }).click();
	await expect(admin.getByRole('alert')).toContainText('There\'s already a template called "Example Fire", which differs from "EXAMPLE FIRE" only in letter case.');

	const old = `case pair ${info.project.name}`;
	const pair = [old, old.toUpperCase()];
	sql(pair.map((n, i) => `INSERT INTO templates (name, agency, case_duplicate) VALUES ('${n}', 'Made-up agency', ${i > 0});`).join('\n'));
	try {
		await admin.goto('/admin/templates');
		const notice = admin.getByTestId('case-duplicates');
		for (const n of pair) await expect(notice).toContainText(`'${n}'`);
		await expect(notice).toContainText('Rename all but one of each');
		await expect(admin.locator(`tr[data-template="${pair[0]}"]`)).toContainText(`same name as '${pair[1]}', ignoring case`);
		expect(await axeOf(admin), 'axe: templates with a pair').toEqual([]);
		await admin.goto('/admin');
		await expect(admin.getByTestId('case-tag')).toHaveText('2 named alike, ignoring case');
		expect(await axeOf(admin), 'axe: Administration with the tag').toEqual([]);
	} finally {
		sql(`DELETE FROM templates WHERE lower(name) = '${old}';`);
	}
	await admin.goto('/admin');
	await expect(admin.getByTestId('case-tag')).toHaveCount(0);
});

// A19 (R27b): an alias name another alias has, in any case, is refused in
// the form, naming that alias. A100: names shared from before the rule (put
// straight into the database, as an upgrade leaves them) are listed on
// Aliases with a tag on each, on each one's page, and as a tag on the
// Administration start page, until one is renamed.
test('A19 A100 alias names unique ignoring case', async ({ browser }, info) => {
	const n = projectN(info);
	const admin = await (await signedIn(browser, 'admin', { viewport: { width: 1280, height: 900 } })).newPage();
	await admin.goto('/admin/aliases');
	await admin.getByRole('button', { name: 'Add alias' }).click();
	const add = admin.getByTestId('alias-form');
	await add.getByLabel('Capcode or pattern').fill(`08100${n}1`);
	await add.getByLabel('Name').fill('STATION 1');
	await add.getByRole('button', { name: 'Add alias' }).click();
	await expect(add.getByRole('alert')).toContainText('There\'s already an alias called "Station 1" (0100001), which differs from "STATION 1" only in letter case.');

	const shared = `Shared name ${n}`;
	const pair: [string, string][] = [
		[`08100${n}2`, shared],
		[`08100${n}3`, shared.toUpperCase()]
	];
	sql(
		pair
			.map(([p, name], i) => `INSERT INTO aliases (pattern, name, template_id, name_duplicate) SELECT '${p}', '${name}', id, ${i > 0} FROM templates WHERE name = 'Example Fire';`)
			.join('\n')
	);
	try {
		await admin.goto('/admin/aliases');
		const notice = admin.getByTestId('shared-names');
		for (const [p, name] of pair) await expect(notice).toContainText(`'${name}' (${p})`);
		await expect(notice).toContainText('Rename all but one of each');
		const row = (name: string) =>
			admin
				.getByTestId('alias-table')
				.getByRole('row')
				.filter({ has: admin.getByRole('link', { name, exact: true }) });
		await expect(row(pair[0][1]).getByTestId('shared-tag')).toHaveText(`same name as ${pair[1][0]}, ignoring case`);
		await expect(row(pair[1][1]).getByTestId('shared-tag')).toHaveText(`same name as ${pair[0][0]}, ignoring case`);
		expect(await axeOf(admin), 'axe: aliases sharing a name').toEqual([]);
		await admin.goto('/admin');
		await expect(admin.getByTestId('shared-names-tag')).toHaveText('2 share names');
		expect(await axeOf(admin), 'axe: Administration with the tag').toEqual([]);

		// The newer one's page names the other; renamed, it isn't listed.
		await admin.goto('/admin/aliases');
		await row(pair[1][1]).getByRole('link', { name: pair[1][1], exact: true }).click();
		await expect(admin.getByTestId('shared-name')).toContainText(`'${pair[0][1]}' (${pair[0][0]})`);
		expect(await axeOf(admin), 'axe: an alias sharing its name').toEqual([]);
		const form = admin.getByTestId('alias-form');
		await form.getByLabel('Name').fill(pair[0][1]);
		await form.getByRole('button', { name: 'Save' }).click();
		await expect(form.getByRole('alert')).toContainText(`There's already an alias called "${pair[0][1]}" (${pair[0][0]})`);
		await form.getByLabel('Name').fill(`${shared} North`);
		await form.getByRole('button', { name: 'Save' }).click();
		await expect(admin.getByRole('status')).toContainText('Saved');
		await expect(admin.getByTestId('shared-name')).toHaveCount(0);
		await admin.goto('/admin/aliases');
		await expect(admin.getByTestId('alias-table')).toBeVisible();
		await expect(admin.getByTestId('shared-names')).toHaveCount(0);
		await admin.goto('/admin');
		await expect(admin.getByRole('link', { name: /Aliases/ }).first()).toBeVisible();
		await expect(admin.getByTestId('shared-names-tag')).toHaveCount(0);
	} finally {
		sql(`DELETE FROM aliases WHERE pattern IN (${pair.map(([p]) => `'${p}'`).join(', ')});`);
	}
	await admin.context().close();
});

// M24 (R38a-R38i): the reports in the browser, on each browser project's own
// made-up week (agency "Report agency n", Report stations n1-n4, from
// scripts/e2e.sh); the counts themselves are checked in Go (A91-A99). Each
// report at 1,280 and 375 px: its table, its chart, no sideways scroll, the
// axe check, and its CSV and chart image downloads.
const reportWeeks: Record<number, { monday: string; offset: string; month: string }> = {
	1: { monday: '2026-10-05', offset: '+11:00', month: '2025-03' },
	2: { monday: '2026-09-07', offset: '+10:00', month: '2025-04' },
	3: { monday: '2026-08-10', offset: '+10:00', month: '2025-05' }
};

function reportDay(n: number, days: number, time: string): string {
	const w = reportWeeks[n] ?? reportWeeks[1];
	const d = new Date(`${w.monday}T00:00:00Z`);
	d.setUTCDate(d.getUTCDate() + days);
	return `${d.toISOString().slice(0, 10)}T${time}:00${w.offset}`;
}

async function postAt(request: APIRequestContext, capcode: string, text: string, at: string) {
	const res = await request.post(`${URL}/api/v1/pages`, {
		headers: { Authorization: `Bearer ${KEY}` },
		data: { capcode, text, received_at: new Date(at).toISOString(), baud: 512 }
	});
	expect(res.status()).toBe(200);
}

/** The project's week (3 jobs of "Report agency n") and its large events,
 * posted once per project: the first test to run posts them. Each is checked
 * on its own: A86 deletes every large event, so an earlier test's week (the
 * R35f localities report's) can still be there with its large events gone. */
async function reportWeek(request: APIRequestContext, n: number) {
	const s = (k: number) => `08000${n}${k}`;
	const has = async (path: string, key: 'jobs' | 'events') => {
		const res = await request.get(`${URL}/api/v1/reports/${path}`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
		return res.ok() && (await res.json())[key] > 0;
	};
	const m = reportWeeks[n].month;
	if (!(await has(`busy-times?${reportRange(n)}`, 'jobs'))) await reportJobs(request, n, s);
	if (!(await has(`large-events?from=${m}-01&to=${m}-28`, 'events'))) await reportLargeEvents(request, m, s);
}

async function reportJobs(request: APIRequestContext, n: number, s: (k: number) => string) {
	const [a, b, c] = [uniqueJob(), uniqueJob(), uniqueJob()];
	for (const k of [1, 2, 3]) await postAt(request, s(k), `@@${a} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON UNITS P1 T1 #STN${k}`, reportDay(n, 0, '14:10'));
	await postAt(request, s(1), `@@${a} HSF1 SMOKE FROM ROOF, CALL BACK @ 12 FICTION ST, MADEUPTON UNITS P1 T1 #STN1`, reportDay(n, 0, '14:40'));
	// Not counted (R38a; owner, 2026-11-15): a Non-emergency follow-up and a
	// page with no job number.
	await postAt(request, s(1), `HbRE ${a} CREWS RETURNING #STN1`, reportDay(n, 0, '16:40'));
	await postAt(request, s(2), `@@${b} GRS3 GRASS FIRE @ SAMPLE RD, TESTVILLE UNITS T1 #STN2`, reportDay(n, 1, '09:05'));
	await postAt(request, s(3), 'QDTraining Wednesday 7pm at the station', reportDay(n, 2, '19:00'));
	await postAt(request, s(1), `@@${c} HSF1 SMOKE IN KITCHEN @ 3 OTHER RD, SAMPLEBURY UNITS P1 #STN1`, reportDay(n, 5, '14:30'));
	await postAt(request, s(2), `@@${c} HSF1 SMOKE IN KITCHEN @ 3 OTHER RD, SAMPLEBURY UNITS P1 #STN2`, reportDay(n, 5, '14:31'));
}

/** Large events in the project's own month of 2025: one of two jobs, 5 h
 * long, and one of a page. */
async function reportLargeEvents(request: APIRequestContext, m: string, s: (k: number) => string) {
	const tz = m === '2025-03' ? '+11:00' : '+10:00';
	await postAt(request, s(4), `@@${uniqueJob()} GRS3 DG0801 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN4`, `${m}-03T10:00:00${tz}`);
	await postAt(request, s(4), `@@${uniqueJob()} GRS3 DG0802 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN4`, `${m}-03T15:00:00${tz}`);
	await postAt(request, s(4), `@@${uniqueJob()} GRS3 DG0803 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN4`, `${m}-20T10:00:00${tz}`);
}

function reportRange(n: number): string {
	const w = reportWeeks[n] ?? reportWeeks[1];
	const sunday = new Date(`${w.monday}T00:00:00Z`);
	sunday.setUTCDate(sunday.getUTCDate() + 6);
	return `from=${w.monday}&to=${sunday.toISOString().slice(0, 10)}&agency=${encodeURIComponent(`Report agency ${n}`)}`;
}

/** The rows of the report's table, each as its cells' text joined by " | ". */
async function tableRows(page: Page): Promise<string[]> {
	return page
		.getByTestId('report-table')
		.locator('tbody tr')
		.evaluateAll((rows) => rows.map((r) => [...r.children].map((c) => (c as HTMLElement).innerText.replace(/\s+/g, ' ').trim()).join(' | ')));
}

const monthOf = (m: string) => new Intl.DateTimeFormat('en-AU', { month: 'short', year: 'numeric', timeZone: 'UTC' }).format(new Date(`${m}-01T00:00:00Z`));

const reportChecks: { path: string; name: string; slug: string; rows: (n: number) => string[]; csv: (n: number) => string[]; query?: (n: number) => string }[] = [
	{
		path: 'busiest',
		name: 'Busiest capcodes',
		slug: 'busiest-capcodes',
		rows: (n) => [
			`1 | Report station ${n}2 (08000${n}2) Report agency ${n} | 3 | 100% | 0 new`,
			`2 | Report station ${n}1 (08000${n}1) Report agency ${n} | 2 | 67% | 0 new`,
			`3 | Report station ${n}3 (08000${n}3) Report agency ${n} | 1 | 33% | 0 new`
		],
		csv: (n) => [`1,Report station ${n}2,08000${n}2,Report agency ${n},3,100%,0`, `2,Report station ${n}1,08000${n}1,Report agency ${n},2,67%,0`]
	},
	{
		path: 'localities',
		name: 'Busiest localities',
		slug: 'busiest-localities',
		rows: () => ['MADEUPTON | 1 | 33%', 'SAMPLEBURY Not on the map | 1 | 33%', 'TESTVILLE | 1 | 33%'],
		csv: () => ['Locality,Jobs,Share of jobs,On the map', 'MADEUPTON,1,33%,yes', 'SAMPLEBURY,1,33%,no', 'TESTVILLE,1,33%,yes']
	},
	{
		path: 'busy-times',
		name: "When it's busy",
		slug: 'when-its-busy',
		rows: () => {
			const rows: string[] = [];
			for (let h = 0; h < 24; h++) {
				const cells = [0, 0, 0, 0, 0, 0, 0];
				if (h === 14) cells[0] = cells[5] = 1;
				if (h === 9) cells[1] = 1;
				rows.push([`${String(h).padStart(2, '0')}:00`, ...cells].join(' | '));
			}
			return rows;
		},
		csv: () => ['Hour,Monday,Tuesday,Wednesday,Thursday,Friday,Saturday,Sunday', '09:00,0,1,0,0,0,0,0', '14:00,1,0,0,0,0,1,0', '19:00,0,0,0,0,0,0,0']
	},
	{
		path: 'event-types',
		name: 'Event type mix',
		slug: 'event-type-mix',
		rows: () => ['HSF1 House fire | Structure fire | 2 | 67%', 'GRS3 Grass fire | Grass fire | 1 | 33%'],
		csv: () => ['All,3,2,1']
	},
	{
		path: 'job-size',
		name: 'Job size',
		slug: 'job-size',
		rows: () => ['Every event type | 3 | 2 1–4, up to 4 | 2 1–3, up to 3 | 1 1–2, up to 2', 'HSF1 House fire | 2 | 2 2–4, up to 4 | 2 2–3, up to 3 | 1 1–2, up to 2'],
		csv: () => ['Every event type,3,2,1–4,4,2,1–3,3,1,1–2,2']
	},
	{
		path: 'long-jobs',
		name: 'Long-running jobs',
		slug: 'long-running-jobs',
		rows: () => [],
		csv: () => ['Job number,First page,Last page,Seconds,Time,Pages,Event type,Locality']
	},
	{
		path: 'appliances',
		name: 'Appliance activity',
		slug: 'appliance-activity',
		rows: () => ['P1 | 2 | 67%', 'T1 | 2 | 67%'],
		csv: () => ['Appliance,Jobs,Share of jobs', 'P1,2,67%', 'T1,2,67%']
	},
	{
		path: 'large-events',
		name: 'Large events over time',
		slug: 'large-events-over-time',
		query: (n) => `from=${reportWeeks[n].month}-01&to=${reportWeeks[n].month}-28`,
		rows: (n) => [`${monthOf(reportWeeks[n].month)} | 2 | 0 s | 1`],
		csv: (n) => ['Month,Large events,Typical seconds,Typical time,Typical jobs', `${reportWeeks[n].month}-01,2,0,0 s,1`]
	}
];

for (const r of reportChecks) {
	test(`M24 ${r.name}: table, chart, phone, axe, CSV and image`, async ({ browser, request }, info) => {
		const n = projectN(info);
		await reportWeek(request, n);
		const query = r.query?.(n) ?? reportRange(n);
		const fs = await import('node:fs/promises');
		for (const width of [1280, 375]) {
			const ctx = await signedIn(browser, 'user', { viewport: { width, height: 900 }, acceptDownloads: true });
			const page = await ctx.newPage();
			await page.goto(`/reports/${r.path}?${query}`);
			await expect(page.getByRole('heading', { level: 1, name: r.name })).toBeVisible();
			await expect(page.getByTestId('report')).toBeVisible();
			// R38a (owner, 2026-11-15): every report says its numbers can be wrong.
			await expect(page.getByTestId('report-caveat')).toHaveText("These numbers can be wrong: they count only the pages PagerArchive received and stored. Pages missed by the radio, or lost while it was down, aren't counted.");
			const rows = await tableRows(page);
			if (r.path === 'long-jobs') {
				expect(rows).toHaveLength(2);
				expect(rows[0]).toMatch(/^EX\d{7} HSF1 House fire, MADEUPTON \| .* \| 30 min \| 4$/);
				expect(rows[1]).toMatch(/^EX\d{7} HSF1 House fire, SAMPLEBURY \| .* \| 1 min \| 2$/);
			} else {
				expect(rows.slice(0, r.rows(n).length)).toEqual(r.rows(n));
			}
			// The chart beside the table, and the breadcrumbs (R79).
			await expect(page.locator('[data-chart] svg, [data-chart] table').first()).toBeVisible();
			await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('Reports');
			if (r.path === 'localities') await expect(page.getByTestId('map')).toHaveAttribute('data-ready', /true|unavailable/);
			// R81: the page never scrolls sideways.
			expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
			expect(await axeOf(page), `axe at ${width} px`).toEqual([]);
			await page.screenshot({ path: info.outputPath(`report-${r.path}-${width}.png`), fullPage: true });
			if (width === 1280) {
				const [csv] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Download CSV' }).click()]);
				expect(csv.suggestedFilename()).toMatch(new RegExp(`^pagerarchive-${r.slug}-\\d{4}-\\d{2}-\\d{2}-to-\\d{4}-\\d{2}-\\d{2}\\.csv$`));
				const text = await fs.readFile(await csv.path(), 'utf8');
				for (const line of r.csv(n)) expect(text.split('\r\n'), `${r.path} CSV`).toContain(line);
				expect(text).not.toMatch(/SMOKE|GRASS FIRE|FICTION ST|CREWS RETURNING|Training/);
				await expect(page.getByRole('status')).toHaveText('The table was saved as a CSV file.');
				const [png] = await Promise.all([page.waitForEvent('download'), page.getByRole('button', { name: 'Download chart' }).click()]);
				expect(png.suggestedFilename()).toMatch(new RegExp(`^pagerarchive-${r.slug}-.*\\.png$`));
				const image = await fs.readFile(await png.path());
				expect([...image.subarray(0, 8)]).toEqual([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
				expect(image.length).toBeGreaterThan(1000);
			}
			await ctx.close();
		}
	});
}

// R38a: the filters are in the address, and picking one shows the report
// for it; When it's busy for one alias picked from the suggestions; an empty
// range says so, with no chart.
test('M24 report filters, the alias picker and an empty range', async ({ page, request }, info) => {
	const n = projectN(info);
	await reportWeek(request, n);
	await page.goto(`/reports/busiest?${reportRange(n)}`);
	await expect(page.getByTestId('report-table')).toBeVisible();
	await page.getByLabel('Event type').selectOption('GRS3');
	await page.getByRole('button', { name: 'Show', exact: true }).click();
	await expect(page).toHaveURL(/event_type=GRS3/);
	await expect.poll(() => tableRows(page)).toEqual([`1 | Report station ${n}2 (08000${n}2) Report agency ${n} | 1 | 100% | 0 new`]);
	await page.reload();
	await expect(page.getByLabel('Event type')).toHaveValue('GRS3');
	await expect.poll(() => tableRows(page)).toHaveLength(1);

	await page.goto(`/reports/busy-times?${reportRange(n)}`);
	const box = page.getByRole('combobox', { name: 'Alias or capcode', exact: true });
	await box.fill(`report station ${n}2`);
	await page.getByRole('listbox', { name: 'Aliases and capcodes' }).getByRole('option', { name: `alias:"Report station ${n}2"` }).click();
	await page.getByRole('button', { name: 'Show', exact: true }).click();
	await expect(page).toHaveURL(new RegExp(`alias=Report\\+station\\+${n}2`));
	await expect(page.getByTestId('report-table').locator('caption')).toContainText(`Jobs to Report station ${n}2`);
	await expect(page.getByTestId('report-busiest')).toContainText('1 job');

	await page.goto('/reports/appliances?from=2001-01-01&to=2001-01-31');
	await expect(page.getByTestId('report-empty')).toHaveText('No jobs in this range named an appliance.');
	await expect(page.getByTestId('chart')).toHaveCount(0);
});

// A120 (R38a, R38d, R38h): the jobs behind a report's number, on the
// project's week (Report agency n: Monday 14:10, Tuesday 09:05 and Saturday
// 14:30), at 1,280 and 375 px, light and dark; the counts themselves are
// checked in Go (A120).
for (const width of [1280, 375]) {
	test(`A120 the jobs behind a number at ${width} px`, async ({ browser, browserName, request }, info) => {
		test.setTimeout(120_000);
		const n = projectN(info);
		await reportWeek(request, n);
		// WebKit's Tab skips links unless Alt is held.
		const tab = browserName === 'webkit' ? 'Alt+Tab' : 'Tab';
		const noText = /SMOKE|GRASS FIRE|FICTION ST|SAMPLE RD|OTHER RD|CREWS RETURNING|Training/;
		for (const colorScheme of ['light', 'dark'] as const) {
			const ctx = await signedIn(browser, 'user', { colorScheme, viewport: { width, height: 900 } });
			const page = await ctx.newPage();
			const noSideways = async () => expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);

			// When it's busy: the cells with jobs are links, named by count, day and hour; 0's aren't.
			await page.goto(`/reports/busy-times?${reportRange(n)}`);
			const grid = page.getByTestId('report-table');
			await expect(grid).toBeVisible();
			const links = grid.getByRole('link');
			await expect(links).toHaveCount(3);
			expect(await links.evaluateAll((as) => as.map((a) => a.getAttribute('aria-label')))).toEqual(['1 job, Tuesday 09:00', '1 job, Monday 14:00', '1 job, Saturday 14:00']);
			await expect(grid.locator('td')).toHaveCount(168);
			await expect(grid.locator('td:not(:has(a))')).toHaveCount(165);
			// ArchiveUx's heat rule (ADR 0049): each cell's count is in its own
			// step's text token, --heat-N-fg on --heat-N.
			const off = await grid.locator('td').evaluateAll((tds) => {
				const probe = document.createElement('span');
				document.body.append(probe);
				const bad = tds.flatMap((td) => {
					const step = /\bl([0-4])\b/.exec(td.className)?.[1];
					if (step === undefined) return [`${td.className}: no heat step`];
					probe.style.color = `var(--heat-${step}-fg)`;
					probe.style.backgroundColor = `var(--heat-${step})`;
					const want = getComputedStyle(probe);
					const s = getComputedStyle(td);
					return s.color === want.color && s.backgroundColor === want.backgroundColor ? [] : [`${td.className}: ${s.color} on ${s.backgroundColor}`];
				});
				probe.remove();
				return bad;
			});
			expect(off, `${colorScheme}: every count in --heat-N-fg on --heat-N`).toEqual([]);
			// From the keyboard: Tab from the grid's box reaches each, with its focus ring.
			await page.getByRole('region', { name: 'Jobs by hour and day, as a table' }).focus();
			for (const name of ['1 job, Tuesday 09:00', '1 job, Monday 14:00', '1 job, Saturday 14:00']) {
				await page.keyboard.press(tab);
				await expect(grid.getByRole('link', { name })).toBeFocused();
			}
			const ring = await page.evaluate(() => {
				const s = getComputedStyle(document.activeElement!);
				return { style: s.outlineStyle, width: parseFloat(s.outlineWidth) };
			});
			expect(ring.style).toBe('solid');
			expect(ring.width).toBeGreaterThan(0);
			await grid.screenshot({ path: info.outputPath(`a120-grid-focus-${colorScheme}-${width}.png`) });
			await page.keyboard.press('Enter');

			// Jobs: what it is, the cell's job, and the address holding it all.
			await expect(page).toHaveURL(/\/reports\/jobs\?/);
			const url = new globalThis.URL(page.url());
			const want = new URLSearchParams(reportRange(n));
			for (const k of ['from', 'to', 'agency']) expect(url.searchParams.get(k), k).toBe(want.get(k));
			expect(url.searchParams.get('weekday')).toBe('6');
			expect(url.searchParams.get('hour')).toBe('14');
			const title = page.getByTestId('jobs-title');
			await expect(title).toHaveText(new RegExp(String.raw`^Jobs on Saturdays 14:00–14:59, \d+ to \d+ \S+ 2026, Report agency ${n}$`));
			await expect(page.getByRole('heading', { level: 1 })).toHaveText(await title.innerText());
			const rows = () =>
				page
					.getByTestId('jobs-table')
					.locator('tbody tr')
					.evaluateAll((rs) => rs.map((r) => [...r.children].map((c) => (c as HTMLElement).innerText.replace(/\s+/g, ' ').trim()).join(' | ')));
			const listed = await rows();
			expect(listed).toHaveLength(1);
			expect(listed[0]).toMatch(/^EX\d{7} HSF1 House fire, SAMPLEBURY \| .+ \| 2$/);
			const job = listed[0].split(' ')[0];
			await expect(page.getByTestId('jobs-table').getByRole('link', { name: job })).toHaveAttribute('href', `/jobs/${job}`);
			await expect(page.getByTestId('report-caveat')).toBeVisible();
			await expect(page.locator('main')).not.toContainText(noText);
			await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toContainText('Reports');
			await noSideways();
			expect(await axeOf(page), `${colorScheme} jobs of a cell`).toEqual([]);
			await page.screenshot({ path: info.outputPath(`a120-cell-${colorScheme}-${width}.png`), fullPage: true });
			// Opened again from its address, the list is the same.
			await page.goto(url.toString());
			await expect.poll(rows).toEqual(listed);
			// The way back: the report, with its filters.
			await page.getByRole('link', { name: "Back to When it's busy" }).click();
			await expect(page).toHaveURL(/\/reports\/busy-times\?/);
			const backTo = new globalThis.URL(page.url()).searchParams;
			for (const k of ['from', 'to', 'agency']) expect(backTo.get(k), k).toBe(want.get(k));
			await expect(grid.getByRole('link')).toHaveCount(3);

			// Appliance activity: P1 opens its 2 jobs.
			await page.goto(`/reports/appliances?${reportRange(n)}`);
			await page.getByTestId('report-table').getByRole('link', { name: 'P1', exact: true }).click();
			await expect(page).toHaveURL(/\/reports\/jobs\?.*appliance=P1/);
			await expect(page.getByTestId('jobs-title')).toHaveText(new RegExp(String.raw`^Jobs naming P1, \d+ to \d+ \S+ 2026, Report agency ${n}$`));
			await expect.poll(async () => (await rows()).length).toBe(2);
			expect((await rows()).map((r) => r.replace(/^EX\d{7} /, '').replace(/ \| .+ \| /, ' | '))).toEqual(['HSF1 House fire, SAMPLEBURY | 2', 'HSF1 House fire, MADEUPTON | 4']);
			await expect(page.locator('main')).not.toContainText(noText);
			await noSideways();
			expect(await axeOf(page), `${colorScheme} jobs of an appliance`).toEqual([]);
			await page.screenshot({ path: info.outputPath(`a120-appliance-${colorScheme}-${width}.png`), fullPage: true });
			await page.getByRole('link', { name: 'Back to Appliance activity' }).click();
			await expect(page).toHaveURL(/\/reports\/appliances\?/);
			await ctx.close();
		}
	});
}


// A111 (R88b, R88c, R88e): the example's two made-up layers of areas, zones
// and regions over them, in the browser. A job whose pages are in two zones
// and one region says all three on its view; one in none says nothing; a
// message's view and the map's list say each page's zone and region; the map
// outlines and labels each layer under the messages, each in its own style,
// credits both, and each layer's button hides and shows it alone, remembered
// in this browser, in light and dark, at 1,280 and 375 px, with no sideways
// scroll; Administration lists both. Headless Firefox has no WebGL, so no
// map to draw them on there.
for (const width of [1280, 375]) {
	test(`A111 areas on a job, a message and the map at ${width} px`, async ({ browser, request }, info) => {
		test.setTimeout(120_000);
		const job = uniqueJob();
		const outside = uniqueJob();
		const west = await postId(request, '0100001', `@@${job} HSF1 SMOKE FROM ROOF @ 3 FICTION ST, MADEUPTON #STN1`);
		await postId(request, '0100002', `@@${job} HSF1 SMOKE FROM ROOF @ 5 EXAMPLE ST, MADEUPTON #STN2`);
		const far = await postId(request, '0100001', `@@${outside} GRS3 GRASS FIRE @ 4 FICTION ST, TESTVILLE #STN1`);
		for (const colorScheme of ['light', 'dark'] as const) {
			const ctx = await signedIn(browser, 'user', { colorScheme, viewport: { width, height: 900 } });
			const page = await ctx.newPage();
			const noSideways = async () => expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(0);

			// The job's view: both zones and the region, at a glance, above its pages.
			await page.goto(`/jobs/${job}`);
			const areas = page.getByTestId('job-areas');
			await expect(areas.getByRole('listitem')).toHaveText(['Zone A', 'Zone B', 'Region North']);
			await noSideways();
			expect(await axeOf(page), `${colorScheme} job view`).toEqual([]);
			await page.screenshot({ path: info.outputPath(`a111-job-${colorScheme}-${width}.png`), fullPage: true });
			await page.goto(`/jobs/${outside}`);
			await expect(page.getByTestId('message-list')).toContainText(outside);
			await expect(page.getByTestId('job-areas')).toHaveCount(0);

			// A message's view says its zone and region beside its location; one in none doesn't.
			await page.goto(`/messages/${west}`);
			await expect(page.getByTestId('area')).toHaveText(['Zone A', 'Region North']);
			await page.goto(`/messages/${far}`);
			await expect(page.getByTestId('location')).toContainText('From the address');
			await expect(page.getByTestId('area')).toHaveCount(0);

			// The map: the list says each page's areas, and each layer is drawn.
			await page.goto(`/map?job=${job}`);
			await expect(page.getByTestId('located-area')).toHaveText(['Zone B', 'Region North', 'Zone A', 'Region North']);
			const map = page.getByTestId('map');
			await expect(map).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
			if ((await map.getAttribute('data-ready')) !== 'true') {
				expect(info.project.name, 'only headless Firefox has no map').toBe('firefox');
				await ctx.close();
				continue;
			}
			// One "Map layers" control, closed at first: its panel has a checkbox for each layer.
			const control = page.getByRole('button', { name: 'Map layers' });
			const panel = page.getByRole('group', { name: 'Map layers' });
			const zones = page.getByRole('checkbox', { name: 'Zone outlines' });
			const regions = page.getByRole('checkbox', { name: 'Region outlines' });
			const drawn = (b: typeof zones) => expect.poll(async () => Number(await b.getAttribute('data-drawn')), { timeout: 10_000 });
			await expect(control).toHaveAttribute('aria-expanded', 'false');
			await expect(panel).toBeHidden();
			await expect(page.getByRole('button', { name: /outlines/ })).toHaveCount(0);
			// R35g: the credits are open when the map opens, phones too, crediting both layers.
			const credits = page.locator('.maplibregl-ctrl-attrib-inner');
			const creditsButton = page.getByRole('button', { name: 'Map credits' });
			await expect(credits).toBeVisible();
			await expect(creditsButton).toHaveAttribute('aria-expanded', 'true');
			await expect(credits).toContainText('Zone outlines: Example zones, made up for PagerArchive');
			await expect(credits).toContainText('Region outlines: Example regions, made up for PagerArchive');
			await page.screenshot({ path: info.outputPath(`a111-map-${colorScheme}-${width}.png`) });
			// After five seconds they close to the ⓘ, a button opening and closing them from the keyboard.
			await expect(credits).toBeHidden({ timeout: 10_000 });
			await expect(creditsButton).toHaveAttribute('aria-expanded', 'false');
			await expect(creditsButton).toBeVisible();
			expect(await axeOf(page), `${colorScheme} map, credits closed`).toEqual([]);
			await page.screenshot({ path: info.outputPath(`a111-map-credits-closed-${colorScheme}-${width}.png`) });
			await creditsButton.focus();
			await page.keyboard.press('Enter');
			await expect(credits).toBeVisible();
			await expect(creditsButton).toHaveAttribute('aria-expanded', 'true');
			await noSideways();
			expect(await axeOf(page), `${colorScheme} map, credits open, layers closed`).toEqual([]);
			await creditsButton.focus();
			await page.keyboard.press('Enter');
			await expect(credits).toBeHidden();
			// Opened, focus goes to the first checkbox; both layers are shown and drawn.
			await control.click();
			await expect(control).toHaveAttribute('aria-expanded', 'true');
			await expect(panel).toBeVisible();
			await expect(control).toHaveAttribute('aria-controls', (await panel.getAttribute('id'))!);
			await expect(zones).toBeFocused();
			for (const b of [zones, regions]) {
				await expect(b).toBeChecked();
				await drawn(b).toBeGreaterThan(0);
			}
			// The panel stays inside the map, leaving most of it in sight.
			const mapBox = (await map.boundingBox())!;
			const panelBox = (await panel.boundingBox())!;
			expect(panelBox.x + panelBox.width, 'panel inside the map').toBeLessThanOrEqual(mapBox.x + mapBox.width);
			expect(panelBox.y + panelBox.height, 'panel inside the map').toBeLessThanOrEqual(mapBox.y + mapBox.height);
			expect(panelBox.width * panelBox.height, 'panel covers under half the map').toBeLessThan((mapBox.width * mapBox.height) / 2);
			await noSideways();
			expect(await axeOf(page), `${colorScheme} map, layers open`).toEqual([]);
			await page.screenshot({ path: info.outputPath(`a111-map-layers-${colorScheme}-${width}.png`) });
			// Escape closes it, focus back on the control (and the page stays as it was).
			await page.keyboard.press('Escape');
			await expect(panel).toBeHidden();
			await expect(control).toBeFocused();
			await expect(page.getByTestId('map-frame')).not.toHaveAttribute('data-fullscreen');
			// From the keyboard: open, Space on a checkbox hides the regions alone.
			await page.keyboard.press('Enter');
			await expect(zones).toBeFocused();
			await page.keyboard.press('Tab');
			await expect(regions).toBeFocused();
			await page.keyboard.press('Space');
			await expect(regions).not.toBeChecked();
			await drawn(regions).toBe(0);
			await drawn(zones).toBeGreaterThan(0);
			// R35g: the credits name the shown layers only.
			await expect(credits).not.toContainText('Region outlines');
			await expect(credits).toContainText('Zone outlines: Example zones, made up for PagerArchive');
			// A click outside closes it; the control again closes it too.
			await page.getByRole('heading', { level: 1 }).click();
			await expect(panel).toBeHidden();
			await control.click();
			await expect(panel).toBeVisible();
			await control.click();
			await expect(panel).toBeHidden();
			await expect(control).toHaveAttribute('aria-expanded', 'false');
			// Still so on the next visit, closed; then shown again.
			await page.reload();
			await expect(panel).toBeHidden();
			// R35g: open again on the next visit, and moving the map closes the credits at once.
			await expect(map).toHaveAttribute('data-ready', 'true', { timeout: 15_000 });
			await expect(credits).toBeVisible();
			const box = (await map.boundingBox())!;
			await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
			await page.mouse.down();
			await page.mouse.move(box.x + box.width / 2 + 60, box.y + box.height / 2 + 30, { steps: 8 });
			await page.mouse.up();
			await expect(credits).toBeHidden({ timeout: 1_000 });
			await expect(creditsButton).toHaveAttribute('aria-expanded', 'false');
			await control.click();
			await expect(regions).not.toBeChecked();
			await expect(zones).toBeChecked();
			await regions.check();
			await drawn(regions).toBeGreaterThan(0);
			// In full screen too.
			await page.getByRole('button', { name: 'Full screen map' }).click();
			await expect(page.getByTestId('map-frame')).toHaveAttribute('data-fullscreen', /overlay|native/);
			await control.click();
			await expect(panel).toBeVisible();
			await regions.uncheck();
			await drawn(regions).toBe(0);
			await regions.check();
			await ctx.close();
		}

		// R88e: Administration lists both layers.
		const admin = await (await signedIn(browser, 'admin', { viewport: { width, height: 900 } })).newPage();
		await admin.goto('/admin');
		await expect(admin.getByTestId('admin-areas-zones')).toContainText('2 Zone outlines');
		await expect(admin.getByTestId('admin-areas-zones')).toContainText('example-areas.geojson');
		await expect(admin.getByTestId('admin-areas-zones')).toContainText('Example zones, made up for PagerArchive');
		await expect(admin.getByTestId('admin-areas-regions')).toContainText('2 Region outlines');
		await expect(admin.getByTestId('admin-areas-regions')).toContainText('example-regions.geojson');
		expect(await axeOf(admin), 'Administration').toEqual([]);
		await admin.context().close();
	});
}

// R88c: a network with no layers of areas has no layers control on its map.
test('A111 no layers, no layers control', async ({ browser }) => {
	const ctx = await signedIn(browser, 'user');
	const page = await ctx.newPage();
	await page.route('**/api/v1/meta', async (r) => {
		const meta = await (await r.fetch()).json();
		delete meta.areas;
		await r.fulfill({ json: meta });
	});
	await page.goto('/map');
	await expect(page.getByTestId('map')).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
	if ((await page.getByTestId('map').getAttribute('data-ready')) === 'true') await expect(page.getByRole('button', { name: 'Full screen map' })).toBeVisible();
	await expect(page.getByRole('button', { name: 'Map layers' })).toHaveCount(0);
	await ctx.close();
});

// A118 (R88c, R35c, R38c): every map has the layers and their control, and
// one choice per browser covers them all: hidden on the map page, hidden on a
// message's map and the localities map; shown there, shown on the map page;
// changed in one tab, followed in another already open. A message's small
// map keeps its marker in sight beside the open panel, and the outlines are
// fetched once for every map the page opens. Headless Firefox has no WebGL.

/** A page's map, its layers control, panel and checkboxes, once the map is drawn; undefined with no map (headless Firefox only). */
async function layersOf(page: Page, info: TestInfo) {
	const map = page.getByTestId('map');
	await expect(map).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
	if ((await map.getAttribute('data-ready')) !== 'true') {
		expect(info.project.name, 'only headless Firefox has no map').toBe('firefox');
		return undefined;
	}
	const zones = page.getByRole('checkbox', { name: 'Zone outlines' });
	const regions = page.getByRole('checkbox', { name: 'Region outlines' });
	return {
		map,
		control: page.getByRole('button', { name: 'Map layers' }),
		panel: page.getByRole('group', { name: 'Map layers' }),
		zones,
		regions,
		credits: page.locator('.maplibregl-ctrl-attrib-inner'),
		drawn: (b: typeof zones) => expect.poll(async () => Number(await b.getAttribute('data-drawn')), { timeout: 10_000 }),
		/** The control is there, under the full-screen button, its panel closed, and the outlines under what the map shows. */
		async present() {
			await expect(this.control).toHaveCount(1);
			await expect(this.control).toHaveAttribute('aria-expanded', 'false');
			await expect(this.panel).toBeHidden();
			const fs = (await page.getByRole('button', { name: 'Full screen map' }).boundingBox())!;
			const c = (await this.control.boundingBox())!;
			expect(Math.abs(c.x - fs.x), 'under the full-screen button').toBeLessThanOrEqual(1);
			expect(c.y, 'under the full-screen button').toBeGreaterThanOrEqual(fs.y + fs.height);
			const order = (await map.getAttribute('data-layers'))!.split(' ');
			const lastArea = Math.max(...order.map((id, i) => (id.startsWith('area-') ? i : -1)));
			expect(lastArea, `outlines drawn: ${order}`).toBeGreaterThanOrEqual(0);
			expect(order.slice(lastArea + 1).length, `something over the outlines: ${order}`).toBeGreaterThan(0);
			expect(order.slice(0, lastArea).every((id) => id.startsWith('area-')), `the outlines under the rest: ${order}`).toBe(true);
		},
		/** A message's map opens at zoom 16, where an area's edge may be out of sight: zoomed out to 14, both layers' are in it. */
		async zoomOut() {
			const out = page.getByRole('button', { name: 'Zoom out' });
			await out.click();
			await expect(map).toHaveAttribute('data-view', /,15\.00$/);
			await out.click();
			await expect(map).toHaveAttribute('data-view', /,14\.00$/);
		},
		async open() {
			await this.control.click();
			await expect(this.panel).toBeVisible();
			await expect(this.zones).toBeFocused();
		},
		/** Esc closes the panel, focus back on the control. */
		async close() {
			await page.keyboard.press('Escape');
			await expect(this.panel).toBeHidden();
			await expect(this.control).toBeFocused();
		},
		async regionsShown(shown: boolean) {
			if (shown) await expect(this.regions).toBeChecked();
			else await expect(this.regions).not.toBeChecked();
			await expect(this.zones).toBeChecked();
			if (shown) await this.drawn(regions).toBeGreaterThan(0);
			else await this.drawn(regions).toBe(0);
			await this.drawn(zones).toBeGreaterThan(0);
			if (shown) await expect(this.credits).toContainText('Region outlines: Example regions, made up for PagerArchive');
			else await expect(this.credits).not.toContainText('Region outlines');
			await expect(this.credits).toContainText('Zone outlines: Example zones, made up for PagerArchive');
		}
	};
}

test('A118 one layers choice for every map, other tabs too', async ({ browser, request }, info) => {
	test.setTimeout(120_000);
	const n = projectN(info);
	await reportWeek(request, n);
	const job = uniqueJob();
	const west = await postId(request, '0100001', `@@${job} HSF1 SMOKE FROM ROOF @ 3 FICTION ST, MADEUPTON #STN1`);
	const east = await postId(request, '0100002', `@@${job} HSF1 SMOKE FROM ROOF @ 5 EXAMPLE ST, MADEUPTON #STN2`);
	const ctx = await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } });
	const page = await ctx.newPage();

	// The map page: both layers shown; the regions hidden here.
	await page.goto(`/map?job=${job}`);
	const onMap = await layersOf(page, info);
	if (!onMap) return ctx.close();
	await onMap.present();
	await onMap.open();
	await onMap.regionsShown(true);
	await onMap.regions.uncheck();
	await onMap.regionsShown(false);
	await onMap.close();

	// A message's detail view: the control, and the regions hidden there too.
	await page.goto(`/messages/${west}`);
	const onMessage = (await layersOf(page, info))!;
	await onMessage.present();
	await onMessage.zoomOut();
	await onMessage.open();
	await onMessage.regionsShown(false);
	await onMessage.close();

	// The busiest localities' map: hidden there too; shown again here.
	await page.goto(`/reports/localities?${reportRange(n)}`);
	await expect(page.getByTestId('report')).toBeVisible();
	const onReport = (await layersOf(page, info))!;
	await onReport.present();
	await onReport.open();
	await onReport.regionsShown(false);
	await onReport.regions.check();
	await onReport.regionsShown(true);
	await onReport.close();

	// Back on the map page, they're shown.
	await page.goto(`/map?job=${job}`);
	const back = (await layersOf(page, info))!;
	await back.open();
	await back.regionsShown(true);

	// Another tab, already open on the map page, follows a change made on a message's map.
	const other = await ctx.newPage();
	await other.goto(`/map?job=${job}`);
	const onOther = (await layersOf(other, info))!;
	await onOther.open();
	await onOther.regionsShown(true);
	await page.bringToFront();
	await page.goto(`/messages/${west}`);
	const again = (await layersOf(page, info))!;
	await again.zoomOut();
	await again.open();
	await again.regions.uncheck();
	await again.regionsShown(false);
	await other.bringToFront();
	await onOther.regionsShown(false);
	await other.close();

	// Another message's view, opened in the app, fetches no outlines again.
	await page.bringToFront();
	const fetched: string[] = [];
	page.on('request', (r) => {
		if (r.url().includes('/api/v1/map/areas/')) fetched.push(r.url());
	});
	// The list beside the detail view (R80), the newest first.
	await page.locator(`a.open[href="/messages/${east}"]`).first().click();
	await expect(page).toHaveURL(new RegExp(`/messages/${east}$`));
	await expect(page.getByTestId('area')).toHaveText(['Zone B', 'Region North']);
	const second = (await layersOf(page, info))!;
	await second.zoomOut();
	await second.open();
	await second.drawn(second.zones).toBeGreaterThan(0);
	expect(fetched, 'outlines fetched again').toEqual([]);
	await second.regions.check();
	await ctx.close();
});

// A118: a message's small map, the panel open, keeps its marker in sight
// beside it; the localities map's heat and outlines, light and dark.
for (const width of [1280, 375]) {
	test(`A118 the small map and the heat map with the layers open at ${width} px`, async ({ browser, request }, info) => {
		test.setTimeout(120_000);
		const n = projectN(info);
		await reportWeek(request, n);
		const west = await postId(request, '0100001', `@@${uniqueJob()} HSF1 SMOKE FROM ROOF @ 3 FICTION ST, MADEUPTON #STN1`);
		for (const colorScheme of ['light', 'dark'] as const) {
			const ctx = await signedIn(browser, 'user', { colorScheme, viewport: { width, height: width === 375 ? 812 : 900 } });
			const page = await ctx.newPage();
			const noSideways = async () => expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(0);

			await page.goto(`/messages/${west}`);
			const l = await layersOf(page, info);
			if (!l) return ctx.close();
			await l.present();
			// The marker, inside the map and not under the open panel: at the zoom it opens at, and zoomed out.
			const inSight = async () => {
				await l.open();
				const mapBox = (await l.map.boundingBox())!;
				const panelBox = (await l.panel.boundingBox())!;
				const room = 12; // the marker's radius and its stroke, and some
				await expect
					.poll(
						async () => {
							const [x, y] = (await l.map.getAttribute('data-marker'))!.split(',').map(Number);
							const [px, py] = [mapBox.x + x, mapBox.y + y];
							const inMap = px > mapBox.x + room && px < mapBox.x + mapBox.width - room && py > mapBox.y + room && py < mapBox.y + mapBox.height - room;
							const underPanel = px > panelBox.x - room && px < panelBox.x + panelBox.width + room && py > panelBox.y - room && py < panelBox.y + panelBox.height + room;
							return inMap && !underPanel;
						},
						{ message: 'the marker in sight beside the panel', timeout: 5_000 }
					)
					.toBe(true);
			};
			await inSight();
			await noSideways();
			expect(await axeOf(page), `${colorScheme} message's map, layers open`).toEqual([]);
			await l.map.scrollIntoViewIfNeeded();
			await page.screenshot({ path: info.outputPath(`a118-message-${colorScheme}-${width}.png`) });
			await l.close();
			await l.zoomOut();
			await inSight();
			await l.regionsShown(true);
			await page.screenshot({ path: info.outputPath(`a118-message-zoomed-out-${colorScheme}-${width}.png`) });
			await l.close();

			await page.goto(`/reports/localities?${reportRange(n)}`);
			await expect(page.getByTestId('report')).toBeVisible();
			const h = (await layersOf(page, info))!;
			await h.present();
			await h.open();
			await h.regionsShown(true);
			await noSideways();
			expect(await axeOf(page), `${colorScheme} localities map, layers open`).toEqual([]);
			// The heat and the outlines, the credits folded (after five seconds), to look at (R36).
			await expect(h.credits).toBeHidden({ timeout: 10_000 });
			await h.map.screenshot({ path: info.outputPath(`a118-heat-${colorScheme}-${width}.png`) });
			await h.close();
			await ctx.close();
		}
	});
}

// R90, A116 (ADR 0037): installable on a phone. The service worker keeps
// the app shell and nothing else, pages always come from the network, and a
// new release takes over.

/** The paths the service worker may keep: the build's files, the icons, the manifest and the offline page. */
const shellPath = /^\/(_app\/immutable\/[\w./-]+|icons\/[a-z0-9-]+\.png|favicon(-32)?\.(svg|png)|apple-touch-icon\.png|manifest\.webmanifest|offline\.html)$/;

/** Every URL in every cache the page's origin has, by cache. */
async function cached(page: Page): Promise<Record<string, string[]>> {
	return page.evaluate(async () => {
		const out: Record<string, string[]> = {};
		for (const name of await caches.keys()) out[name] = (await (await caches.open(name)).keys()).map((r) => r.url);
		return out;
	});
}

/** Opens path and waits until the service worker controls the page (reloading once if it took over after the page loaded). */
async function controlled(page: Page, path = '/') {
	await page.goto(path);
	await page.evaluate(async () => {
		await navigator.serviceWorker.ready;
	});
	if (!(await page.evaluate(() => !!navigator.serviceWorker.controller))) await page.reload();
	await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);
}

test('A116 installable: the manifest, its icons and theme from the tokens, before sign-in', async ({ browser, browserName }) => {
	const ctx = await newContext(browser, { colorScheme: 'light', serviceWorkers: 'allow' });
	const page = await ctx.newPage();
	await page.goto('/');
	await expect(page).toHaveURL(/\/signin/);
	const href = await page.locator('head link[rel="manifest"]').evaluate((l) => (l as HTMLLinkElement).href);
	expect(new globalThis.URL(href).pathname).toBe('/manifest.webmanifest');
	// Loading it needs no session, as the favicon (R82e).
	const res = await ctx.request.get(href);
	expect(res.status()).toBe(200);
	expect(res.headers()['content-type']).toBe('application/manifest+json');
	const m = await res.json();
	expect(m).toMatchObject({ id: '/', name: 'PagerArchive', short_name: 'PagerArchive', start_url: '/', scope: '/', display: 'standalone' });
	// Its colours are the light theme's top bar (--surface) and page (--bg).
	const colour = (name: string) =>
		page.evaluate((n) => {
			const probe = document.createElement('span');
			probe.style.color = `var(${n})`;
			document.body.append(probe);
			const c = getComputedStyle(probe).color;
			probe.remove();
			return c;
		}, name);
	const rgb = (hex: string) => `rgb(${[1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)).join(', ')})`;
	expect(rgb(m.theme_color)).toBe(await colour('--surface'));
	expect(rgb(m.background_color)).toBe(await colour('--bg'));
	// 192 and 512, as plain and as maskable icons, each the size it says.
	const icons = m.icons as { src: string; sizes: string; type: string; purpose: string }[];
	for (const purpose of ['any', 'maskable'])
		for (const size of [192, 512]) expect(icons.some((i) => i.purpose === purpose && i.sizes === `${size}x${size}`), `${purpose} ${size}`).toBe(true);
	for (const icon of icons) {
		const png = await ctx.request.get(new globalThis.URL(icon.src, href).href);
		expect(png.status(), icon.src).toBe(200);
		expect(png.headers()['content-type']).toBe('image/png');
		const body = await png.body();
		const size = Number(icon.sizes.split('x')[0]);
		expect([body.readUInt32BE(16), body.readUInt32BE(20)], icon.src).toEqual([size, size]);
	}
	// iPhone: shown full screen from the home screen, with its own name.
	await expect(page.locator('head meta[name="apple-mobile-web-app-capable"]')).toHaveAttribute('content', 'yes');
	await expect(page.locator('head meta[name="apple-mobile-web-app-title"]')).toHaveAttribute('content', 'PagerArchive');
	// The status bar: the top bar's colour in each theme as sent (written from
	// the tokens), then the colour the top bar is drawn in.
	const served = await (await ctx.request.get(`${URL}/signin`)).text();
	expect(served).toContain(`<meta name="theme-color" content="${m.theme_color}" media="(prefers-color-scheme: light)" />`);
	expect(served).toMatch(/<meta name="theme-color" content="#[0-9a-f]{6}" media="\(prefers-color-scheme: dark\)" \/>/);
	const themeColours = page.locator('head meta[name="theme-color"]');
	await expect(themeColours).toHaveCount(2);
	for (const meta of await themeColours.all()) await expect(meta).toHaveAttribute('content', await colour('--surface'));
	await page.emulateMedia({ colorScheme: 'dark' });
	const dark = await colour('--surface');
	expect(dark).not.toBe(rgb(m.theme_color));
	for (const meta of await themeColours.all()) await expect(meta).toHaveAttribute('content', dark);

	// Chrome's own installability check: a valid manifest, its icons, and a
	// service worker that handles fetches (the "Install app" menu item).
	if (browserName === 'chromium') {
		await controlled(page, '/signin');
		const cdp = await ctx.newCDPSession(page);
		const manifest = (await cdp.send('Page.getAppManifest')) as { url: string; errors: { message: string }[] };
		expect(manifest.url).toBe(href);
		expect(manifest.errors).toEqual([]);
		await expect.poll(async () => ((await cdp.send('Page.getInstallabilityErrors')) as { installabilityErrors: unknown[] }).installabilityErrors).toEqual([]);
	}
	await ctx.close();
});

test('A116 the service worker keeps the app shell and nothing else', async ({ browser, browserName, request }) => {
	const ctx = await signedIn(browser, 'user', { serviceWorkers: 'allow' });
	const page = await ctx.newPage();
	const fromWorker: string[] = [];
	const notFromWorker: string[] = [];
	page.on('response', (r) => {
		const path = new globalThis.URL(r.url()).pathname;
		// Pages come through it too, from the network (never kept: below).
		if (r.request().isNavigationRequest()) return;
		(r.fromServiceWorker() ? fromWorker : notFromWorker).push(path);
	});
	await controlled(page);
	// Pages, a message, a job, a search, the map with its tiles, albums,
	// capcodes, integrations, an account page and reports: what a person sees.
	await waitLive(page);
	const job = uniqueJob();
	await post(request, '0100001', `@@${job} HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN1`);
	await page.getByTestId('message-list').locator('article').filter({ hasText: job }).locator('a.open').click();
	await expect(page.locator('dl').first()).toContainText(job);
	await page.goto(`/jobs/${job}`);
	await expect(page.locator('main article')).toHaveCount(1);
	await page.goto('/?q=SMOKE');
	await waitLive(page);
	await page.goto('/map');
	await expect(page.getByTestId('map')).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
	for (const path of ['/albums', '/capcodes', '/integrations', '/account/profile', '/reports']) {
		await page.goto(path);
		await expect(page.locator('main h1').first()).toBeVisible();
	}

	const all = await cached(page);
	const names = Object.keys(all);
	expect(names, "one cache, this build's shell").toHaveLength(1);
	expect(names[0]).toMatch(/^shell-/);
	const paths = all[names[0]].map((u) => new globalThis.URL(u).pathname);
	expect(
		paths.filter((p) => !shellPath.test(p)),
		'only shell files are kept'
	).toEqual([]);
	expect(paths.filter((p) => p.startsWith('/api/') || p.startsWith('/auth/') || p.startsWith('/map/') || p === '/')).toEqual([]);
	expect(paths).toContain('/offline.html');
	expect(paths).toContain('/manifest.webmanifest');
	// Every build file the pages loaded is in it: the shell is the build's.
	const loaded = await page.evaluate(() => performance.getEntriesByType('resource').map((e) => new globalThis.URL(e.name).pathname));
	for (const p of loaded.filter((p) => p.startsWith('/_app/immutable/'))) expect(paths, p).toContain(p);
	// Answers from the API never came through it, in any page; shell files did.
	if (browserName === 'chromium') {
		expect(
			fromWorker.filter((p) => !shellPath.test(p)),
			'nothing but shell files from the service worker'
		).toEqual([]);
		expect(fromWorker.some((p) => p.startsWith('/_app/immutable/'))).toBe(true);
		expect(notFromWorker.filter((p) => p.startsWith('/api/')).length).toBeGreaterThan(0);
	}
	console.log(`A116: ${paths.length} shell files kept in ${names[0]}; none from /api/, /auth/ or /map/, and no page`);
	await ctx.close();
});

test('A116 offline, a page says so, and shows nothing from the archive', async ({ browser, browserName }) => {
	test.skip(browserName === 'firefox', "Playwright's offline mode in Firefox doesn't reach a service worker's own requests");
	const ctx = await signedIn(browser, 'user', { serviceWorkers: 'allow' });
	const page = await ctx.newPage();
	await controlled(page);
	await waitLive(page);
	await ctx.setOffline(true);
	await page.goto('/albums');
	await expect(page.getByRole('heading', { level: 1, name: "You're offline" })).toBeVisible();
	await expect(page.getByRole('link', { name: 'Try again' })).toBeVisible();
	expect(await page.locator('body').innerText()).not.toMatch(/SMOKE|EX000/);
	expect(await axeOf(page), 'the offline page').toEqual([]);
	await ctx.setOffline(false);
	await page.getByRole('link', { name: 'Try again' }).click();
	await expect(page).toHaveURL(/\/albums$/);
	await expect(page.getByRole('heading', { level: 1, name: 'Albums' })).toBeVisible();
	await ctx.close();
});

test('A116 a new release takes over an installed app', async ({ browser }) => {
	const ctx = await signedIn(browser, 'user', { serviceWorkers: 'allow' });
	const page = await ctx.newPage();
	await controlled(page);
	await waitLive(page);
	const before = Object.keys(await cached(page));
	// An earlier release's cache, as an app installed before would have.
	await page.evaluate(async () => (await caches.open('shell-earlier')).put('/_app/immutable/earlier.js', new Response('export {};')));
	// A new release's service worker: the browser finds it as it checks for
	// one, and it takes over at once and clears the earlier caches.
	const scriptURL = await page.evaluate(async () => {
		const changed = new Promise<void>((resolve) => navigator.serviceWorker.addEventListener('controllerchange', () => resolve()));
		const reg = await navigator.serviceWorker.register('/service-worker.js?next', { type: 'module' });
		await changed;
		return reg.active?.scriptURL ?? '';
	});
	expect(scriptURL).toMatch(/\?next$/);
	await expect.poll(async () => Object.keys(await cached(page))).toEqual(before);
	await page.goto('/');
	await waitLive(page);
	await ctx.close();
});

// R90: a release that came out while the app was open is found when the app
// is brought back to the front, and the next tap loads that page whole, with
// the new release's code. (The release's version is answered for the test,
// so the service worker, which page.route can't always see past, is blocked.)
test('A116 an app left open loads a new release on the next tap', async ({ browser }) => {
	const ctx = await signedIn(browser);
	const page = await ctx.newPage();
	await page.goto('/');
	await waitLive(page);
	await page.route('**/_app/version.json', (r) => r.fulfill({ json: { version: 'next' } }));
	await page.evaluate(() => ((window as unknown as { stillHere: boolean }).stillHere = true));
	const asked = page.waitForResponse('**/_app/version.json');
	await page.evaluate(() => window.dispatchEvent(new Event('focus')));
	await asked;
	await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Albums', exact: true }).click();
	await expect(page).toHaveURL(/\/albums$/);
	await expect(page.getByRole('heading', { level: 1, name: 'Albums' })).toBeVisible();
	expect(await page.evaluate(() => (window as unknown as { stillHere?: boolean }).stillHere)).toBeUndefined();
	await ctx.close();
});

// A121 (R8; owner, 2026-11-17): the call sign is shown as Call sign, in a
// message's row and detail view and the integration form; callsign: finds
// its pages, and so do the old spellings, a typed group: and an address
// with group=, bookmarked before the rename (ADR 0040).
test('A121 call sign', async ({ browser, request }, info) => {
	const sign = `CS${projectN(info)}${uniqueJob().slice(2)}`;
	const other = `XS${projectN(info)}${uniqueJob().slice(2)}`;
	const job = uniqueJob();
	await post(request, '0100001', `@@${job} HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #${sign}`);
	await post(request, '0100002', `@@${job} HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #${other}`);
	const page = await (await signedIn(browser, 'user')).newPage();
	await page.goto('/');
	await waitLive(page);
	const box = page.getByRole('combobox', { name: 'Search' });
	const options = page.getByRole('listbox', { name: 'Search suggestions' }).getByRole('option');
	const list = page.getByTestId('message-list');
	const ids = () => list.locator('article').evaluateAll((as) => as.map((a) => a.getAttribute('data-message-id')));

	// The keys say callsign:, never group:, and its values are suggested.
	await box.click();
	await expect(options.filter({ hasText: 'callsign:' })).toHaveText(/callsign:\s*a call sign/);
	await expect(options.filter({ hasText: 'group:' })).toHaveCount(0);
	await box.fill(`callsign:${sign.slice(0, -1)}`);
	await expect(options.filter({ hasText: sign })).toHaveText(`callsign:"${sign}"`);

	// callsign: finds the one page, and its row says Call sign.
	await box.fill(`callsign:${sign}`);
	await box.press('Enter');
	await expect(page).toHaveURL(new RegExp(`/\\?callsign=${sign}$`));
	await expect(list.locator('article')).toHaveCount(1);
	const found = await ids();
	const row = list.locator('article').first();
	// The words are for screen readers, with the value: "Call sign CS…".
	await expect(row.locator('.callsign')).toHaveText(new RegExp(`^Call sign\\s${sign}$`));
	await expect(row.locator('.callsign .visually-hidden')).toHaveText(/^Call sign\s?$/);
	await expect(row.locator('.callsign')).toHaveAttribute('title', 'Call sign');

	// A typed group: finds the same page, and the address it writes says callsign=.
	await page.goto('/');
	await waitLive(page);
	await box.fill(`group:${sign}`);
	await box.press('Enter');
	await expect(page).toHaveURL(new RegExp(`/\\?callsign=${sign}$`));
	await expect(list.locator('article')).toHaveCount(1);
	expect(await ids()).toEqual(found);

	// An old address with group= still filters, and the box shows callsign:.
	await page.goto(`/?group=${sign}`);
	await waitLive(page);
	await expect(list.locator('article')).toHaveCount(1);
	expect(await ids()).toEqual(found);
	await expect(box).toHaveValue(`callsign:${sign}`);
	await expect(list).not.toContainText(other);

	// The detail view: Call sign, and no Group.
	await list.locator('article').first().locator('a.open').click();
	const details = page.locator('.rest dl');
	await expect(details.locator('dt:text-is("Call sign") + dd')).toHaveText(sign);
	await expect(details.locator('dt:text-is("Group")')).toHaveCount(0);

	// The integration form's filter: Call sign, and no Group.
	await page.goto('/integrations');
	await page.getByRole('button', { name: 'New integration' }).click();
	const form = page.locator('form.integration-form');
	await expect(form.getByLabel('Call sign', { exact: true })).toBeVisible();
	await expect(form.getByLabel('Group', { exact: true })).toHaveCount(0);
	console.log(`A121: callsign:${sign}, group:${sign} and ?group=${sign} found the same page; the row, detail view and integration form say Call sign`);
	await page.context().close();
});

/** Runs SQL on the test database, as sql() does, and returns psql's unaligned output. */
function sqlOut(input: string): string {
	const url = process.env.PA_E2E_DATABASE_URL;
	const args = ['-q', '-At', '-v', 'ON_ERROR_STOP=1'];
	if (url) return execFileSync('psql', [url, ...args], { input }).toString().trim();
	return execFileSync('docker', ['exec', '-i', process.env.PA_E2E_DB ?? '', 'psql', ...args, '-U', 'postgres'], { input }).toString().trim();
}

/** Locks one message's row in a psql session that stays open, so a re-parse
 * stops there until it's let go (as A122 and A123 hold theirs). */
async function holdMessage(id: number): Promise<() => Promise<void>> {
	const url = process.env.PA_E2E_DATABASE_URL;
	const p = url
		? spawn('psql', [url, '-q', '-At'])
		: spawn('docker', ['exec', '-i', process.env.PA_E2E_DB ?? '', 'psql', '-q', '-At', '-U', 'postgres']);
	let out = '';
	let closed = false;
	p.stdout.on('data', (d) => (out += d.toString()));
	p.on('close', () => (closed = true));
	p.stdin.write(`BEGIN; SELECT 'held' FROM messages WHERE id = ${id} FOR UPDATE;\n`);
	for (const until = Date.now() + 30_000; !out.includes('held'); await new Promise((r) => setTimeout(r, 100))) {
		if (Date.now() > until || closed) throw new Error('the lock on the held page was not taken');
	}
	return async () => {
		if (closed) return;
		p.stdin.end('ROLLBACK;\n');
		await new Promise((r) => p.on('close', r));
	};
}

// A123 (R23c): an admin's re-parse controls in Administration, desktop and
// phone. A re-parse of 3,000 made-up pages, held at the 1,500th by a lock,
// shows Running with done of total, its rate and time left, and a tag on the
// start page; Pause holds it (Resume, not Pause, then), Resume carries it on
// to the end (Idle, the last run's numbers); Stop shows Stopped, with no
// Resume, and Start again runs it from the beginning. Axe on each state.
test('A123 re-parse controls in Administration', async ({ browser }, info) => {
	test.setTimeout(240_000);
	const n = projectN(info);
	const capcode = `091230${n}`;
	sqlOut(`INSERT INTO messages (capcode, text, sent_at, dedup_key)
		SELECT '${capcode}', 'MADE-UP A123 E2E PAGE ' || g, now() - interval '400 days' + g * interval '1 second', sha256(('a123 e2e ${n} ' || g)::bytea)
		FROM generate_series(1, 3000) g`);
	const held = Number(sqlOut(`SELECT id FROM messages WHERE capcode = '${capcode}' ORDER BY id OFFSET 1499 LIMIT 1`));
	const queue = (reason: string) =>
		sqlOut(`INSERT INTO reparse_requests (capcode_pattern, reason) VALUES ('${capcode}', '${reason}'); SELECT pg_notify('pagerarchive_reparse', '')`);
	const page = await (await signedIn(browser, 'admin', { viewport: { width: 1280, height: 900 } })).newPage();
	page.on('dialog', (d) => d.accept());
	const noSideways = async (where: string) =>
		expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), `no sideways scroll, ${where}`).toBe(true);
	const reparseState = async () => (await (await page.request.get('/api/v1/admin/reparse')).json()).state as string;
	// Nothing else re-parsing: earlier tests' requests are done, and a run an
	// earlier try left held is stopped.
	await expect.poll(reparseState, { timeout: 60_000 }).not.toBe('running');
	if ((await reparseState()) !== 'idle') {
		await page.request.post('/api/v1/admin/reparse/start');
		await expect.poll(reparseState, { timeout: 60_000 }).toBe('idle');
	}

	let release = await holdMessage(held);
	try {
		queue('alias added');
		await expect.poll(reparseState, { timeout: 30_000 }).toBe('running');
		await page.goto('/admin');
		await expect(page.getByTestId('reparse-tag')).toHaveText(/running, \d+ %/, { timeout: 30_000 });
		await page.getByTestId('admin-screens').getByRole('link', { name: 'Re-parse' }).click();
		await expect(page).toHaveURL(/\/admin\/reparse$/);
		const state = page.getByTestId('reparse-state');
		const done = page.getByTestId('reparse-done');
		await expect(state).toHaveText('Running');
		await expect(done).toContainText('1,499 of 3,000 messages (49 %)', { timeout: 30_000 });
		await expect(page.getByTestId('reparse-status')).toContainText('an alias added');
		await expect(page.getByTestId('reparse-rate')).toContainText(/a minute|Working it out/);
		await expect(page.getByTestId('reparse-eta')).toBeVisible();
		const controls = page.getByRole('group', { name: 'Re-parse controls' });
		await expect(controls.getByRole('button')).toHaveText(['Pause', 'Stop', 'Start again']);
		expect(await axeOf(page), 'axe: re-parse running, 1280px').toEqual([]);
		await page.setViewportSize({ width: 375, height: 800 });
		await noSideways('running, 375px');
		expect(await axeOf(page), 'axe: re-parse running, 375px').toEqual([]);
		await page.screenshot({ path: info.outputPath('reparse-running-375.png'), fullPage: true });
		await page.setViewportSize({ width: 1280, height: 900 });

		// Pause: it stops after the page in hand.
		await controls.getByRole('button', { name: 'Pause' }).click();
		await expect(state).toHaveText('Paused');
		await expect(controls.getByRole('button')).toHaveText(['Resume', 'Stop', 'Start again']);
		await release();
		await page.reload();
		await expect(done).toContainText('1,500 of 3,000 messages (50 %)', { timeout: 15_000 });
		await expect(page.getByTestId('reparse-rate')).toHaveCount(0);
		expect(await axeOf(page), 'axe: re-parse paused').toEqual([]);
		await page.goto('/admin');
		await expect(page.getByTestId('reparse-tag')).toHaveText('paused');
		await page.goto('/admin/reparse');

		// Resume: on to the end, and Idle with the run's numbers.
		await controls.getByRole('button', { name: 'Resume' }).click();
		await expect(state).toHaveText('Idle', { timeout: 60_000 });
		await expect(done).toContainText('3,000 of 3,000 messages (100 %)');
		await expect(controls.getByRole('button')).toHaveText(['Start again']);
		expect(await axeOf(page), 'axe: re-parse idle').toEqual([]);

		// Stop, then Start again from the beginning.
		release = await holdMessage(held);
		queue('alias template changed');
		await expect(state).toHaveText('Running', { timeout: 30_000 });
		await expect(done).toContainText('1,499 of 3,000', { timeout: 30_000 });
		await controls.getByRole('button', { name: 'Stop' }).click();
		await expect(state).toHaveText('Stopped');
		await expect(controls.getByRole('button')).toHaveText(['Start again']);
		await page.setViewportSize({ width: 375, height: 800 });
		await noSideways('stopped, 375px');
		expect(await axeOf(page), 'axe: re-parse stopped, 375px').toEqual([]);
		await page.goto('/admin');
		await expect(page.getByTestId('reparse-tag')).toHaveText('stopped');
		await page.goto('/admin/reparse');
		await page.setViewportSize({ width: 1280, height: 900 });
		await controls.getByRole('button', { name: 'Start again' }).click();
		await expect(state).toHaveText('Running');
		await expect(done).toContainText(/\d of 3,000/);
		await release();
		await expect(state).toHaveText('Idle', { timeout: 60_000 });
		await expect(done).toContainText('3,000 of 3,000 messages (100 %)');
	} finally {
		await release();
		await page.context().close();
	}
	console.log(`A123 ${info.project.name}: running at 1,499 of 3,000, paused at 1,500, resumed to Idle, stopped and started again; 1280 and 375 px, axe clean`);
});

/** Holds retroactive redaction (R45a) right after page id's new text is
 * written, before its re-parse: a trigger on the page waits for an advisory
 * lock that a psql session keeps until it's let go. */
async function holdRedaction(id: number): Promise<() => Promise<void>> {
	sqlOut(`SET client_min_messages = warning; CREATE TABLE IF NOT EXISTS a124e2e_held (id bigint);
		DELETE FROM a124e2e_held; INSERT INTO a124e2e_held VALUES (${id});
		CREATE OR REPLACE FUNCTION a124e2e_hold() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN
			IF NEW.id IN (SELECT id FROM a124e2e_held) THEN PERFORM pg_advisory_xact_lock(124125); END IF;
			RETURN NULL;
		END $$;
		DROP TRIGGER IF EXISTS a124e2e_hold ON messages;
		CREATE TRIGGER a124e2e_hold AFTER UPDATE OF text ON messages FOR EACH ROW EXECUTE FUNCTION a124e2e_hold();`);
	const url = process.env.PA_E2E_DATABASE_URL;
	const p = url
		? spawn('psql', [url, '-q', '-At'])
		: spawn('docker', ['exec', '-i', process.env.PA_E2E_DB ?? '', 'psql', '-q', '-At', '-U', 'postgres']);
	let out = '';
	let closed = false;
	p.stdout.on('data', (d) => (out += d.toString()));
	p.on('close', () => (closed = true));
	p.stdin.write(`SELECT 'held', pg_advisory_lock(124125);\n`);
	for (const until = Date.now() + 30_000; !out.includes('held'); await new Promise((r) => setTimeout(r, 100))) {
		if (Date.now() > until || closed) throw new Error('the lock that holds the run was not taken');
	}
	return async () => {
		if (closed) return;
		p.stdin.end('SELECT pg_advisory_unlock(124125);\n');
		await new Promise((r) => p.on('close', r));
	};
}

// A124 (R45a): an admin's retroactive redaction controls in Administration,
// desktop and phone. A run over 3,000 made-up pages, every other one with a
// made-up number the example's rules remove, held at the 1,500th right after
// its new text is written, shows Running with done of total, its rate and
// time left, and a tag on the start page; Pause holds it (Resume, not Pause,
// then), Resume carries it on to the end (Idle, the run's numbers); Stop
// shows Stopped, with no Resume, and Start again runs it from the beginning.
// Axe on each state.
test('A124 retroactive redaction controls in Administration', async ({ browser }, info) => {
	test.setTimeout(240_000);
	const n = projectN(info);
	const capcode = `091240${n}`;
	const put = () =>
		sqlOut(`UPDATE messages m SET text = 'MADE-UP A124 E2E PAGE ' || x.g || CASE WHEN x.g % 2 = 0 THEN ' CALL 0412 345 678' ELSE '' END
			FROM (SELECT id, row_number() OVER (ORDER BY id) AS g FROM messages WHERE capcode = '${capcode}') x WHERE m.id = x.id`);
	sqlOut(`INSERT INTO messages (capcode, text, sent_at, dedup_key)
		SELECT '${capcode}', 'MADE-UP A124 E2E PAGE ' || g, now() - interval '400 days' + g * interval '1 second', sha256(('a124 e2e ${n} ' || g)::bytea)
		FROM generate_series(1, 3000) g`);
	put();
	const first = Number(sqlOut(`SELECT min(id) FROM messages WHERE capcode = '${capcode}'`));
	const held = Number(sqlOut(`SELECT id FROM messages WHERE capcode = '${capcode}' ORDER BY id OFFSET 1499 LIMIT 1`));
	const withNumber = () => sqlOut(`SELECT count(*) FROM messages WHERE capcode = '${capcode}' AND strpos(text, '0412 345 678') > 0`);
	// A run as the nightly run starts one, with the API's rules, placed just
	// before the made-up pages so it checks those.
	const startRun = () =>
		sqlOut(`DELETE FROM redaction_runs;
			INSERT INTO redaction_runs (state, reason, rules_hash, total, after_id)
			SELECT 'running', 'rules_changed', value, 3000, ${first - 1} FROM settings WHERE key = 'redaction_hash';
			SELECT pg_notify('pagerarchive_redaction', '')`);
	const page = await (await signedIn(browser, 'admin', { viewport: { width: 1280, height: 900 } })).newPage();
	page.on('dialog', (d) => d.accept());
	const noSideways = async (where: string) =>
		expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), `no sideways scroll, ${where}`).toBe(true);
	const redactionState = async () => (await (await page.request.get('/api/v1/admin/redaction')).json()).state as string;
	// The API's first nightly run has applied its rules, and no run an
	// earlier try left is under way.
	await expect.poll(() => sqlOut(`SELECT count(*) FROM settings WHERE key = 'redaction_hash'`), { timeout: 60_000 }).toBe('1');
	if ((await redactionState()) !== 'idle') {
		await page.request.post('/api/v1/admin/redaction/start');
		await expect.poll(redactionState, { timeout: 60_000 }).toBe('idle');
	}

	let release = await holdRedaction(held);
	try {
		startRun();
		await expect.poll(redactionState, { timeout: 30_000 }).toBe('running');
		await page.goto('/admin');
		await expect(page.getByTestId('redaction-tag')).toHaveText(/running, \d+ %/, { timeout: 30_000 });
		await page.getByTestId('admin-screens').getByRole('link', { name: 'Retroactive redaction' }).click();
		await expect(page).toHaveURL(/\/admin\/redaction$/);
		const state = page.getByTestId('redaction-state');
		const done = page.getByTestId('redaction-done');
		await expect(state).toHaveText('Running');
		await expect(done).toContainText('1,499 of 3,000 messages (49 %)', { timeout: 30_000 });
		await expect(page.getByTestId('redaction-status')).toContainText('the redaction rules changed');
		await expect(page.getByTestId('redaction-rate')).toContainText(/a minute|Working it out/);
		await expect(page.getByTestId('redaction-eta')).toBeVisible();
		const controls = page.getByRole('group', { name: 'Retroactive redaction controls' });
		await expect(controls.getByRole('button')).toHaveText(['Pause', 'Stop', 'Start again']);
		expect(await axeOf(page), 'axe: redaction running, 1280px').toEqual([]);
		await page.setViewportSize({ width: 375, height: 800 });
		await noSideways('running, 375px');
		expect(await axeOf(page), 'axe: redaction running, 375px').toEqual([]);
		await page.screenshot({ path: info.outputPath('redaction-running-375.png'), fullPage: true });
		await page.setViewportSize({ width: 1280, height: 900 });

		// Pause: it stops after the page in hand.
		await controls.getByRole('button', { name: 'Pause' }).click();
		await expect(state).toHaveText('Paused');
		await expect(controls.getByRole('button')).toHaveText(['Resume', 'Stop', 'Start again']);
		await release();
		await page.reload();
		await expect(done).toContainText('1,500 of 3,000 messages (50 %)', { timeout: 15_000 });
		await expect(page.getByTestId('redaction-rate')).toHaveCount(0);
		expect(await axeOf(page), 'axe: redaction paused').toEqual([]);
		await page.goto('/admin');
		await expect(page.getByTestId('redaction-tag')).toHaveText('paused');
		await page.goto('/admin/redaction');

		// Resume: on to the end, and Idle with the run's numbers.
		await controls.getByRole('button', { name: 'Resume' }).click();
		await expect(state).toHaveText('Idle', { timeout: 60_000 });
		await expect(done).toContainText('3,000 of 3,000 messages (100 %)');
		await expect(page.getByTestId('redaction-changed')).toHaveText('1,500 messages');
		await expect(controls.getByRole('button')).toHaveText(['Start again']);
		expect(await axeOf(page), 'axe: redaction idle').toEqual([]);
		expect(withNumber()).toBe('0');

		// Stop, then Start again from the beginning.
		put();
		release = await holdRedaction(held);
		startRun();
		await expect(state).toHaveText('Running', { timeout: 30_000 });
		await expect(done).toContainText('1,499 of 3,000', { timeout: 30_000 });
		await controls.getByRole('button', { name: 'Stop' }).click();
		await expect(state).toHaveText('Stopped');
		await expect(controls.getByRole('button')).toHaveText(['Start again']);
		await page.setViewportSize({ width: 375, height: 800 });
		await noSideways('stopped, 375px');
		expect(await axeOf(page), 'axe: redaction stopped, 375px').toEqual([]);
		await page.goto('/admin');
		await expect(page.getByTestId('redaction-tag')).toHaveText('stopped');
		await page.goto('/admin/redaction');
		await page.setViewportSize({ width: 1280, height: 900 });
		await controls.getByRole('button', { name: 'Start again' }).click();
		await expect(state).toHaveText('Running');
		await expect(page.getByTestId('redaction-status')).toContainText('an admin started it again');
		await release();
		await expect(state).toHaveText('Idle', { timeout: 60_000 });
		await expect(done).toContainText('(100 %)');
		expect(withNumber()).toBe('0');
	} finally {
		await release();
		sqlOut(`SET client_min_messages = warning; DROP TRIGGER IF EXISTS a124e2e_hold ON messages; DELETE FROM a124e2e_held`);
		await page.context().close();
	}
	console.log(`A124 ${info.project.name}: running at 1,499 of 3,000, paused at 1,500, resumed to Idle with 1,500 changed, stopped and started again; 1280 and 375 px, axe clean`);
});

// A126 (R49a; owner, 2026-11-18): the integration form offers the example
// agency's alarm levels by label, and only there. A webhook at "Alarm level
// at or above Second alarm" gets the level 2 and 3 pages, not level 1 or
// none, and nothing it gets, the integration's card and the pages' views
// name a level.
test('A126 level filter in the integration form', async ({ page, request }, info) => {
	const labels = ['First alarm', 'Second alarm', 'Third alarm', 'Fourth alarm', 'Fifth alarm', 'Sixth alarm', 'Seventh alarm', 'Eighth alarm', 'Ninth alarm'];
	const http = await import('node:http');
	const received: Record<string, unknown>[] = [];
	const bodies: string[] = [];
	const receiver = http.createServer((req, res) => {
		let data = '';
		req.on('data', (c) => (data += c));
		req.on('end', () => {
			bodies.push(data);
			received.push(JSON.parse(data || '{}'));
			res.writeHead(200, { 'Content-Type': 'text/plain' }).end('ok');
		});
	});
	await new Promise<void>((r) => receiver.listen(0, '127.0.0.1', r));
	const port = (receiver.address() as { port: number }).port;
	try {
		const allow = await request.put(`${URL}/api/v1/admin/integration-hosts`, {
			headers: { Cookie: `pa_session=${SESSIONS.admin}` },
			data: { entries: ['127.0.0.0/8'] }
		});
		expect(allow.status()).toBe(200);
		const name = `Bigger jobs ${projectN(info)} ${Date.now()}`;
		await page.goto('/integrations');
		await page.getByRole('button', { name: 'New integration' }).click();
		const form = page.locator('form.integration-form');
		const levels = form.getByTestId('level-filter');
		const field = levels.getByLabel('Field', { exact: true });
		await expect(field.locator('option')).toHaveText(['Any level', 'Example Fire Service: Alarm level']);
		await expect(levels.getByLabel('At or above', { exact: true })).toHaveCount(0);
		await field.selectOption({ label: 'Example Fire Service: Alarm level' });
		const at = levels.getByLabel('At or above', { exact: true });
		await expect(at.locator('option')).toHaveText(labels);
		await expect(at).toHaveValue('1');
		await at.selectOption({ label: 'Second alarm' });

		await form.getByLabel('Name required', { exact: true }).fill(name);
		await chooseInSelection(page, '0100001', 'Station 1 (0100001)');
		await form.getByLabel('URL required', { exact: true }).fill(`http://127.0.0.1:${port}/hook`);
		await form.getByRole('button', { name: 'Save integration' }).click();
		await expect(page.getByRole('status').filter({ hasText: `Saved “${name}”` })).toBeVisible();
		const card = page.getByTestId('integration').filter({ hasText: name });
		for (const l of labels) await expect(card).not.toContainText(l);

		const jobs = [uniqueJob(), uniqueJob(), uniqueJob()];
		const fire = (job: string, level: string) => `@@${job} HSF1 FACTORY FIRE @ 12 FICTION ST, MADEUPTON${level ? ` ALARM ${level}` : ''} #STN1`;
		await post(request, '0100001', fire(jobs[0], '1'));
		await post(request, '0100001', fire(jobs[1], ''));
		await post(request, '0100001', fire(jobs[2], '2'));
		await post(request, '0100001', fire(jobs[2], '3'));
		const ours = () => received.filter((r) => jobs.includes(r.job_number as string));
		await expect.poll(() => ours().length, { timeout: 10000 }).toBe(2);
		await page.waitForTimeout(1000);
		expect(ours().map((r) => [r.job_number, (r.extra as Record<string, string>).alarm_level])).toEqual([
			[jobs[2], '2'],
			[jobs[2], '3']
		]);
		for (const b of bodies) for (const l of labels) expect(b).not.toContain(l);

		// Edited again, the form keeps the field and the level.
		await card.getByRole('button', { name: 'Edit' }).click();
		const again = page.locator('form.integration-form');
		await expect(again.getByTestId('level-filter').getByLabel('Field', { exact: true })).toHaveValue('example-fire/alarm_level');
		await expect(again.getByTestId('level-filter').getByLabel('At or above', { exact: true })).toHaveValue('2');
		await again.getByRole('button', { name: 'Cancel' }).click();

		// The pages and their job show no level's label (R87b).
		await page.goto(`/?job=${jobs[2]}`);
		await waitLive(page);
		await expect(page.getByTestId('message-list').locator('article')).toHaveCount(2);
		for (const l of labels) await expect(page.locator('main')).not.toContainText(l);
		await page.getByTestId('message-list').locator('article').first().locator('a.open').click();
		await expect(page.locator('.rest dl')).toBeVisible();
		for (const l of labels) await expect(page.locator('main')).not.toContainText(l);
		console.log(`A126: the form listed ${labels.length} levels; at or above Second alarm, the webhook got ${jobs[2]} at 2 and 3, not ${jobs[0]} (1) or ${jobs[1]} (none)`);


	} finally {
		receiver.close();
		// Its integration goes, so other tests' Station 1 pages don't reach it.
		const list = await request.get(`${URL}/api/v1/integrations`, { headers: asRole('user') });
		for (const i of (await list.json()).integrations as { id: number; name: string }[]) {
			if (i.name.startsWith(`Bigger jobs ${projectN(info)} `)) await request.delete(`${URL}/api/v1/integrations/${i.id}`, { headers: asRole('user') });
		}
	}
});

// A132, R82g: every form says which fields are required, in the label, with
// the word (owner, 2026-11-20: "The word required, every form."); unmarked
// is optional, and nothing says "(optional)". Each form checks itself on
// save: a message under each problem, focus on the first, a top line that
// only counts. ArchiveUx's rule (its design-system.md, section 5).
async function requiredRule(page: Page, where: string) {
	const found = await page.evaluate(() => {
		const shown = (el: Element) => (el as HTMLElement).getClientRects().length > 0;
		const controls = [...document.querySelectorAll<HTMLInputElement>('input[required], select[required], textarea[required]')]
			.filter((el) => shown(el) && !el.readOnly && !el.disabled)
			.map((el) => el.id);
		const optional = [...document.querySelectorAll('label, legend')].map((el) => el.textContent ?? '').filter((t) => /\(optional/i.test(t));
		const stray = [...document.querySelectorAll('.required')]
			.filter(shown)
			.map((m) => {
				const legend = m.closest('legend');
				if (legend) return legend.closest('fieldset')?.getAttribute('aria-describedby') ? '' : `a legend with no hint: ${legend.textContent}`;
				const label = m.closest('label');
				const control = label?.control as HTMLInputElement | null | undefined;
				return control?.required ? '' : `a marker on a control that isn't required: ${label?.textContent}`;
			})
			.filter(Boolean);
		return { controls, optional, stray };
	});
	expect(found.controls.every(Boolean), `${where}: every required control has an id`).toBe(true);
	expect(found.optional, `${where}: no "(optional)"`).toEqual([]);
	expect(found.stray, `${where}: markers`).toEqual([]);
	for (const id of found.controls) await expect(page.locator(`[id="${id}"]`), `${where}: #${id}'s name`).toHaveAccessibleName(/\brequired\b/);
	expect(await axeOf(page), `${where}: axe`).toEqual([]);
	return found.controls.length;
}

test('A132 required fields say so, and each form checks itself', async ({ browser, request }, info) => {
	test.setTimeout(240_000);
	const admin = await (await signedIn(browser, 'admin', { viewport: { width: 1280, height: 900 } })).newPage();
	const user = await (await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } })).newPage();
	const name = `A132 ${projectN(info)} ${Date.now()}`;
	// The integration made below sends to this machine (R51b's allow list).
	await request.put(`${URL}/api/v1/admin/integration-hosts`, { headers: asRole('admin'), data: { entries: ['127.0.0.0/8'] } });
	// A page on Station 1, for Add to album and the alias's requests.
	await post(request, '0100001', `@@${uniqueJob()} HSF1 A132 SMOKE @ 12 FICTION ST, MADEUPTON #STN1`);
	let posted = 0;
	admin.on('request', (r) => {
		if (r.method() === 'POST' && new globalThis.URL(r.url()).pathname === '/api/v1/integrations') posted++;
	});

	try {
		// A new integration, of every kind.
		await admin.goto('/integrations');
		await admin.getByRole('button', { name: 'New integration' }).click();
		const form = admin.locator('form.integration-form');
		await expect(form).toHaveAttribute('novalidate', '');
		const kinds = form.getByLabel('Send to');
		const options = await kinds.locator('option').allTextContents();
		expect(options.length).toBeGreaterThanOrEqual(6);
		for (const kind of options) {
			await kinds.selectOption({ label: kind });
			expect(await requiredRule(admin, `new integration: ${kind}`), kind).toBeGreaterThan(1);
		}
		await kinds.selectOption({ index: 0 }); // the webhook

		// The webhook: Name and URL required, the header's value only with a name.
		const nameBox = form.getByRole('textbox', { name: /^Name/ });
		const url = form.getByRole('textbox', { name: /^URL/ });
		const hvalue = form.getByLabel(/^Auth header value/);
		await expect(nameBox).toHaveAccessibleName(/^Name\W*required$/);
		await expect(url).toHaveAccessibleName(/^URL\W*required$/);
		await expect(hvalue).toHaveAccessibleName('Auth header value');
		await form.getByLabel(/^Auth header name/).fill('Authorization');
		await expect(hvalue).toHaveAccessibleName(/^Auth header value\W*required with a header name$/);
		await expect(hvalue).toHaveAttribute('required', '');
		await form.getByLabel(/^Auth header name/).fill('');
		await expect(hvalue).toHaveAccessibleName('Auth header value');
		const pages = form.getByRole('group', { name: /^Which pages/ });
		await expect(pages).toHaveAccessibleName(/^Which pages\W*required$/);
		await expect(pages).toHaveAccessibleDescription('Choose at least one capcode or alias.');

		// Saved empty: nothing is sent; a message under each problem; focus on the first.
		await form.getByRole('button', { name: 'Save integration' }).click();
		const summary = form.getByTestId('form-summary');
		await expect(summary).toHaveText('3 fields need attention');
		for (const [what, control] of [
			['Name', nameBox],
			['URL', url]
		] as const) {
			await expect(control, what).toHaveAttribute('aria-invalid', 'true');
			const described = (await control.getAttribute('aria-describedby')) ?? '';
			const message = admin.locator(`[id="${described.split(' ').pop()}"]`);
			await expect(message, what).toHaveClass(/field-error/);
			await expect(message, what).toBeVisible();
		}
		await expect(form.locator('[id$="-name-error"]')).toHaveText('Fill this in.');
		await expect(pages).toHaveAccessibleDescription('Choose at least one capcode or alias. Nothing chosen yet: choose at least one capcode or alias.');
		await expect(nameBox).toBeFocused();
		expect(await axeOf(admin), 'axe: after a save with problems').toEqual([]);

		// Name filled and saved again: its message goes; focus moves to the capcode box.
		await nameBox.fill(name);
		await expect(nameBox).not.toHaveAttribute('aria-invalid', 'true');
		await form.getByRole('button', { name: 'Save integration' }).click();
		await expect(summary).toHaveText('2 fields need attention');
		const box = form.getByRole('combobox', { name: 'Capcodes and aliases' });
		await expect(box).toBeFocused();
		expect(posted, 'nothing was sent').toBe(0);

		// Made, then edited: a saved URL needn't be typed again, so it's unmarked.
		await box.fill('0100009');
		await form.getByRole('listbox', { name: 'Suggestions' }).getByRole('option').first().click();
		await url.fill('http://127.0.0.1:9/a131');
		await form.getByRole('button', { name: 'Save integration' }).click();
		const card = admin.getByTestId('integration').filter({ hasText: name });
		await card.getByRole('button', { name: 'Edit' }).click();
		const edit = card.locator('form.integration-form');
		await expect(edit.getByRole('textbox', { name: /^URL/ })).toHaveAccessibleName('URL');
		await expect(edit.getByRole('textbox', { name: /^URL/ })).not.toHaveAttribute('required', '');
		await requiredRule(admin, 'an integration, edited');
		await edit.getByRole('button', { name: 'Cancel' }).click();

		// The other forms, each open.
		await admin.goto('/admin/templates');
		await admin.getByRole('button', { name: 'Add template' }).click();
		await requiredRule(admin, 'a new template');
		const templates = await request.get(`${URL}/api/v1/admin/templates`, { headers: asRole('admin') });
		const templateID = (await templates.json()).find((t: { name: string }) => t.name === 'Example Fire').id;
		await admin.goto(`/admin/templates/${templateID}`);
		await expect(admin.locator('main h1')).toBeVisible();
		await requiredRule(admin, "a template's page");
		await admin.goto('/admin/aliases');
		await admin.getByRole('button', { name: 'Add alias' }).click();
		await requiredRule(admin, 'a new alias');
		for (const path of ['/admin/retention', '/admin/large-events', '/account/invite', '/reports/capcode', '/albums']) {
			await admin.goto(path);
			await expect(admin.locator('main h1').first()).toBeVisible();
			await expect(admin.locator('main form').first()).toBeVisible();
			expect(await requiredRule(admin, path), path).toBeGreaterThan(0);
		}
		// An album's Edit, and Add to album.
		const album = await (await request.post(`${URL}/api/v1/albums`, { headers: asRole('admin'), data: { name, description: '' } })).json();
		await admin.goto(`/albums/${album.id}`);
		await admin.getByRole('button', { name: 'Rename or describe' }).click();
		await requiredRule(admin, "an album's Edit");
		await request.delete(`${URL}/api/v1/albums/${album.id}`, { headers: asRole('admin') });
		await admin.goto('/');
		await admin.getByTestId('message-list').locator('article a.open').first().click();
		await admin.getByRole('button', { name: 'Add to album' }).click();
		await expect(admin.getByRole('dialog', { name: 'Add to album' })).toBeVisible();
		await requiredRule(admin, 'Add to album');

		// A user's alias requests: new, a change and a deletion.
		await user.goto('/requests?pattern=0999999');
		await expect(user.getByRole('heading', { name: 'Request a new alias' })).toBeVisible();
		await requiredRule(user, 'a new alias request');
		const res = await request.get(`${URL}/api/v1/messages?capcode=0100001`, { headers: asRole('user') });
		const aliasID = (await res.json()).messages[0].alias_id;
		for (const ask of ['Request a change', 'Request deletion']) {
			await user.goto(`/aliases/${aliasID}`);
			await waitLive(user);
			await user.getByRole('button', { name: ask }).click();
			await expect(user.getByRole('heading', { name: ask })).toBeVisible();
			await requiredRule(user, ask);
		}
	} finally {
		const list = await request.get(`${URL}/api/v1/integrations`, { headers: asRole('admin') });
		for (const i of (await list.json()).integrations as { id: number; name: string }[]) {
			if (i.name === name) await request.delete(`${URL}/api/v1/integrations/${i.id}`, { headers: asRole('admin') });
		}
	}
});

// A131 (R19a, ADR 0050): a second receiver, the test's own, removed after:
// a receiver hears a transmission once, so a near copy comes from another.
async function withSecondReceiver(request: APIRequestContext, name: string, run: (post: (capcode: string, text: string, at: number) => Promise<string>) => Promise<void>) {
	const id = `a131-${name}`;
	const key = `a131-key-${id}-${Date.now()}`;
	const hash = createHash('sha256').update(key).digest('hex');
	sql(`INSERT INTO clients (id, name, key_hash) VALUES ('${id}', 'Second receiver', decode('${hash}', 'hex'));`);
	try {
		await run(async (capcode, text, at) => {
			const res = await request.post(`${URL}/api/v1/pages`, {
				headers: { Authorization: `Bearer ${key}` },
				data: { capcode, text, received_at: new Date(at).toISOString(), baud: 512 }
			});
			expect(res.status()).toBe(200);
			return (await res.json()).outcome;
		});
	} finally {
		sql(`DELETE FROM receptions WHERE client_id = '${id}'; DELETE FROM clients WHERE id = '${id}';`);
	}
}
// A131: a copy cut short is stored and shown live; the whole page from the
// second receiver, within the window, replaces its text in the open list,
// and the message's view shows the cut copy under its receiver, as heard.
test('A131 a better copy replaces a cut one, live, and the cut one is kept', async ({ page, request }) => {
	await withSecondReceiver(request, `live-${test.info().project.name}`, async (postB) => {
		const job = uniqueJob();
		const full = `@@${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`;
		const cut = full.slice(0, 32);
		await page.goto(`/?job=${job}`);
		await waitLive(page);
		const list = page.getByTestId('message-list');
		const now = Date.now();
		await postAt(request, '0100001', cut, new Date(now).toISOString());
		await expect(list.locator('article')).toHaveCount(1);
		await expect(list).not.toContainText('#STN1');
		expect(await postB('0100001', full, now + 400)).toBe('duplicate');
		// Shown in its place, live: still one row, now the whole page.
		await expect(list).toContainText('MADEUPTON #STN1');
		await expect(list.locator('article')).toHaveCount(1);

		await list.locator('article').first().locator('a.open').click();
		await expect(page.getByRole('heading', { name: 'Received by' })).toBeVisible();
		// The detail view (beside the list on a wide screen) shows the whole page.
		await expect(page.getByRole('region', { name: 'Message', exact: true }).locator('p.text')).toContainText('#STN1');
		const heard = page.getByTestId('variant');
		await expect(heard).toHaveCount(1);
		await expect(heard).toHaveText(cut);
		await expect(page.getByText('Heard as')).toBeVisible();
		await expect(page.locator('tbody tr').filter({ hasText: 'Receiver' })).toHaveCount(2);
	});
});

// A131: where a copy differs from the message, the differing characters are marked.
test('A131 a garbled copy is shown with its differences marked', async ({ page, request }) => {
	await withSecondReceiver(request, `marked-${test.info().project.name}`, async (postB) => {
		const job = uniqueJob();
		const full = `@@${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`;
		const now = Date.now();
		await postAt(request, '0100002', full, new Date(now).toISOString());
		expect(await postB('0100002', full.replace('FROM', 'FR}M'), now + 400)).toBe('duplicate');
		await page.goto(`/?job=${job}`);
		await waitLive(page);
		const list = page.getByTestId('message-list');
		await expect(list.locator('article')).toHaveCount(1);
		await list.locator('article').first().locator('a.open').click();
		const heard = page.getByTestId('variant');
		await expect(heard).toHaveText(full.replace('FROM', 'FR}M'));
		await expect(heard.locator('mark')).toHaveText(['}']);
	});
});

// A133 (R77c; owner, 2026-11-23): a new large event shows a dot. With the
// server's answer stood in, at 1,280 px: the Large events entry and the
// Tools heading, the group open and closed, and the shrunk menu's Tools icon
// with its tooltip; at 375 px, More, and Tools and Large events in it. The
// names say so, nothing scrolls sideways, and axe passes with the dot and
// without. The answer itself is A133's API half
// (internal/acceptance/a133_test.go).
const NEW_EVENT = { name: 'Large events, new in the last 4 hours', tools: 'Tools, new large event', more: 'More, new large event' };

/** Stands in the server's answer for the dot: on or off, for an hour. */
async function standInDot(page: Page, on: boolean) {
	const now = new Date();
	const state = JSON.stringify(on ? { new: true, until: new Date(now.getTime() + 3600_000).toISOString(), now: now.toISOString() } : { new: false, now: now.toISOString() });
	await page.route('**/api/v1/me/new-large-event/stream', (r) =>
		r.fulfill({ status: 200, headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' }, body: `retry: 3000\n\nevent: state\ndata: ${state}\n\n` })
	);
	await page.route('**/api/v1/me/new-large-event', (r) => r.fulfill({ json: JSON.parse(state) }));
}

/** The dot's fill against what it's drawn on: the nearest ancestor with a background. */
async function dotContrast(dot: Locator): Promise<number> {
	const c = await dot.evaluate((el) => {
		const fill = getComputedStyle(el).backgroundColor;
		let on = 'rgb(255, 255, 255)';
		for (let p = el.parentElement; p; p = p.parentElement) {
			const bg = getComputedStyle(p).backgroundColor;
			const a = bg.match(/rgba\([^)]*,\s*([\d.]+)\)$/);
			if (bg !== 'transparent' && !(a && Number(a[1]) === 0)) {
				on = bg;
				break;
			}
		}
		return { fill, on };
	});
	const rgb = (s: string) => {
		const n = s.match(/[\d.]+/g)!.slice(0, 3).map(Number);
		return s.startsWith('color(srgb') ? n.map((v) => v * 255) : n;
	};
	const lum = (x: number[]) => x.map((v) => v / 255).map((v) => (v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4)).reduce((s, v, i) => s + v * [0.2126, 0.7152, 0.0722][i], 0);
	const [x, y] = [lum(rgb(c.fill)), lum(rgb(c.on))].sort((p, q) => q - p);
	return (x + 0.05) / (y + 0.05);
}

test('A133 a new large event shows a dot in the menu', async ({ browser, request }, info) => {
	test.setTimeout(240_000);
	const report: string[] = [];
	const failures: string[] = [];
	const check = async (where: string, dot: Locator) => {
		await expect(dot, where).toBeVisible();
		const r = await dotContrast(dot);
		report.push(`${where} ${r.toFixed(2)}:1`);
		if (r < 3) failures.push(`${where}: the dot is ${r.toFixed(2)}:1, needs 3:1`);
	};
	const noSideScroll = async (page: Page, where: string) =>
		expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), `${where}: sideways scroll`).toBeLessThanOrEqual(0);
	for (const look of ['harbour', 'console'] as const) {
		await setLook(request, 'user', look);
		try {
			for (const colorScheme of ['light', 'dark'] as const) {
				const where = `${look === 'console' ? 'Console' : 'Harbour'}, ${colorScheme}`;
				// 1,280 px: the entry and the heading, open and closed, and shrunk.
				const page = await (await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 }, colorScheme })).newPage();
				await standInDot(page, true);
				await page.goto('/');
				await expect(page.locator('html')).toHaveAttribute('data-look', look);
				const nav = page.getByRole('navigation', { name: 'Main' });
				const entry = nav.getByRole('link', { name: NEW_EVENT.name, exact: true });
				const tools = nav.getByRole('link', { name: NEW_EVENT.tools, exact: true });
				await expect(entry).toBeVisible();
				await expect(tools).toBeVisible();
				await check(`${where}, 1280: Large events`, entry.locator('.dot'));
				await check(`${where}, 1280: Tools`, tools.locator('.dot'));
				// A dot, no number.
				await expect(entry.locator('.dot')).toHaveText('');
				await expect(tools.locator('.dot')).toHaveText('');
				await noSideScroll(page, `${where}, 1280`);
				if (look === 'harbour' && colorScheme === 'light') await page.screenshot({ path: info.outputPath('a133-dot-1280.png') });
				expect(await axeOf(page), `${where}: axe, with the dot`).toEqual([]);
				// The group closed: the heading still shows it.
				await nav.getByRole('button', { name: 'Tools: hide its pages' }).click();
				await expect(entry).toBeHidden();
				await check(`${where}, 1280, Tools closed: Tools`, tools.locator('.dot'));
				await nav.getByRole('button', { name: 'Tools: show its pages' }).click();
				// Shrunk: on Tools' icon, its tooltip saying so.
				await nav.getByRole('button', { name: 'Shrink menu' }).click();
				const icon = nav.getByRole('link', { name: NEW_EVENT.tools, exact: true });
				await check(`${where}, 1280, shrunk: Tools icon`, icon.locator('.dot'));
				await icon.hover();
				await expect(nav.locator('#menu-tools .fresh')).toHaveText('New large event, in the last 4 hours');
				await expect(nav.locator('#menu-tools .flyhead')).toHaveText('Printing for training, capcodes with no alias, and large events');
				await check(`${where}, 1280, shrunk panel: Large events`, nav.getByRole('link', { name: NEW_EVENT.name, exact: true }).locator('.dot'));
				if (look === 'harbour' && colorScheme === 'light') await page.screenshot({ path: info.outputPath('a133-dot-shrunk.png') });
				expect(await axeOf(page), `${where}: axe, shrunk, with the dot`).toEqual([]);
				await nav.getByRole('button', { name: 'Widen the menu' }).click();
				await page.context().close();

				// 375 px: More, and Tools and Large events in it.
				const phone = await (await signedIn(browser, 'user', { viewport: { width: 375, height: 740 }, hasTouch: true, colorScheme })).newPage();
				await standInDot(phone, true);
				await phone.goto('/');
				const bar = phone.getByRole('navigation', { name: 'Main' });
				const more = bar.getByRole('button', { name: NEW_EVENT.more, exact: true });
				await check(`${where}, 375: More`, more.locator('.dot'));
				await noSideScroll(phone, `${where}, 375`);
				await more.click();
				const dialog = phone.getByRole('dialog', { name: 'More' });
				await check(`${where}, 375, More: Tools`, dialog.locator('h3').filter({ has: phone.getByRole('link', { name: NEW_EVENT.tools, exact: true }) }).locator('.dot'));
				await check(`${where}, 375, More: Large events`, dialog.getByRole('link', { name: NEW_EVENT.name, exact: true }).locator('.dot'));
				if (look === 'harbour' && colorScheme === 'light') await phone.screenshot({ path: info.outputPath('a133-dot-375.png') });
				expect(await axeOf(phone), `${where}: axe at 375 px, with the dot`).toEqual([]);
				await phone.context().close();

				// Without the dot: none drawn, the names plain, axe clean.
				const plain = await (await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 }, colorScheme })).newPage();
				await standInDot(plain, false);
				await plain.goto('/');
				const pnav = plain.getByRole('navigation', { name: 'Main' });
				await expect(pnav.getByRole('link', { name: 'Large events', exact: true })).toBeVisible();
				await expect(pnav.getByRole('link', { name: 'Tools', exact: true })).toBeVisible();
				await expect(pnav.locator('.dot')).toHaveCount(0);
				expect(await axeOf(plain), `${where}: axe, without the dot`).toEqual([]);
				await plain.context().close();
			}
		} finally {
			await setLook(request, 'user', 'harbour');
		}
	}
	console.log(`A133 (${info.project.name}): ${report.join('; ')}`);
	expect(failures).toEqual([]);
});

// A133, live: with a second tab open, an event that opens shows the dot in
// both without a reload, and opening the Large events page in one takes it
// away in both (the server keeps what the user has seen).
test('A133 the dot is live in two tabs', async ({ browser, request }) => {
	// Earlier tests' pages may have opened an event within 12 hours: start
	// from none, so this one opens here.
	sql('DELETE FROM large_events;');
	const ctx = await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } });
	const [one, two] = [await ctx.newPage(), await ctx.newPage()];
	await Promise.all([one.goto('/'), two.goto('/')]);
	await Promise.all([waitLive(one), waitLive(two)]);
	const seen = await request.post(`${URL}/api/v1/me/large-events-seen`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
	expect(seen.status()).toBe(200);
	const nav = (p: Page) => p.getByRole('navigation', { name: 'Main' });
	const entry = (p: Page) => nav(p).getByRole('link', { name: NEW_EVENT.name, exact: true });
	for (const p of [one, two]) await expect(nav(p).locator('.dot')).toHaveCount(0);
	const n = ['chromium', 'firefox', 'webkit'].indexOf(test.info().project.name) + 1;
	const start = Date.now();
	await post(request, '0100001', `@@${uniqueJob()} GRS3 DG93${n}1 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN1`);
	for (const p of [one, two]) await expect(entry(p)).toBeVisible({ timeout: 5000 });
	console.log(`A133 (live): the dot showed in both tabs ${Date.now() - start} ms after the page that opened the event`);
	await expect(nav(two).getByRole('link', { name: NEW_EVENT.tools, exact: true })).toBeVisible();
	await entry(one).click();
	await expect(one.getByRole('heading', { level: 1, name: 'Large events' })).toBeVisible();
	for (const p of [one, two]) await expect(nav(p).locator('.dot')).toHaveCount(0, { timeout: 5000 });
	await ctx.close();
});

// A134 (R87c, R88b; owner, 2026-11-23): a large event's areas. The example
// profile's two made-up layers; an event of pages at 3 FICTION ST, MADEUPTON
// (Zone A, Region North) and one at 4 FICTION ST, TESTVILLE (in none), 14
// hours apart. At 1,280 and 375 px, light and dark. (With no layers, no areas
// anywhere: A134's API half, internal/acceptance/a134_test.go.)
test('A134 a large event shows its areas', async ({ browser, request }, info) => {
	test.setTimeout(180_000);
	sql('DELETE FROM large_events;');
	const n = ['chromium', 'firefox', 'webkit'].indexOf(info.project.name) + 1;
	const t0 = Date.now() - 14 * 3600_000;
	const at = (ms: number) => new Date(t0 + ms).toISOString();
	const [j1, j2, j3] = [uniqueJob(), uniqueJob(), uniqueJob()];
	await postAt(request, '0100001', `@@${j1} GRS3 DG94${n}1 GRASS FIRE @ 3 FICTION ST, MADEUPTON #STN1`, at(0));
	await postAt(request, '0100002', `@@${j1} HSF1 SMOKE FROM ROOF @ 3 FICTION ST, MADEUPTON #STN2`, at(60_000));
	await postAt(request, '0100003', `@@${j2} GRS3 DG94${n}2 GRASS FIRE @ 4 FICTION ST, TESTVILLE #STN3`, new Date().toISOString());
	const events = (await (await request.get(`${URL}/api/v1/large-events`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } })).json()).events as { id: number }[];
	expect(events).toHaveLength(2);
	const [second, first] = events;
	const tags = (l: Locator) => l.locator('li');
	let joined = false;
	for (const width of [1280, 375]) {
		for (const colorScheme of ['light', 'dark'] as const) {
			const where = `${width} px, ${colorScheme}`;
			const page = await (await signedIn(browser, 'user', { viewport: { width, height: 900 }, colorScheme, hasTouch: width < 768 })).newPage();
			await page.goto('/large-events');
			const card = (id: number) => page.locator(`[data-event-id="${id}"]`);
			await expect(card(first.id)).toBeVisible();
			const want = joined ? ['Zone A', 'Zone B', 'Region North'] : ['Zone A', 'Region North'];
			await expect(tags(card(first.id).getByTestId('event-areas')), where).toHaveText(want);
			await expect(card(second.id).getByTestId('event-areas')).toHaveCount(0);
			await expect(card(second.id)).not.toContainText(/unknown/i);
			expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), `${where}: sideways scroll`).toBeLessThanOrEqual(0);
			await page.screenshot({ path: info.outputPath(`a134-list-${width}-${colorScheme}.png`) });
			expect(await axeOf(page), `${where}: axe, Large events`).toEqual([]);

			await page.goto(`/large-events/${first.id}`);
			await waitLive(page);
			const areas = page.getByTestId('event-areas');
			await expect(tags(areas), where).toHaveText(want);
			// Each tag edged as its layer is outlined: zones solid, regions dashed.
			await expect(areas.locator('li[data-layer="zones"]').first()).toHaveCSS('border-top-style', 'solid');
			await expect(areas.locator('li[data-layer="regions"]')).toHaveCSS('border-top-style', 'dashed');
			if (!joined) {
				// A page at 5 EXAMPLE ST (Zone B, Region North) joins it: its
				// view follows without a reload, Region North once.
				await postAt(request, '0100004', `@@${j3} GRS3 DG94${n}3 GRASS FIRE @ 5 EXAMPLE ST, MADEUPTON #STN4`, at(120_000));
				await expect(tags(areas), `${where}: after the page joined`).toHaveText(['Zone A', 'Zone B', 'Region North'], { timeout: 5000 });
				joined = true;
			}
			await page.screenshot({ path: info.outputPath(`a134-event-${width}-${colorScheme}.png`) });
			expect(await axeOf(page), `${where}: axe, the event's view`).toEqual([]);
			await page.goto(`/large-events/${second.id}`);
			await expect(page.getByTestId('event-about')).toBeVisible();
			await expect(page.getByTestId('event-areas')).toHaveCount(0);
			await expect(page.getByTestId('event-about')).not.toContainText(/unknown/i);
			await page.context().close();
		}
	}
});

// A140, A141 (R77e, R77f): the app switcher. The run's API is standalone (no
// RADIOARCHIVE_URL); these start a second API on the same database with
// RadioArchive set, for their own run only (scripts/e2e.sh), and stop it
// after, so no worker runs twice while other tests do.
const RADIO = 'http://radio.example.test'; // as RADIOARCHIVE_URL is read: no trailing slash
async function combinedApi(extra: Record<string, string> = {}): Promise<{ url: string; stop: () => Promise<void> }> {
	const bin = process.env.PA_E2E_BIN;
	const port = process.env.PA_E2E_COMBINED_PORT;
	if (!bin || !port) throw new Error('A140: PA_E2E_BIN and PA_E2E_COMBINED_PORT come from scripts/e2e.sh');
	const url = `http://127.0.0.1:${port}`;
	const p = spawn(bin, ['serve'], {
		env: {
			...process.env,
			PROFILES_DIR: process.env.PA_E2E_PROFILES,
			TZ: 'Australia/Melbourne',
			LISTEN_ADDR: `127.0.0.1:${port}`,
			APP_URL: url,
			AUTHENTIK_URL: 'http://127.0.0.1:1',
			OIDC_CLIENT_SECRET: 'unused',
			AUTHENTIK_API_TOKEN: 'unused',
			ALLOWED_EMAIL_DOMAINS: 'example.test',
			INTEGRATION_SECRETS_KEY: '',
			RADIOARCHIVE_URL: RADIO,
			...extra
		},
		stdio: ['ignore', 'ignore', 'pipe']
	});
	let log = '';
	p.stderr.on('data', (d) => (log += d.toString()));
	let exited = false;
	p.on('exit', () => (exited = true));
	for (const until = Date.now() + 60_000; ; await new Promise((r) => setTimeout(r, 200))) {
		if (exited || Date.now() > until) throw new Error(`A140: the combined API didn't start:\n${log.slice(-3000)}`);
		try {
			if ((await fetch(`${url}/healthz`)).ok) break;
		} catch {
			// not yet
		}
	}
	return {
		url,
		stop: async () => {
			// Its log joins the run's, for the P1 check at the end of e2e.sh.
			writeFileSync(`${bin}.log`, log, { flag: 'a' });
			if (exited) return;
			p.kill('SIGTERM');
			await new Promise((r) => p.on('exit', r));
		}
	};
}

/** Nobody marked and the switcher for everyone off: a new install's state. */
const resetSwitcher = () => sql(`UPDATE users SET sees_switcher = false; DELETE FROM settings WHERE key = 'switcher_everyone';`);

async function meOf(request: APIRequestContext, base: string, role: Role) {
	const res = await request.get(`${base}/api/v1/me`, { headers: asRole(role) });
	expect(res.status()).toBe(200);
	return (await res.json()) as { show_switcher: boolean };
}

/** role's message list at base, at width, in colorScheme. */
async function listAt(browser: Browser, base: string, role: Role, width: number, colorScheme: 'light' | 'dark' = 'light') {
	const ctx = await signedIn(browser, role, { viewport: { width, height: width < 768 ? 740 : 900 }, colorScheme });
	await ctx.addCookies([{ name: 'pa_session', value: SESSIONS[role], url: base }]);
	const page = await ctx.newPage();
	await page.goto(`${base}/`);
	await waitLive(page);
	await expect(page.getByRole('button', { name: /^Account menu/ })).toBeVisible();
	return page;
}

test('A140 the app switcher: none standalone, for whom R77f says with RadioArchive set', async ({ browser, request }, info) => {
	test.setTimeout(240_000);
	const switcher = (p: Page) => p.getByRole('banner').getByRole('link', { name: 'Switch to Radio' });
	// Standalone (this run's API): no switcher for anyone, even marked or for
	// everyone, at either width, and no RadioArchive in meta.
	const meta = await (await request.get(`${URL}/api/v1/meta`, { headers: asRole('user') })).json();
	expect(meta.radioarchive_url, 'standalone: no RadioArchive in meta').toBeUndefined();
	try {
		sql(`UPDATE users SET sees_switcher = true WHERE email = 'user@example.test'; INSERT INTO settings (key, value) VALUES ('switcher_everyone', 'true') ON CONFLICT (key) DO UPDATE SET value = 'true';`);
		for (const role of ['user', 'admin'] as const) {
			expect((await meOf(request, URL, role)).show_switcher, `standalone ${role}`).toBe(false);
			for (const width of [1280, 375]) {
				const page = await listAt(browser, URL, role, width);
				await expect(page.locator('[data-app-switcher]'), `standalone ${role} ${width}`).toHaveCount(0);
				await page.context().close();
			}
		}
	} finally {
		resetSwitcher();
	}

	const api = await combinedApi();
	try {
		const base = api.url;
		const cmeta = await (await request.get(`${base}/api/v1/meta`, { headers: asRole('user') })).json();
		expect([cmeta.radioarchive_url, cmeta.radioarchive_name], 'combined: meta names RadioArchive').toEqual([RADIO, 'Radio']);
		// Set, nobody marked: none.
		expect((await meOf(request, base, 'user')).show_switcher, 'unmarked').toBe(false);
		for (const width of [1280, 375]) {
			const page = await listAt(browser, base, 'user', width);
			await expect(page.locator('[data-app-switcher]'), `unmarked ${width}`).toHaveCount(0);
			await page.context().close();
		}
		// Marked: they see it, desktop and phone, light and dark, in the top bar
		// before the account menu; it opens RadioArchive in the same tab.
		sql(`UPDATE users SET sees_switcher = true WHERE email = 'user@example.test';`);
		expect((await meOf(request, base, 'user')).show_switcher, 'marked').toBe(true);
		expect((await meOf(request, base, 'userC')).show_switcher, 'someone else, unmarked').toBe(false);
		for (const width of [1280, 375]) {
			for (const colorScheme of ['light', 'dark'] as const) {
				const where = `marked ${width} ${colorScheme}`;
				const page = await listAt(browser, base, 'user', width, colorScheme);
				const s = switcher(page);
				await expect(s, where).toBeVisible();
				await expect(s).toHaveText('Radio');
				await expect(s).toHaveAttribute('href', RADIO);
				expect(await s.getAttribute('target'), 'the same tab').toBeNull();
				const [sb, ab] = [await s.boundingBox(), await page.getByRole('button', { name: /^Account menu/ }).boundingBox()];
				expect(sb!.x + sb!.width, `${where}: before the account menu`).toBeLessThanOrEqual(ab!.x + 1);
				expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), `${where}: sideways scroll`).toBeLessThanOrEqual(0);
				await page.screenshot({ path: info.outputPath(`a135-switcher-${width}-${colorScheme}.png`) });
				expect(await axeOf(page), `${where}: axe`).toEqual([]);
				if (width === 1280 && colorScheme === 'light') {
					await page.route(`${RADIO}/**`, (r) => r.fulfill({ contentType: 'text/html', body: '<!doctype html><title>Radio stand-in</title><p>Radio stand-in</p>' }));
					await s.click();
					await expect(page).toHaveURL(`${RADIO}/`);
					expect(page.context().pages(), 'no new tab').toHaveLength(1);
				}
				await page.context().close();
			}
		}
		const other = await listAt(browser, base, 'userC', 1280);
		await expect(other.locator('[data-app-switcher]'), 'someone else, unmarked').toHaveCount(0);
		await other.context().close();
		// Everyone on: the unmarked see it too.
		sql(`INSERT INTO settings (key, value) VALUES ('switcher_everyone', 'true');`);
		expect((await meOf(request, base, 'userC')).show_switcher, 'everyone on').toBe(true);
		for (const width of [1280, 375]) {
			const page = await listAt(browser, base, 'userC', width);
			await expect(switcher(page), `everyone on ${width}`).toBeVisible();
			await page.context().close();
		}
	} finally {
		resetSwitcher();
		await api.stop();
	}
});

test('A141 who sees the switcher: the admin controls on Users', async ({ browser, request }, info) => {
	test.setTimeout(240_000);
	const userLabel = 'Test User (user@example.test)';
	// Standalone: neither control.
	{
		const page = await (await signedIn(browser, 'admin', { viewport: { width: 1280, height: 900 } })).newPage();
		await page.goto('/admin/users');
		await expect(page.getByRole('link', { name: userLabel, exact: true })).toBeVisible();
		await expect(page.getByRole('heading', { name: /^Switcher to/ })).toHaveCount(0);
		await expect(page.getByRole('checkbox', { name: 'Show the switcher to everyone' })).toHaveCount(0);
		await expect(page.getByRole('button', { name: /^(Show|Hide) switcher:/ })).toHaveCount(0);
		await page.context().close();
	}
	resetSwitcher();
	const api = await combinedApi();
	try {
		const base = api.url;
		// Only an admin changes them; nobody marked and everyone off to start with.
		expect((await request.put(`${base}/api/v1/admin/switcher`, { headers: asRole('user'), data: { everyone: true } })).status(), 'a user: everyone').toBe(403);
		const users = (await (await request.get(`${base}/api/v1/admin/users`, { headers: asRole('admin') })).json()) as { id: number; email: string; sees_switcher: boolean }[];
		const target = users.find((u) => u.email === 'user@example.test')!;
		expect(users.filter((u) => u.sees_switcher), 'nobody marked').toEqual([]);
		expect((await request.put(`${base}/api/v1/admin/users/${target.id}/switcher`, { headers: asRole('user'), data: { sees_switcher: true } })).status(), 'a user: a mark').toBe(403);
		expect((await (await request.get(`${base}/api/v1/admin/switcher`, { headers: asRole('admin') })).json()).everyone, 'everyone off').toBe(false);

		for (const width of [1280, 375]) {
			for (const colorScheme of ['light', 'dark'] as const) {
				const where = `${width} ${colorScheme}`;
				const ctx = await signedIn(browser, 'admin', { viewport: { width, height: width < 768 ? 740 : 900 }, colorScheme });
				await ctx.addCookies([{ name: 'pa_session', value: SESSIONS.admin, url: base }]);
				const page = await ctx.newPage();
				await page.goto(`${base}/admin/users`);
				await expect(page.getByRole('heading', { name: 'Switcher to Radio' }), where).toBeVisible();
				const everyone = page.getByRole('checkbox', { name: 'Show the switcher to everyone' });
				await expect(everyone).not.toBeChecked();
				const row = page.getByRole('listitem').filter({ has: page.getByRole('link', { name: userLabel, exact: true }) });
				// Mark them: the tag, the mark in the API, and their switcher.
				await row.getByRole('button', { name: `Show switcher: ${userLabel}` }).click();
				await expect(page.getByRole('status')).toHaveText(`${userLabel} sees the switcher to Radio.`);
				await expect(row.getByTestId('sees-switcher')).toHaveText('Sees switcher');
				expect((await meOf(request, base, 'user')).show_switcher, `${where}: marked`).toBe(true);
				await page.screenshot({ path: info.outputPath(`a136-users-${width}-${colorScheme}.png`) });
				expect(await axeOf(page), `${where}: axe`).toEqual([]);
				await row.getByRole('button', { name: `Hide switcher: ${userLabel}` }).click();
				await expect(page.getByRole('status')).toHaveText(`${userLabel} no longer sees the switcher to Radio.`);
				await expect(row.getByTestId('sees-switcher')).toHaveCount(0);
				expect((await meOf(request, base, 'user')).show_switcher, `${where}: unmarked`).toBe(false);
				// Everyone: on, kept after a reload, then off.
				await everyone.check();
				await expect(page.getByRole('status')).toHaveText('Everyone sees the switcher to Radio.');
				expect((await meOf(request, base, 'userC')).show_switcher, `${where}: everyone on`).toBe(true);
				await page.reload();
				await expect(page.getByRole('checkbox', { name: 'Show the switcher to everyone' })).toBeChecked();
				await page.getByRole('checkbox', { name: 'Show the switcher to everyone' }).uncheck();
				await expect(page.getByRole('status')).toHaveText('Only the people marked see the switcher to Radio.');
				expect((await meOf(request, base, 'userC')).show_switcher, `${where}: everyone off`).toBe(false);
				await ctx.close();
			}
		}
	} finally {
		resetSwitcher();
		await api.stop();
	}
});

// A142 (R77d): the frame is ArchiveUx's, and differs from PagerArchive's own
// only as R77d lists. The frame's other checks (A68, A70, A74, A125, A133,
// the version's tag and R77b's notice) run on it unchanged.
test("A142 the frame is ArchiveUx's", async ({ browser }) => {
	const desk = await (await signedIn(browser, 'user', { viewport: { width: 1280, height: 900 } })).newPage();
	await desk.goto('/');
	await waitLive(desk);
	await expect(desk.locator('div.frame > nav.side[aria-label="Main"]'), "ArchiveUx's frame").toHaveCount(1);
	// A count of 0 shows no pill; the name still says it.
	const sheet = desk.getByRole('link', { name: 'Print sheet, 0 messages', exact: true });
	await expect(sheet).toBeVisible();
	await expect(sheet.locator('.count')).toHaveCount(0);
	await desk.context().close();
	// On a phone, More is current on a group's start page, not on a desktop-only page.
	const phone = await (await signedIn(browser, 'user', { viewport: { width: 375, height: 740 } })).newPage();
	const more = phone.getByRole('navigation', { name: 'Main' }).getByRole('button', { name: /^More/ });
	await phone.goto('/tools');
	await expect(more).toHaveAttribute('aria-current', 'page');
	await phone.goto('/sheet');
	await expect(more).not.toHaveAttribute('aria-current', 'page');
	await phone.goto('/account/profile');
	await expect(more).toHaveAttribute('aria-current', 'page');
	await phone.context().close();
});

// A135 (R92, R92a; owner, 2026-11-24; #106): fireground chips. With
// RADIOARCHIVE_URL set (meta's radioarchive_url, stood in here: the test
// API runs standalone), a page naming made-up fireground channels shows a
// chip for each under its unchanged text, in the list and its detail view,
// linking to the radio in a new tab; at 375 px they're on their own line,
// each at least the minimum tap target. Unset, there are none. (The
// numbers' extraction and the API field: internal/acceptance/a135_test.go.)
test('A135 fireground chips link to the radio', async ({ browser, request }, info) => {
	// Two sizes, two views, a new tab each and an axe check per size.
	test.setTimeout(120_000);
	const radio = 'https://radio.example.test';
	const job = uniqueJob();
	const text = `@@${job} GRS3 GRASS FIRE FGND 041 FGND 7 @ SAMPLE RD, MADEUPTON #STN1`;
	await post(request, '0100001', text);
	const res = await request.get(`${URL}/api/v1/messages?job=${job}`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
	const m = (await res.json()).messages[0] as { id: number; fireground_channels: number[] | null };
	expect(m.fireground_channels).toEqual([41, 7]);
	const report: string[] = [];

	for (const [label, viewport] of [
		['desktop', { width: 1280, height: 800 }],
		['phone', { width: 375, height: 740 }]
	] as const) {
		const ctx = await signedIn(browser, 'user', { viewport, hasTouch: label === 'phone' });
		await ctx.route('**/api/v1/meta', async (r) => r.fulfill({ json: { ...(await (await r.fetch()).json()), radioarchive_url: radio } }));
		await ctx.route(`${radio}/**`, (r) => r.fulfill({ contentType: 'text/html', body: '<title>Radio</title><p>Radio</p>' }));
		const page = await ctx.newPage();
		for (const where of ['list', 'detail'] as const) {
			await page.goto(where === 'list' ? `/?job=${job}` : `/messages/${m.id}`);
			const scope = where === 'list' ? page.getByTestId('message-list').locator('article').first() : page.locator('article.detail');
			const textEl = scope.locator('p.text').first();
			await expect(textEl).toHaveText(text.slice(2));
			const chips = scope.getByTestId('fireground').getByRole('link');
			await expect(chips).toHaveCount(2);
			for (const [i, n] of [41, 7].entries()) {
				const chip = chips.nth(i);
				await expect(chip).toHaveAccessibleName(`Listen to fireground ${n} (opens in a new tab)`);
				await expect(chip).toHaveText(`Fireground ${n}`);
				await expect(chip).toHaveAttribute('href', `${radio}/?fireground=${n}`);
				await expect(chip).toHaveAttribute('target', '_blank');
				await expect(chip).toHaveAttribute('rel', /\bnoopener\b/);
			}
			// Under the text, on their own line.
			const t = (await textEl.boundingBox())!;
			const boxes = await Promise.all([0, 1].map(async (i) => (await chips.nth(i).boundingBox())!));
			for (const b of boxes) expect(b.y, `${label} ${where}: a chip under the text`).toBeGreaterThanOrEqual(t.y + t.height - 1);
			if (label === 'phone') {
				const target = await page.evaluate(() => {
					const probe = document.createElement('div');
					probe.style.height = 'var(--target)';
					document.body.append(probe);
					const h = probe.getBoundingClientRect().height;
					probe.remove();
					return h;
				});
				expect(target).toBeGreaterThan(0);
				for (const b of boxes) {
					expect(b.height, `${label} ${where}: chip height`).toBeGreaterThanOrEqual(target - 0.5);
					expect(b.width, `${label} ${where}: chip width`).toBeGreaterThanOrEqual(target - 0.5);
				}
				report.push(`phone ${where}: chips ${boxes.map((b) => `${Math.round(b.width)}x${Math.round(b.height)}`).join(', ')} (target ${target} px)`);
			}
			// Clicking one opens the radio in a new tab, not the detail view.
			const before = page.url();
			const [tab] = await Promise.all([ctx.waitForEvent('page'), chips.first().click()]);
			await tab.waitForLoadState();
			expect(tab.url()).toBe(`${radio}/?fireground=41`);
			await tab.close();
			expect(page.url()).toBe(before);
			if (where === 'list') {
				const axe = await new AxeBuilder({ page }).include('[data-testid="message-list"]').withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
				expect(axe.violations.map((v) => v.id)).toEqual([]);
			}
		}
		await ctx.close();
	}

	// Unset: no chip anywhere.
	const ctx = await signedIn(browser, 'user');
	const page = await ctx.newPage();
	await page.goto(`/?job=${job}`);
	const row = page.getByTestId('message-list').locator('article').first();
	await expect(row.locator('p.text')).toHaveText(text.slice(2));
	await expect(page.getByTestId('fireground')).toHaveCount(0);
	await page.goto(`/messages/${m.id}`);
	await expect(page.locator('article.detail p.text')).toHaveText(text.slice(2));
	await expect(page.getByTestId('fireground')).toHaveCount(0);
	await expect(page.getByRole('link', { name: /fireground/i })).toHaveCount(0);
	await ctx.close();
	console.log(`A135 (${info.project.name}): chips 41 and 7 in the list and detail view with RADIOARCHIVE_URL set, none unset; ${report.join('; ')}`);
});

// A148 (R92b): beside RadioArchive with LOOK_COOKIE_DOMAIN set, the shared
// look cookie decides the first paint, over the account's look, and the page
// never switches when the account loads; the sign-in page follows it too; a
// change in the account menu saves to the account and the cookie (Domain,
// Path=/, a year, SameSite=Lax). Standalone, the cookie changes nothing.
// The cookie is set host-only here (the test's host is an address); what the
// server writes is read from its answer.
test('A148 the shared look cookie decides the first paint', async ({ browser, request }, info) => {
	test.setTimeout(120_000);
	await setLook(request, 'user', 'harbour');
	// Every change of the root's look after the page starts, and the first.
	const watchLook = (page: Page) =>
		page.addInitScript(() => {
			const w = window as unknown as { looks: (string | null)[] };
			w.looks = [];
			new MutationObserver(() => w.looks.push(document.documentElement.getAttribute('data-look'))).observe(document, {
				subtree: true,
				attributes: true,
				attributeFilter: ['data-look']
			});
		});
	const looks = (page: Page) => page.evaluate(() => (window as unknown as { looks: (string | null)[] }).looks);
	const report: string[] = [];

	// Standalone (this run's API): a look cookie is ignored.
	const alone = await signedIn(browser, 'user');
	await alone.addCookies([{ name: 'look', value: 'console', url: URL }]);
	const ap = await alone.newPage();
	await ap.goto('/');
	await waitLive(ap);
	await expect(ap.locator('html')).toHaveAttribute('data-look', 'harbour');
	expect(await ap.locator('html').getAttribute('data-look-shared')).toBeNull();
	await ap.goto('/signin');
	expect(await ap.locator('html').getAttribute('data-look'), 'standalone sign-in page').toBeNull();
	await alone.close();

	const api = await combinedApi({ LOOK_COOKIE_DOMAIN: '127.0.0.1' });
	try {
		const base = api.url;
		const ctx = await newContext(browser, { viewport: { width: 1280, height: 900 } });
		await ctx.addCookies([
			{ name: 'pa_session', value: SESSIONS.user, url: base },
			{ name: 'look', value: 'console', url: base }
		]);
		const page = await ctx.newPage();
		await watchLook(page);
		const res = await page.goto(`${base}/`);
		expect(await res?.text(), 'the page as sent').toMatch(/<html data-look-shared="" data-look="console"/);
		await waitLive(page);
		await expect(page.getByRole('button', { name: /^Account menu/ })).toBeVisible();
		await page.waitForTimeout(500);
		await expect(page.locator('html')).toHaveAttribute('data-look', 'console');
		expect(await looks(page), 'the look never changed as the page loaded').toEqual([]);
		report.push('cookie console over the account harbour: sent and kept as console');

		// A change in the account menu: at once, saved to the account and the cookie.
		const saved = page.waitForResponse((r) => r.url() === `${base}/api/v1/me` && r.request().method() === 'PATCH');
		await page.getByRole('button', { name: /^Account menu/ }).click();
		const choice = page.getByRole('combobox', { name: 'Look', exact: true });
		await expect(choice).toHaveValue('console');
		await choice.selectOption('harbour');
		await expect(page.locator('html')).toHaveAttribute('data-look', 'harbour');
		const setCookie = (await (await saved).headerValue('set-cookie')) ?? '';
		for (const part of ['look=harbour', 'Domain=127.0.0.1', 'Path=/', 'Max-Age=31536000', 'SameSite=Lax'])
			expect(setCookie, `the change's Set-Cookie has ${part}`).toContain(part);
		expect(setCookie).not.toMatch(/HttpOnly/i);
		expect((await meOf(request, base, 'user')) as unknown as { look: string }).toMatchObject({ look: 'harbour' });
		report.push(`a change saved to the account and wrote ${setCookie}`);

		// The sign-in page, before sign-in, follows the cookie too.
		const out = await newContext(browser);
		await out.addCookies([{ name: 'look', value: 'console', url: base }]);
		const sp = await out.newPage();
		await sp.goto(`${base}/signin`);
		await expect(sp.getByRole('heading', { name: 'Sign in' })).toBeVisible();
		await expect(sp.locator('html')).toHaveAttribute('data-look', 'console');
		report.push('the sign-in page in the cookie\'s console');
		await out.close();
		await ctx.close();
	} finally {
		await api.stop();
		await setLook(request, 'user', 'harbour');
	}
	console.log(`A148 (${info.project.name}): standalone ignores the cookie; ${report.join('; ')}`);
});

// A153 (R36a, ADR 0053): the page's policy runs no inline script but the
// build's own. Signed out at the root URL, the header's script-src is this
// origin and the hash of each inline script the page as built has, no
// 'unsafe-inline' or 'unsafe-eval'; neither the page nor the offline page has
// an inline event handler or a javascript: link; and the app boots to the
// sign-in page. Then, as a user and as an admin, it boots and opens the main
// screens. No violation fires on any of them: afterEach checks every test's
// pages, A116's with the service worker in control and offline among them.
test("A153 no inline script but the build's", async ({ browser, request }, info) => {
	const out = await (await newContext(browser)).newPage();
	const res = await out.goto('/');
	// Authentik isn't running here, so the sign-in page says it can't be reached.
	await expect(out).toHaveURL(/\/signin(\?|$)/);
	await expect(out.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible();
	const scriptSrc = (res!.headers()['content-security-policy'] ?? '')
		.split(';')
		.map((d) => d.trim())
		.find((d) => d.startsWith('script-src '));
	expect(scriptSrc).toMatch(/^script-src 'self'( 'sha256-[A-Za-z0-9+/]{43}=')+$/);
	// The hashes are the page's own inline scripts', worked out here. (The
	// page itself, as sent: the browser has moved on to the sign-in page.)
	const html = await (await out.context().request.get(`${URL}/`)).text();
	const inline = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script\s*>/gi)].filter((m) => !/(^|\s)src\s*=/i.test(m[1]));
	expect(inline.length).toBeGreaterThan(0);
	const hashes = [...new Set(inline.map((m) => `'sha256-${createHash('sha256').update(m[2].replace(/\r\n?/g, '\n')).digest('base64')}'`))];
	expect(scriptSrc).toBe(`script-src 'self' ${hashes.join(' ')}`);
	for (const [path, text] of [
		['/', html],
		['/offline.html', await (await request.get(`${URL}/offline.html`)).text()]
	]) {
		expect(text.match(/<[^>]*\son[a-z]+\s*=/gi), `inline event handlers in ${path}`).toBeNull();
		expect(text.match(/javascript:/gi), `javascript: links in ${path}`).toBeNull();
	}
	await out.context().close();

	// Signed in, the app boots and its screens open.
	const job = uniqueJob();
	await post(request, '0100001', `@@${job} HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON #STN1`);
	const found = await request.get(`${URL}/api/v1/messages?job=${job}`, { headers: { Cookie: `pa_session=${SESSIONS.user}` } });
	const id = ((await found.json()).messages[0] as { id: number }).id;
	const opened: string[] = [];
	for (const role of ['user', 'admin'] as const) {
		const page = await (await signedIn(browser, role)).newPage();
		await page.goto('/');
		await expect(page.getByTestId('message-list')).toContainText(job);
		await page.goto(`/messages/${id}`);
		await expect(page.getByText(job).first()).toBeVisible();
		await page.goto('/map');
		await expect(page.getByTestId('map')).toHaveAttribute('data-ready', /^(true|unavailable)$/, { timeout: 15_000 });
		await page.goto('/reports');
		await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
		if (role === 'admin') {
			await page.goto('/admin');
			await expect(page.getByRole('heading', { level: 1, name: 'Administration' })).toBeVisible();
		}
		opened.push(role);
		await page.context().close();
	}
	console.log(`A153 (${info.project.name}): ${scriptSrc}; booted signed out and as ${opened.join(' and ')}`);
});
