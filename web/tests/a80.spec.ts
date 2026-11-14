/// <reference types="node" />
// A80 in a browser (scripts/a80.sh): what an admin sees in Administration about
// the backups, from the app's address through Authentik's sign-in, as a person
// would. PA_A80_EXPECT says what the backups are doing: "failed" or "ok"; or
// "now", to press Back up now and wait for the backup service's good result.
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
	if (process.env.PA_A80_EXPECT === 'now') {
		// R86a: the backup service sees the request within a minute, then the
		// dumps and the check: minutes, on A77's small archive.
		test.setTimeout(600_000);
		await screens.getByRole('link', { name: 'Backups' }).click();
		const button = page.getByRole('button', { name: 'Back up now' });
		const state = page.getByTestId('backup-now');
		await button.click();
		await expect(state).toContainText('Asked for at');
		await expect(button).toBeDisabled();
		const again = await page.request.post('/api/v1/admin/backup');
		expect(again.status()).toBe(409);
		expect(['backup_queued', 'backup_running']).toContain((await again.json()).code);
		await expect(state).toContainText('Running since', { timeout: 120_000 });
		await page.screenshot({ path: test.info().outputPath('backup-running.png') });
		expect((await new AxeBuilder({ page }).withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa']).analyze()).violations).toEqual([]);
		await expect(state).toContainText('was good.', { timeout: 420_000 });
		await expect(button).toBeEnabled();
		await expect(page.getByTestId('backup-status')).toContainText('Working.');
		await page.screenshot({ path: test.info().outputPath('backup-done.png') });
	} else if (process.env.PA_A80_EXPECT === 'failed') {
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
