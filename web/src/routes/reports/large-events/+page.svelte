<script lang="ts">
	// R38i: large events (R87) per week or month: how many, how long they
	// typically last and how many jobs they typically hold. An event isn't one
	// agency's, so only the dates apply.
	import ColumnChart from '#lib/ColumnChart.svelte';
	import ReportPage from '#lib/ReportPage.svelte';
	import { longDay, monthName, shortDay, span, type LargeEventsReport } from '#lib/reports.ts';

	const when = (d: LargeEventsReport, start: string) => (d.by === 'month' ? monthName(start) : `Week of ${shortDay(start)}`);
	const tick = (d: LargeEventsReport, start: string) => (d.by === 'month' ? monthName(start) : shortDay(start));
	const time = (s: number | null) => (s === null ? '–' : span(s));
</script>

<ReportPage
	title="Large events over time"
	intro="Large events by the week or month they started: how many, their typical time from first to last page, and their typical number of jobs (each the median)."
	name="large-events"
	slug="large-events-over-time"
	defaultDays={365}
	filters={{}}
	empty={(d: LargeEventsReport) => d.events === 0}
	emptyText="No large events started in this range."
	csv={(d: LargeEventsReport) => ({
		headers: [d.by === 'month' ? 'Month' : 'Week from', 'Large events', 'Typical seconds', 'Typical time', 'Typical jobs'],
		rows: d.buckets.map((b) => [b.start, b.events, b.median_seconds, time(b.median_seconds), b.median_jobs])
	})}
>
	{#snippet controls(q: URLSearchParams)}
		<label class="field">
			<span>By</span>
			<select name="by" value={q.get('by') ?? 'month'}>
				<option value="month">Month</option>
				<option value="week">Week</option>
			</select>
		</label>
	{/snippet}
	{#snippet body(d: LargeEventsReport)}
		<div data-chart>
			<ColumnChart label="Column chart of large events per {d.by}, as in the table" columns={d.buckets.map((b) => ({ label: tick(d, b.start), value: b.events, text: String(b.events) }))} />
		</div>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex (a table wider than the screen scrolls in its box, which the keyboard reaches too: WCAG 2.1.1) -->
		<div class="scroll" tabindex="0" role="region" aria-label="Report table">
			<table data-testid="report-table">
				<caption>{longDay(d.from)} to {longDay(d.to)}: {d.events} large {d.events === 1 ? 'event' : 'events'}, typically {time(d.median_seconds)} and {d.median_jobs ?? '–'} {d.median_jobs === 1 ? 'job' : 'jobs'}</caption>
				<thead><tr><th scope="col">{d.by === 'month' ? 'Month' : 'Week'}</th><th scope="col" class="num">Events</th><th scope="col" class="num">Typical time</th><th scope="col" class="num">Typical jobs</th></tr></thead>
				<tbody>
					{#each d.buckets as b (b.start)}
						<tr><th scope="row">{when(d, b.start)}</th><td class="num">{b.events}</td><td class="num">{time(b.median_seconds)}</td><td class="num">{b.median_jobs ?? '–'}</td></tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/snippet}
</ReportPage>

<style>
	.field {
		display: flex;
		flex-direction: column;
		gap: var(--space-3xs);
		font-size: var(--text-sm);
	}
	.field > span {
		font-weight: var(--weight-strong);
	}
	.field select {
		min-height: var(--target);
	}
</style>
