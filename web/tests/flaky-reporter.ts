/// <reference types="node" />
// Layer 2 of CI's retries (ADR 0025): in CI a failed browser test is tried
// once more (playwright.config.ts), and one that passes only then is flaky.
// This reporter lists each as a warning annotation, "flaky: <test>", and in
// the job's summary, so a retry never hides it. A test that fails both times
// still fails the run.
import { appendFileSync } from 'node:fs';
import type { FullConfig, FullResult, Reporter, Suite, TestCase } from '@playwright/test/reporter';

export default class FlakyReporter implements Reporter {
	private suite: Suite | undefined;

	onBegin(_config: FullConfig, suite: Suite) {
		this.suite = suite;
	}

	onEnd(_result: FullResult) {
		const flaky = (this.suite?.allTests() ?? []).filter((t: TestCase) => t.outcome() === 'flaky');
		if (flaky.length === 0) return;
		const name = (t: TestCase) => t.titlePath().filter(Boolean).join(' › ');
		const failedTries = (t: TestCase) => t.results.filter((r) => r.status !== 'passed' && r.status !== 'skipped').length;
		const lines = flaky.map((t) => `${name(t)} (failed ${failedTries(t)} time(s), then passed on try ${t.results.length})`);
		if (process.env.GITHUB_ACTIONS) {
			const esc = (s: string) => s.replace(/%/g, '%25').replace(/\r/g, '%0D').replace(/\n/g, '%0A');
			for (const line of lines) console.log(`::warning title=Flaky::flaky: ${esc(line)}`);
		} else {
			for (const line of lines) console.log(`flaky: ${line}`);
		}
		// The step's output, so CI keeps the report and the failed try's trace.
		if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, 'flaky=true\n');
		const summary = process.env.GITHUB_STEP_SUMMARY;
		if (summary) {
			appendFileSync(
				summary,
				`\n### Passed only on a retry\n\nEach is a flake to look into: it failed, then passed when tried again (ADR 0025).\n\n${lines.map((l) => `- flaky: ${l}`).join('\n')}\n`
			);
		}
	}
}
