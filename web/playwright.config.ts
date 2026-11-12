/// <reference types="node" />
import { defineConfig, devices } from '@playwright/test';

// The API is started by scripts/e2e.sh or scripts/stack-e2e.sh, which set PA_URL.
const baseURL = process.env.PA_URL ?? 'http://127.0.0.1:18080';

export default defineConfig({
	testDir: 'tests',
	fullyParallel: false,
	workers: 1, // tests share one database
	// Layer 2 of CI's retries (ADR 0025): in CI a failed test is tried once
	// more; one that passes only then is reported as flaky (a warning and the
	// job's summary, tests/flaky-reporter.ts), and one that fails both times
	// fails. By hand, no retries.
	retries: process.env.CI ? 1 : 0,
	// The HTML report is uploaded by CI when a browser test fails.
	reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }], ['./tests/flaky-reporter.ts']],
	use: { baseURL, trace: 'retain-on-failure', screenshot: 'only-on-failure' },
	// R36: Chrome/Brave (Chromium), Firefox and Safari (WebKit).
	projects: [
		{ name: 'chromium', use: { ...devices['Desktop Chrome'] } },
		{ name: 'firefox', use: { ...devices['Desktop Firefox'] } },
		{ name: 'webkit', use: { ...devices['Desktop Safari'] } }
	]
});
