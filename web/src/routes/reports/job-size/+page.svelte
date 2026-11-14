<script lang="ts">
	// R38f: pages, capcodes and appliances per job, by event type: the
	// typical number (the median), the middle half and the largest.
	import BarChart from '#lib/BarChart.svelte';
	import ReportPage from '#lib/ReportPage.svelte';
	import { eventTypeName, longDay, type JobSizeReport, type SizeStat } from '#lib/reports.ts';

	const name = (r: JobSizeReport['rows'][number]) => (r.event_type ? eventTypeName(r.event_type) : 'Every event type');
	const spread = (s: SizeStat) => (s.low === s.high ? `${s.low}` : `${s.low}–${s.high}`);
</script>

<ReportPage
	title="Job size"
	intro="How big jobs are: their pages, the capcodes they paged and the appliances they named. Typical is the median, half the jobs are in the middle range, and the largest is shown too."
	name="job-size"
	slug="job-size"
	empty={(d: JobSizeReport) => d.jobs === 0}
	csv={(d: JobSizeReport) => ({
		headers: ['Event type', 'Jobs', 'Pages typical', 'Pages middle half', 'Pages largest', 'Capcodes typical', 'Capcodes middle half', 'Capcodes largest', 'Appliances typical', 'Appliances middle half', 'Appliances largest'],
		rows: d.rows.map((r) => [name(r), r.jobs, r.pages.median, spread(r.pages), r.pages.max, r.capcodes.median, spread(r.capcodes), r.capcodes.max, r.appliances.median, spread(r.appliances), r.appliances.max])
	})}
>
	{#snippet body(d: JobSizeReport)}
		<div data-chart>
			<BarChart
				label="Chart of pages per job by event type: the middle half as a bar, the typical number as a mark, a line to the largest, as in the table"
				bars={d.rows.map((r) => ({ label: name(r), value: r.pages.median, text: `${r.pages.median} (${spread(r.pages)}, up to ${r.pages.max})`, range: r.pages }))}
			/>
		</div>
		<div class="scroll">
			<table data-testid="report-table">
				<caption>Per job, {longDay(d.from)} to {longDay(d.to)}: {d.jobs} {d.jobs === 1 ? 'job' : 'jobs'}{d.agency ? ` of ${d.agency}` : ''}</caption>
				<thead>
					<tr><th scope="col">Event type</th><th scope="col" class="num">Jobs</th><th scope="col" class="num">Pages</th><th scope="col" class="num">Capcodes</th><th scope="col" class="num">Appliances</th></tr>
				</thead>
				<tbody>
					{#each d.rows as r, i (i)}
						<tr>
							<th scope="row">{name(r)}</th>
							<td class="num">{r.jobs}</td>
							{#each [r.pages, r.capcodes, r.appliances] as s, k (k)}
								<td class="num">{s.median}<br /><span class="muted">{spread(s)}, up to {s.max}</span></td>
							{/each}
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/snippet}
</ReportPage>
