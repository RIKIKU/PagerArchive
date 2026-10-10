/// <reference types="node" />
import { defineConfig, devices } from '@playwright/test';

// The API is started by scripts/e2e.sh, which sets these.
const baseURL = process.env.PA_USER_URL ?? 'http://127.0.0.1:18080';

export default defineConfig({
	testDir: 'tests',
	fullyParallel: false,
	workers: 1, // tests share one database
	retries: 0,
	reporter: [['list']],
	use: { baseURL, trace: 'retain-on-failure' },
	// R36: Chrome/Brave (Chromium), Firefox and Safari (WebKit).
	projects: [
		{ name: 'chromium', use: { ...devices['Desktop Chrome'] } },
		{ name: 'firefox', use: { ...devices['Desktop Firefox'] } },
		{ name: 'webkit', use: { ...devices['Desktop Safari'] } }
	]
});
