<script lang="ts">
	// R38c: jobs by locality, with a heat layer on the map (R35's tiles) for
	// the localities the address dataset has.
	import BarChart from '#lib/BarChart.svelte';
	import MapView from '#lib/MapView.svelte';
	import ReportPage from '#lib/ReportPage.svelte';
	import { longDay, percent, type LocalitiesReport } from '#lib/reports.ts';

	const name = (l: string | null) => l ?? 'No locality';
</script>

<ReportPage
	title="Busiest localities"
	intro="Jobs by the locality the first of their pages names. On the map, each locality the address dataset has is placed at its middle and drawn by its number of jobs; the table lists them all."
	name="localities"
	slug="busiest-localities"
	empty={(d: LocalitiesReport) => d.rows.length === 0}
	csv={(d: LocalitiesReport) => ({
		headers: ['Locality', 'Jobs', 'Share of jobs', 'On the map'],
		rows: d.rows.map((r) => [name(r.locality), r.jobs, percent(r.share), r.lon !== undefined ? 'yes' : 'no'])
	})}
>
	{#snippet body(d: LocalitiesReport, meta)}
		<div class="charts">
			<MapView {meta} variant="heat" heat={d.rows.filter((r) => r.lon !== undefined && r.lat !== undefined).map((r) => ({ name: r.locality ?? '', lon: r.lon!, lat: r.lat!, jobs: r.jobs }))} />
			<div data-chart>
				<BarChart label="Bar chart of the 15 localities with the most jobs, as in the table" bars={d.rows.slice(0, 15).map((r) => ({ label: name(r.locality), value: r.jobs, text: `${r.jobs} (${percent(r.share)})` }))} />
			</div>
		</div>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex (a table wider than the screen scrolls in its box, which the keyboard reaches too: WCAG 2.1.1) -->
		<div class="scroll" tabindex="0" role="region" aria-label="Report table">
			<table data-testid="report-table">
				<caption>{longDay(d.from)} to {longDay(d.to)}: {d.jobs} {d.jobs === 1 ? 'job' : 'jobs'}{d.agency ? ` of ${d.agency}` : ''}</caption>
				<thead><tr><th scope="col">Locality</th><th scope="col" class="num">Jobs</th><th scope="col" class="num">Share</th></tr></thead>
				<tbody>
					{#each d.rows as r (r.locality)}
						<tr>
							<th scope="row">{name(r.locality)}{#if r.locality !== null && r.lon === undefined}<br /><span class="muted">Not on the map</span>{/if}</th>
							<td class="num">{r.jobs}</td>
							<td class="num">{percent(r.share)}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/snippet}
</ReportPage>

<style>
	.charts {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
		min-width: 0;
	}
</style>
