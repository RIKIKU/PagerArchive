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
	// Each test has its own time limit (30 s, or its test.setTimeout), so a
	// stuck test fails in minutes and names itself. The whole run has one too
	// in CI: the browser job's e2e.spec.ts run takes 50-60 minutes, and up to
	// about 75 on a slow runner (the same tests, each slower, none stuck).
	// Past 95 minutes Playwright stops, names the test it was on, lists those
	// not run and writes its report, before the job's own limit (110) would
	// cancel it with no report at all.
	globalTimeout: process.env.CI ? 95 * 60_000 : 0,
	// The HTML report is uploaded by CI when a browser test fails.
	reporter: [['list'], ['html', { open: 'never', outputFolder: 'playwright-report' }], ['./tests/flaky-reporter.ts']],
	// The app's service worker (R90) is blocked unless a test allows it: it
	// passes every request but the app shell's straight to the network, but a
	// page it controls is one whose requests page.route can't always see.
	// A116's tests allow it, and browse the app with it.
	use: { baseURL, trace: 'retain-on-failure', screenshot: 'only-on-failure', serviceWorkers: 'block' },
	// R36: Chrome/Brave (Chromium), Firefox and Safari (WebKit).
	projects: [
		{ name: 'chromium', use: { ...devices['Desktop Chrome'] } },
		{ name: 'firefox', use: { ...devices['Desktop Firefox'] } },
		{ name: 'webkit', use: { ...devices['Desktop Safari'] } }
	]
});
