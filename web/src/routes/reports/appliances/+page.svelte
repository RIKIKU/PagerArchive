<script lang="ts">
	// R38h: how often each appliance (R8's units, shown as Appliances) is
	// named: the jobs naming it, each job once however many pages name it.
	import BarChart from '#lib/BarChart.svelte';
	import ReportPage from '#lib/ReportPage.svelte';
	import { longDay, percent, type AppliancesReport } from '#lib/reports.ts';
</script>

<ReportPage
	title="Appliance activity"
	intro="How many jobs name each appliance, on any of their pages."
	name="appliances"
	slug="appliance-activity"
	empty={(d: AppliancesReport) => d.rows.length === 0}
	emptyText="No jobs in this range named an appliance."
	csv={(d: AppliancesReport) => ({ headers: ['Appliance', 'Jobs', 'Share of jobs'], rows: d.rows.map((a) => [a.appliance, a.jobs, percent(a.share)]) })}
>
	{#snippet body(d: AppliancesReport)}
		<div data-chart>
			<BarChart label="Bar chart of the 20 appliances named in the most jobs, as in the table" bars={d.rows.slice(0, 20).map((a) => ({ label: a.appliance, value: a.jobs, text: `${a.jobs} (${percent(a.share)})` }))} />
		</div>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex (a table wider than the screen scrolls in its box, which the keyboard reaches too: WCAG 2.1.1) -->
		<div class="scroll" tabindex="0" role="region" aria-label="Report table">
			<table data-testid="report-table">
				<caption>{longDay(d.from)} to {longDay(d.to)}: {d.rows.length} {d.rows.length === 1 ? 'appliance' : 'appliances'} in {d.jobs} {d.jobs === 1 ? 'job' : 'jobs'}{d.agency ? ` of ${d.agency}` : ''}</caption>
				<thead><tr><th scope="col">Appliance</th><th scope="col" class="num">Jobs</th><th scope="col" class="num">Share of jobs</th></tr></thead>
				<tbody>
					{#each d.rows as a (a.appliance)}
						<tr><th scope="row">{a.appliance}</th><td class="num">{a.jobs}</td><td class="num">{percent(a.share)}</td></tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/snippet}
</ReportPage>
