/// <reference types="node" />
import { defineConfig, devices } from '@playwright/test';

// The API is started by scripts/e2e.sh, which sets these.
const baseURL = process.env.PA_USER_URL ?? 'http://127.0.0.1:18080';

export default defineConfig({
	testDir: 'tests',
	fullyParallel: false,
	workers: 1, // tests share one database
	retries: 0,
	// The HTML report is uploaded by CI when a browser test fails.
	reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }]],
	use: { baseURL, trace: 'retain-on-failure', screenshot: 'only-on-failure' },
	// R36: Chrome/Brave (Chromium), Firefox and Safari (WebKit).
	projects: [
		{ name: 'chromium', use: { ...devices['Desktop Chrome'] } },
		{ name: 'firefox', use: { ...devices['Desktop Firefox'] } },
		{ name: 'webkit', use: { ...devices['Desktop Safari'] } }
	]
});
