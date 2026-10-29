/// <reference types="node" />
// SPEC.md section 11, step 2, in a real browser against the full compose
// stack (scripts/stack-e2e.sh): an admin is created, invites a test user,
// and the test user enrols with a code sent to an allowed test domain,
// caught by Mailpit. Authentik's own pages are used, as a person would.
import { test, expect, type Page, type APIRequestContext } from '@playwright/test';
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

/** Fills in Authentik's enrolment pages: name and password, then the emailed code. */
async function enrol(page: Page, request: APIRequestContext, email: string, name: string) {
	const since = new Date(Date.now() - 2000);
	const password = `correct horse ${name.toLowerCase()} staple`;
	await page.locator('input[name="name"]').fill(name);
	await page.locator('input[name="password"]').fill(password);
	await page.locator('input[name="password_repeat"]').fill(password);
	await page.locator('button[type="submit"]').click();
	const code = /\b\d{6}\b/.exec(await mail(request, email, 'code', since))?.[0];
	expect(code, 'code in the email').toBeTruthy();
	await page.locator('input[name="code"]').fill(code!);
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
	await visitor.context().close();

	// The admin invites the test user.
	const tester = `tester@${DOMAIN}`;
	const since = new Date(Date.now() - 2000);
	await admin.goto('/account');
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
	await user.goto('/account');
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
