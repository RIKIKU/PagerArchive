/// <reference types="node" />
// SPEC.md section 11, step 2, in a real browser against the full compose
// stack (scripts/stack-e2e.sh): an admin is created, invites a test user,
// and the test user enrols with a code sent to an allowed test domain,
// caught by Mailpit. Authentik's own pages are used, as a person would.
import { test, expect, type Page, type APIRequestContext } from '@playwright/test';

const MAILPIT = process.env.PA_MAILPIT_URL ?? 'http://localhost:8025';
const DOMAIN = process.env.PA_TEST_DOMAIN ?? 'example.test';
const OWNER = process.env.PA_OWNER_EMAIL ?? `owner@${DOMAIN}`;

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

test('§11 step 2: the first admin invites a test user, who enrols with an emailed code', async ({ browser, request, baseURL }) => {
	test.setTimeout(180_000);
	const app = new URL(baseURL!).origin;

	// The first admin (INITIAL_ADMIN_EMAIL) sets up their account.
	const admin = await (await browser.newContext()).newPage();
	await admin.goto('/signin');
	await admin.getByRole('link', { name: 'Set up the first admin' }).click();
	await expect(admin.locator('input[name="password"]')).toBeVisible();
	await enrol(admin, request, OWNER, 'Owner Person');
	await admin.waitForURL((u) => u.origin === app && u.pathname === '/');
	await expect(admin.getByRole('link', { name: 'Users' })).toBeVisible();
	await admin.screenshot({ path: test.info().outputPath('1-admin-signed-in.png') });

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
	await expect(user.getByRole('link', { name: 'Users' })).toHaveCount(0);
	await user.screenshot({ path: test.info().outputPath('2-test-user-signed-in.png') });

	// The admin sees them, invited by the admin.
	await admin.goto('/admin/users');
	await expect(admin.locator('li').filter({ hasText: tester })).toContainText(`Invited by Owner Person (${OWNER})`);
	console.log(`§11 step 2: ${OWNER} set up as admin, invited ${tester}, who enrolled with the emailed code and is signed in as a user`);
});
