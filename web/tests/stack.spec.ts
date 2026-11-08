/// <reference types="node" />
// SPEC.md section 11, step 2, in a real browser against the full compose
// stack (scripts/stack-e2e.sh): an admin is created, invites a test user,
// and the test user enrols with a code sent to an allowed test domain,
// caught by Mailpit. Authentik's own pages are used, as a person would. Then
// A81 (ADR 0021): link scanners, resending, and two browsers finishing at once.
import { test, expect, type Browser, type Page, type APIRequestContext } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { execFileSync } from 'node:child_process';
import { join } from 'node:path';

const MAILPIT = process.env.PA_MAILPIT_URL ?? 'http://localhost:8025';
const DOMAIN = process.env.PA_TEST_DOMAIN ?? 'example.test';
const OWNER = process.env.PA_OWNER_EMAIL ?? `owner@${DOMAIN}`;
// The made-up test icons (ADR 0011).
const testIcon = (name: string) => join(process.cwd(), '..', 'testdata', 'icons', name);

/** The text of the newest email to address after since whose subject has subject. */
async function mail(request: APIRequestContext, address: string, subject: string, since: Date): Promise<string> {
	const deadline = Date.now() + 30_000;
	while (Date.now() < deadline) {
		const res = await request.get(`${MAILPIT}/api/v1/search`, { params: { query: `to:"${address}"` } });
		const list = (await res.json()) as { messages: { ID: string; Created: string; Subject: string }[] };
		const m = list.messages.find((m) => new Date(m.Created) > since && m.Subject.includes(subject));
		if (m) return ((await (await request.get(`${MAILPIT}/api/v1/message/${m.ID}`)).json()) as { Text: string }).Text;
		await new Promise((r) => setTimeout(r, 300));
	}
	throw new Error(`no "${subject}" email to ${address}`);
}

/** The password enrol sets for someone of this name. */
const passwordOf = (name: string) => `correct horse ${name.toLowerCase()} staple`;

/** Fills in Authentik's enrolment pages: name and password, then the emailed code. */
async function enrol(page: Page, request: APIRequestContext, email: string, name: string) {
	const since = new Date(Date.now() - 2000);
	const password = passwordOf(name);
	await page.locator('input[name="name"]').fill(name);
	await page.locator('input[name="password"]').fill(password);
	await page.locator('input[name="password_repeat"]').fill(password);
	await page.locator('button[type="submit"]').click();
	const code = /\b\d{6}\b/.exec(await mail(request, email, 'code', since))?.[0];
	expect(code, 'code in the email').toBeTruthy();
	await page.locator('input[name="code"]').fill(code!);
	await page.locator('button[type="submit"]').click();
}

/**
 * Answers one of Authentik's flow pages. The page redraws while its loading
 * overlay is up, which empties a field filled in before that.
 */
async function answer(page: Page, field: string, value: string) {
	const input = page.locator(`input[name="${field}"]`);
	await expect(input).toBeVisible({ timeout: 30_000 });
	await expect(page.locator('ak-loading-overlay')).toHaveCount(0, { timeout: 30_000 });
	await input.fill(value);
	await page.locator('button[type="submit"]').click();
}

test('§11 steps 2 and 3: the first admin invites a test user, who enrols; a client feeds pages they see live, parsed and on the map; A57 icons', async ({ browser, request, baseURL }) => {
	test.setTimeout(180_000);
	const app = new URL(baseURL!).origin;

	// The first admin (INITIAL_ADMIN_EMAIL) sets up their account, starting
	// from the app's address as a person would: on a new install, a signed-out
	// visitor is sent to the sign-in page, which offers the set-up.
	const admin = await (await browser.newContext()).newPage();
	await admin.goto('/');
	await admin.waitForURL((u) => u.origin === app && u.pathname === '/signin');
	// Authentik's page draws its form when the flow executor answers: wait
	// for that answer, not a fixed time (it can be slow after a cold start).
	const flowAnswered = admin.waitForResponse((r) => r.url().includes('/api/v3/flows/executor/') && r.request().method() === 'GET', { timeout: 120_000 });
	await admin.getByRole('button', { name: 'Set up the first admin' }).click();
	await flowAnswered;
	await expect(admin.locator('input[name="password"]')).toBeVisible();
	await enrol(admin, request, OWNER, 'Owner Person');
	await admin.waitForURL((u) => u.origin === app && u.pathname === '/');
	await expect(admin.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Admin' })).toBeVisible();
	await admin.screenshot({ path: test.info().outputPath('1-admin-signed-in.png') });

	// Once an admin exists, a signed-out visitor goes straight to Authentik.
	const visitor = await (await browser.newContext()).newPage();
	await visitor.goto('/');
	await visitor.waitForURL((u) => u.origin !== app && u.pathname.startsWith('/if/flow/'));

	// R44c, A78: from that visitor's fresh browser, the first admin signs in
	// with the address and password they enrolled with, and is the admin.
	await answer(visitor, 'uidField', OWNER);
	await answer(visitor, 'password', passwordOf('Owner Person'));
	await visitor.waitForURL((u) => u.origin === app && u.pathname === '/', { timeout: 30_000 });
	await expect(visitor.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Admin' })).toBeVisible();
	await visitor.goto('/account/profile');
	await expect(visitor.locator('dl')).toContainText(OWNER);
	await visitor.context().close();

	// R44c, A78: from another fresh browser, forgot-password reaches the code
	// step for that account, and the code goes to their address.
	const forgetful = await (await browser.newContext()).newPage();
	await forgetful.goto('/');
	await forgetful.waitForURL((u) => u.origin !== app && u.pathname.startsWith('/if/flow/'));
	await forgetful.getByRole('link', { name: /forgot/i }).click();
	const resetAsked = new Date(Date.now() - 2000);
	await answer(forgetful, 'uidField', OWNER);
	await expect(forgetful.locator('input[name="code"]')).toBeVisible();
	expect(/\b\d{6}\b/.exec(await mail(request, OWNER, 'code', resetAsked))?.[0], 'code in the reset email').toBeTruthy();
	await forgetful.context().close();
	console.log('R44c: a fresh browser signed in as the first admin; forgot-password reached the code step for that account');

	// The admin invites the test user.
	const tester = `tester@${DOMAIN}`;
	const since = new Date(Date.now() - 2000);
	await admin.goto('/account/invite'); // its own page since M13 (ADR 0015)
	await admin.getByLabel('Their email address').fill(tester);
	await admin.getByRole('button', { name: 'Send invitation' }).click();
	await expect(admin.getByRole('status').filter({ hasText: `Invitation sent to ${tester}` })).toBeVisible();
	const link = /https?:\/\/\S+itoken=[0-9a-f-]+/.exec(await mail(request, tester, 'invited', since))?.[0];
	expect(link, 'invitation link in the email').toBeTruthy();

	// The test user follows the link and enrols with the emailed code.
	const user = await (await browser.newContext()).newPage();
	await user.goto(link!);
	await enrol(user, request, tester, 'Test Person');
	await user.waitForURL((u) => u.origin === app && u.pathname === '/');
	await expect(user.getByRole('status').filter({ hasText: 'Live' })).toBeVisible();
	await user.goto('/account/profile');
	await expect(user.locator('dl')).toContainText(tester);
	await expect(user.locator('dl')).toContainText('User');
	await expect(user.locator('dl')).toContainText(`Owner Person (${OWNER})`);
	await expect(user.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Admin' })).toHaveCount(0);
	await user.screenshot({ path: test.info().outputPath('2-test-user-signed-in.png') });

	// The admin sees them, invited by the admin.
	await admin.goto('/admin/users');
	await expect(admin.locator('li').filter({ hasText: tester })).toContainText(`Invited by Owner Person (${OWNER})`);
	console.log(`§11 step 2: ${OWNER} set up as admin, invited ${tester}, who enrolled with the emailed code and is signed in as a user`);

	// §11 step 3: a client container is fed fixture lines through stdin (no
	// radio). Each page appears in the signed-in browser within 2 seconds,
	// with its parsed fields, grouping and a map point.
	await user.goto('/');
	await expect(user.getByRole('status').filter({ hasText: 'Live' })).toBeVisible();
	const golden = [
		[100001, '@@EX0000001 HSF1 SMOKE FROM ROOF @ 12 FICTION ST, MADEUPTON GRID 123456 UNITS T1 T2 #STN1'],
		[100002, '@@EX0000002 GRS3 GRASS FIRE @ SAMPLE RD, MADEUPTON #STN2'],
		[100001, 'HbRE EX0000001 CREWS RETURNING #STN1'],
		[100001, 'QDTraining Tuesday 7pm at the station']
	] as const;
	const lines = [
		...golden.map(([capcode, text]) => `POCSAG512: Address:  ${capcode}  Function: 0  Alpha:   ${text}`),
		// A1: every group's copy, one job.
		...[1, 2, 3, 4].map((n) => `POCSAG512: Address:  10000${n}  Function: 0  Alpha:   @@EX0000004 HSF1 SMOKE FROM SHED @ 3 FICTION ST, MADEUPTON #STN${n}`)
	];
	execFileSync('docker', ['run', '-i', '--rm', '--network', process.env.PA_NETWORK!, '-e', 'SERVER_URL=http://api:8080',
		'-e', 'CLIENT_ID=section11', '-e', `CLIENT_KEY=${process.env.PA_CLIENT_KEY}`, process.env.PA_CLIENT_IMAGE!], { input: lines.join('\n') + '\n' });
	const posted = Date.now(); // the client has posted every line and exited
	const list = user.getByTestId('message-list');
	await expect(list.locator('article')).toHaveCount(lines.length, { timeout: 2000 });
	console.log(`§11 step 3: all ${lines.length} pages shown ${Date.now() - posted} ms after the client finished posting`);
	await expect(list.locator('article').filter({ hasText: 'SMOKE FROM ROOF' })).not.toContainText('@@');

	// Parsed fields (golden row 1).
	await list.locator('article').filter({ hasText: 'SMOKE FROM ROOF' }).locator('a.open').click();
	const details = user.locator('dl').first();
	for (const v of ['EX0000001', 'HSF1', 'SMOKE FROM ROOF', '12 FICTION ST', 'MADEUPTON', '123456', 'T1, T2', 'STN1']) await expect(details).toContainText(v);
	await expect(user.getByTestId('location')).toContainText('From the address');
	// Grouping: the follow-up joins job EX0000001, and A1's copies are one job.
	await user.goto('/jobs/EX0000001');
	await expect(user.locator('main article')).toHaveCount(2);
	await user.goto('/jobs/EX0000004');
	await expect(user.locator('main article')).toHaveCount(4);
	// Map points for the addresses in the example address dataset.
	await user.goto('/map');
	await expect(user.getByTestId('map')).toHaveAttribute('data-ready', 'true', { timeout: 15_000 });
	const located = user.getByTestId('located-list');
	await expect(located.locator('li')).toHaveCount(6); // rows 1 and 2, and A1's 4 copies
	await expect(located.locator('li').filter({ hasText: 'GRASS FIRE' })).toContainText('Street (approximate)');
	await user.waitForTimeout(1500); // let the map finish drawing for the screenshot
	await user.screenshot({ path: test.info().outputPath('3-map.png') });
	console.log('§11 step 3: parsed fields, job grouping and 6 map points checked');

	// A57 on the full stack (R65, R66): the admin uploads the made-up test
	// icon to "Example Fire" in the browser; an SVG, a 200 KB PNG and a text
	// file renamed .png are refused with the reason. The test user's list
	// shows the icon beside "Station 1" with "Example Fire Service" as its
	// text alternative, in light and dark mode (axe passes), and the page
	// requests nothing from anywhere but PagerArchive.
	await admin.goto('/admin/templates');
	await admin.getByTestId('template-table').getByRole('link', { name: 'Example Fire' }).click();
	await admin.getByLabel('Add an icon').setInputFiles(testIcon('example-icon.png'));
	await admin.getByRole('button', { name: 'Upload icon' }).click();
	const current = admin.getByTestId('current-icon').getByRole('img', { name: 'Example Fire Service' });
	await expect(current).toBeVisible();
	const src = (await current.getAttribute('src'))!;
	for (const [file, reason] of [
		['example.svg', "SVG icons aren't accepted"],
		['large-200kb.png', 'An icon file can be at most 100 KB'],
		['not-an-image.png', 'Icons must be PNG, ICO, GIF, JPEG or WebP images']
	]) {
		await admin.getByLabel('Replace it with').setInputFiles(testIcon(file));
		await admin.getByRole('button', { name: 'Replace icon' }).click();
		await expect(admin.getByTestId('icon-error'), file).toContainText(reason);
		await expect(current, file).toHaveAttribute('src', src);
	}
	const origins = new Set<string>();
	user.on('request', (r) => {
		if (!r.url().startsWith('data:')) origins.add(new URL(r.url()).origin);
	});
	for (const scheme of ['light', 'dark'] as const) {
		await user.emulateMedia({ colorScheme: scheme });
		await user.goto('/');
		await expect(user.getByRole('status').filter({ hasText: 'Live' })).toBeVisible();
		const row = list.locator('article').filter({ hasText: 'SMOKE FROM ROOF' });
		const img = row.getByRole('img', { name: 'Example Fire Service' });
		await expect(img).toHaveAttribute('src', src);
		await expect.poll(() => img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth)).toBe(64);
		await expect(img.locator('xpath=../following-sibling::a[1]')).toHaveText('Station 1');
		expect(await img.locator('..').evaluate((t) => getComputedStyle(t).backgroundColor)).toBe('rgb(255, 255, 255)');
		const results = await new AxeBuilder({ page: user }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze();
		expect(results.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(' ')).join('; ')}`), scheme).toEqual([]);
		await user.screenshot({ path: test.info().outputPath(`4-icons-${scheme}.png`) });
	}
	expect([...origins], 'origins the page requested').toEqual([app]);
	console.log(`A57: icon ${src} shown beside Station 1 in light and dark mode; 3 files refused; origins requested: ${[...origins].join(' ')}`);
});

/** Signs in on Authentik's pages, from the app's address, in a fresh browser. */
async function signIn(browser: Browser, app: string, email: string, name: string): Promise<Page> {
	const page = await (await browser.newContext()).newPage();
	await page.goto('/');
	await page.waitForURL((u) => u.origin !== app && u.pathname.startsWith('/if/flow/'));
	await answer(page, 'uidField', email);
	await answer(page, 'password', passwordOf(name));
	await page.waitForURL((u) => u.origin === app && u.pathname === '/', { timeout: 30_000 });
	return page;
}

/** The invitation link in an email. */
function linkIn(text: string): string {
	const link = /https?:\/\/\S+itoken=[0-9a-f-]+/.exec(text)?.[0];
	expect(link, 'invitation link in the email').toBeTruthy();
	return link!;
}

/** Sends an invitation from the invite page and returns the emailed link. */
async function inviteFrom(page: Page, request: APIRequestContext, address: string): Promise<string> {
	const since = new Date(Date.now() - 2000);
	await page.goto('/account/invite');
	await page.getByLabel('Their email address').fill(address);
	await page.getByRole('button', { name: 'Send invitation' }).click();
	await expect(page.getByRole('status').filter({ hasText: `Invitation sent to ${address}` })).toBeVisible();
	return linkIn(await mail(request, address, 'invited', since));
}

/** Fills in the enrolment page's name and password, and waits for the code page. */
async function toCodePage(page: Page, name: string) {
	await expect(page.locator('input[name="name"]')).toBeVisible({ timeout: 30_000 });
	await page.locator('input[name="name"]').fill(name);
	await page.locator('input[name="password"]').fill(passwordOf(name));
	await page.locator('input[name="password_repeat"]').fill(passwordOf(name));
	await page.locator('button[type="submit"]').click();
	await expect(page.locator('input[name="code"]')).toBeVisible({ timeout: 30_000 });
}

/**
 * A mail provider's link scanner: a headless browser with no cookies, from
 * its own address, opens the link and runs the enrolment page as far as it
 * goes without the code. Authentik takes the address from X-Forwarded-For,
 * because the browser reaches it through Docker's bridge, a trusted proxy.
 */
async function scan(browser: Browser, link: string, from: string) {
	const ctx = await browser.newContext({ extraHTTPHeaders: { 'X-Forwarded-For': from } });
	const page = await ctx.newPage();
	await page.goto(link);
	await toCodePage(page, 'Link Scanner');
	await ctx.close();
}

/** A replaced or used link is refused: Authentik doesn't offer enrolment. */
async function refused(browser: Browser, link: string, what: string) {
	const ctx = await browser.newContext();
	const page = await ctx.newPage();
	await page.goto(link);
	await expect(page.getByText(/invalid invite|not found/i).first(), what).toBeVisible({ timeout: 30_000 });
	await expect(page.locator('input[name="name"]'), what).toHaveCount(0);
	await ctx.close();
}

/** Runs SQL in the stack's database: the app's (postgres) or Authentik's. */
function sql(database: string, statement: string) {
	execFileSync('docker', ['compose', '-f', '../compose.yaml', '-f', '../deploy/test/compose.yaml', 'exec', '-T', 'db',
		'psql', '-q', '-v', 'ON_ERROR_STOP=1', '-U', 'postgres', '-d', database, '-c', statement]);
}

/** The row for address in a list of invitations or users. */
const rowFor = (page: Page, address: string) => page.locator('li').filter({ hasText: address });

type Listed = { id: number; email: string; status: string; expires_at: string };

// Runs after the test above, which made the first admin and the test user.
test('A81: invitations survive link scanners, and resending (R43a, R43d)', async ({ browser, request, baseURL }) => {
	test.setTimeout(300_000);
	const app = new URL(baseURL!).origin;
	const admin = await signIn(browser, app, OWNER, 'Owner Person');

	// User A.
	const a = `a81-a@${DOMAIN}`;
	const aPage = await (await browser.newContext()).newPage();
	await aPage.goto(await inviteFrom(admin, request, a));
	await enrol(aPage, request, a, 'Inviter A');
	await aPage.waitForURL((u) => u.origin === app && u.pathname === '/');

	// A invites g. A link scanner runs the page twice, from two addresses.
	const g = `g@${DOMAIN}`;
	const gLink = await inviteFrom(aPage, request, g);
	await scan(browser, gLink, '192.0.2.10');
	await scan(browser, gLink, '198.51.100.20');
	await aPage.goto('/account/invite');
	await expect(rowFor(aPage, g), "still waiting in A's list after the scans").toContainText('Waiting');
	// G opens the same link, enters the emailed code and enrols, A as inviter.
	const gPage = await (await browser.newContext()).newPage();
	await gPage.goto(gLink);
	await enrol(gPage, request, g, 'Person G');
	await gPage.waitForURL((u) => u.origin === app && u.pathname === '/', { timeout: 30_000 });
	await gPage.goto('/account/profile');
	await expect(gPage.locator('dl')).toContainText(`Inviter A (${a})`);
	await aPage.goto('/account/invite');
	await expect(rowFor(aPage, g)).toContainText('Joined');
	await refused(browser, gLink, 'a used link');
	console.log('A81: two scans from two addresses left the invitation waiting; G enrolled with the same link, A recorded as inviter; the used link is refused');

	// A's invitation to h expires (in the app and in Authentik); A resends it.
	const h = `h@${DOMAIN}`;
	const hOld = await inviteFrom(aPage, request, h);
	sql('postgres', `UPDATE invitations SET expires_at = now() - interval '1 minute' WHERE email = '${h}'`);
	sql('authentik', `UPDATE authentik_stages_invitation_invitation SET expires = now() - interval '1 minute' WHERE fixed_data->>'email' = '${h}'`);
	await aPage.goto('/account/invite');
	await expect(rowFor(aPage, h)).toContainText('Expired');
	const resent = new Date(Date.now() - 2000);
	await aPage.getByRole('button', { name: `Resend: invitation to ${h}` }).click();
	await expect(aPage.getByRole('status').filter({ hasText: `sent to ${h} again` })).toBeVisible();
	const hNew = linkIn(await mail(request, h, 'invited', resent));
	expect(hNew, 'a new link').not.toBe(hOld);
	await expect(rowFor(aPage, h), 'one invitation to h').toHaveCount(1);
	await expect(rowFor(aPage, h)).toContainText('Waiting');
	const mine = (await (await aPage.request.get('/api/v1/invitations')).json()) as Listed[];
	const hRows = mine.filter((i) => i.email === h);
	expect(hRows.map((i) => i.status)).toEqual(['pending']);
	const days = (new Date(hRows[0].expires_at).getTime() - Date.now()) / 86_400_000;
	expect(days, 'a new 7 days').toBeGreaterThan(6.99);
	expect(days).toBeLessThanOrEqual(7);
	await refused(browser, hOld, 'the old link after a resend');
	const hPage = await (await browser.newContext()).newPage();
	await hPage.goto(hNew);
	await enrol(hPage, request, h, 'Person H');
	await hPage.waitForURL((u) => u.origin === app && u.pathname === '/', { timeout: 30_000 });
	await hPage.goto('/account/profile');
	await expect(hPage.locator('dl')).toContainText(`Inviter A (${a})`);
	console.log('A81: the expired invitation to h was resent: a new link for 7 days, the old one refused, one waiting invitation, H enrolled with A as inviter');

	// The admin resends B's waiting invitation from B's page in Administration.
	const b = `tester@${DOMAIN}`;
	const bPage = await signIn(browser, app, b, 'Test Person');
	const j = `j@${DOMAIN}`;
	const jOld = await inviteFrom(bPage, request, j);
	await admin.goto('/admin/users');
	await rowFor(admin, b).first().getByRole('link').first().click();
	await admin.waitForURL((u) => /^\/admin\/users\/\d+$/.test(u.pathname));
	const jResent = new Date(Date.now() - 2000);
	await admin.getByRole('button', { name: `Resend: invitation to ${j}` }).click();
	await expect(admin.getByRole('status').filter({ hasText: `sent to ${j} again` })).toBeVisible();
	const jNew = linkIn(await mail(request, j, 'invited', jResent));
	expect(jNew).not.toBe(jOld);
	await bPage.goto('/account/invite');
	await expect(rowFor(bPage, j), "still in B's list").toContainText('Waiting');
	await admin.goto('/admin/users');
	await expect(rowFor(admin, j)).toContainText('From Test Person');
	await refused(browser, jOld, "B's old link after the admin's resend");
	console.log("A81: the admin resent B's invitation: B is still its inviter, B's old link is refused");

	// A can't resend a used invitation.
	await aPage.goto('/account/invite');
	await expect(rowFor(aPage, g)).toContainText('Joined');
	await expect(aPage.getByRole('button', { name: `Resend: invitation to ${g}` })).toHaveCount(0);
	const used = mine.find((i) => i.email === g)!;
	const res = await aPage.request.post(`/api/v1/invitations/${used.id}/resend`);
	expect(res.status(), 'resending a used invitation').toBe(409);
	console.log('A81: resending a used invitation is refused');

	// R43a: finishing enrolment twice, from two browsers at once, makes one account.
	const k = `k@${DOMAIN}`;
	const kLink = await inviteFrom(aPage, request, k);
	const racers: Page[] = [];
	const codes: string[] = [];
	for (const name of ['Racer One', 'Racer Two']) {
		const p = await (await browser.newContext()).newPage();
		const since = new Date(Date.now() - 500);
		await p.goto(kLink);
		await toCodePage(p, name);
		codes.push(/\b\d{6}\b/.exec(await mail(request, k, 'code', since))![0]);
		racers.push(p);
		await new Promise((r) => setTimeout(r, 1100)); // so the next code's email is newer
	}
	expect(codes[0], 'each browser has its own code').not.toBe(codes[1]);
	await Promise.all(racers.map((p, i) => p.locator('input[name="code"]').fill(codes[i])));
	await Promise.all(racers.map((p) => p.locator('button[type="submit"]').click()));
	const outcomes = await Promise.allSettled(racers.map((p) => p.waitForURL((u) => u.origin === app && u.pathname === '/', { timeout: 30_000 })));
	expect(outcomes.filter((o) => o.status === 'fulfilled'), 'one browser finishes enrolment').toHaveLength(1);
	await admin.goto('/admin/users');
	await expect(rowFor(admin, k), 'one account for k').toHaveCount(1);
	console.log('A81: two browsers finishing one invitation at once made one account; the other was refused');
});
