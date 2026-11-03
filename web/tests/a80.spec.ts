/// <reference types="node" />
// A80 in a browser (scripts/a80.sh): what an admin sees in Administration about
// the backups, from the app's address through Authentik's sign-in, as a person
// would. PA_A80_EXPECT says what the backups are doing: "failed" or "ok".
import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';

const OWNER = process.env.PA_OWNER_EMAIL ?? 'owner@example.org';
// internal/stacktest's password for this address, set by A77's seed.
const PASSWORD = `correct horse ${OWNER.replace('@', ' at ')}`;

test('Administration shows whether the last backup worked', async ({ page, baseURL }) => {
	test.setTimeout(120_000);
	const app = new URL(baseURL!).origin;
	await page.goto('/');
	await page.waitForURL((u) => u.origin !== app && u.pathname.startsWith('/if/flow/'), { timeout: 60_000 });
	await page.locator('input[name="uidField"]').fill(OWNER);
	const password = page.locator('input[name="password"]');
	if (!(await password.isVisible())) await page.locator('button[type="submit"]').click();
	// On cld01 a single try left the field empty and the page waiting: try again.
	await expect(async () => {
		await password.fill(PASSWORD);
		await page.locator('button[type="submit"]').click();
		await page.waitForURL((u) => u.origin === app && u.pathname === '/', { timeout: 10_000 });
	}).toPass({ timeout: 60_000 });

	await page.goto('/admin');
	const screens = page.getByTestId('admin-screens');
	await expect(screens.getByRole('link', { name: 'Backups' })).toBeVisible();
	const alert = page.getByTestId('backup-alert');
	if (process.env.PA_A80_EXPECT === 'failed') {
		await expect(alert).toContainText("The last backup failed: the database couldn't be reached");
		await expect(alert).toContainText('The last good backup was');
		await page.screenshot({ path: test.info().outputPath('backup-failed.png') });
		expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()).violations).toEqual([]);
		await alert.getByRole('link', { name: 'Backups' }).click();
		await expect(page).toHaveURL(/\/admin\/backups$/);
		await expect(page.getByTestId('backup-status')).toContainText('The last backup failed.');
		expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()).violations).toEqual([]);
	} else {
		await expect(page.getByRole('heading', { name: 'Administration' })).toBeVisible();
		await expect(alert).toHaveCount(0);
		await screens.getByRole('link', { name: 'Backups' }).click();
		await expect(page.getByTestId('backup-status')).toContainText('Working.');
	}
});
