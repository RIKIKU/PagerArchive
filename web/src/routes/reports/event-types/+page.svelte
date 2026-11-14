<script lang="ts">
	// R38e: each event type's share of the jobs over the range, and month by
	// month, to show the seasons. Every event type is counted, so there's no
	// event type filter.
	import BarChart from '#lib/BarChart.svelte';
	import ReportPage from '#lib/ReportPage.svelte';
	import { eventTypeName, longDay, monthName, percent, type EventTypesReport } from '#lib/reports.ts';

	// The month table's columns: the five event types with the most jobs, and
	// the rest together.
	const top = 5;
	const shown = (d: EventTypesReport) => d.types.slice(0, top);
	const rest = (d: EventTypesReport, m: EventTypesReport['months'][number]) => m.types.slice(top).reduce((a, b) => a + b, 0);
	const share = (n: number, of: number) => (of ? percent(n / of) : '–');
</script>

<ReportPage
	title="Event type mix"
	intro="Each event type's share of the jobs (the first event type a job's pages give), over the range and month by month."
	name="event-types"
	slug="event-type-mix"
	defaultDays={365}
	filters={{ agency: true, priority: true }}
	empty={(d: EventTypesReport) => d.jobs === 0}
	csv={(d: EventTypesReport) => ({
		headers: ['Month', 'Jobs', ...d.types.map((t) => eventTypeName(t))],
		rows: [...d.months.map((m) => [m.month, m.jobs, ...m.types]), ['All', d.jobs, ...d.types.map((t) => t.jobs)]]
	})}
>
	{#snippet body(d: EventTypesReport)}
		<div data-chart>
			<BarChart label="Bar chart of each event type's share of the jobs, as in the table" bars={d.types.map((t) => ({ label: eventTypeName(t), value: t.jobs, text: `${percent(t.share)} (${t.jobs})` }))} />
		</div>
		<div class="tables">
			<table data-testid="report-table">
				<caption>{longDay(d.from)} to {longDay(d.to)}: {d.jobs} {d.jobs === 1 ? 'job' : 'jobs'}{d.agency ? ` of ${d.agency}` : ''}</caption>
				<thead><tr><th scope="col">Event type</th><th scope="col">Category</th><th scope="col" class="num">Jobs</th><th scope="col" class="num">Share</th></tr></thead>
				<tbody>
					{#each d.types as t (t.code)}
						<tr><th scope="row">{eventTypeName(t)}</th><td>{t.category ?? ''}</td><td class="num">{t.jobs}</td><td class="num">{percent(t.share)}</td></tr>
					{/each}
				</tbody>
			</table>
			<div class="scroll">
				<table data-testid="report-months">
					<caption>Month by month: each event type's share of the month's jobs</caption>
					<thead>
						<tr>
							<th scope="col">Month</th>
							<th scope="col" class="num">Jobs</th>
							{#each shown(d) as t (t.code)}<th scope="col" class="num">{t.code ?? 'None'}</th>{/each}
							{#if d.types.length > top}<th scope="col" class="num">Others</th>{/if}
						</tr>
					</thead>
					<tbody>
						{#each d.months as m (m.month)}
							<tr>
								<th scope="row">{monthName(m.month)}</th>
								<td class="num">{m.jobs}</td>
								{#each shown(d) as _t, i (i)}<td class="num">{share(m.types[i], m.jobs)}</td>{/each}
								{#if d.types.length > top}<td class="num">{share(rest(d, m), m.jobs)}</td>{/if}
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	{/snippet}
</ReportPage>

<style>
	.tables {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
		min-width: 0;
	}
</style>
