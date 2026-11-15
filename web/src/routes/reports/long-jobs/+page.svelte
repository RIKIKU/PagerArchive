<script lang="ts">
	// R38g: the jobs with the longest time from their first page to their
	// last, each linking to its job view (R34).
	import BarChart from '#lib/BarChart.svelte';
	import ReportPage from '#lib/ReportPage.svelte';
	import { dateTime, isoAttr } from '#lib/format.ts';
	import { eventTypeName, longDay, span, type LongJobsReport } from '#lib/reports.ts';
</script>

<ReportPage
	title="Long-running jobs"
	intro="The jobs that started in the range with the longest time from their first page to their last. Jobs whose pages were all sent at one time aren't listed."
	name="long-jobs"
	slug="long-running-jobs"
	empty={(d: LongJobsReport) => d.rows.length === 0}
	emptyText="No jobs in this range had pages at more than one time."
	csv={(d: LongJobsReport, meta) => ({
		headers: ['Job number', 'First page', 'Last page', 'Seconds', 'Time', 'Pages', 'Event type', 'Locality'],
		rows: d.rows.map((j) => [j.job_number, dateTime(j.first, meta.time_zone), dateTime(j.last, meta.time_zone), j.seconds, span(j.seconds), j.pages, eventTypeName(j.event_type), j.locality ?? ''])
	})}
>
	{#snippet controls(q: URLSearchParams)}
		<label class="field">
			<span>Show</span>
			<select name="limit" value={q.get('limit') ?? '20'}>
				{#each ['20', '50', '100'] as n (n)}<option value={n}>Top {n}</option>{/each}
			</select>
		</label>
	{/snippet}
	{#snippet body(d: LongJobsReport, meta)}
		<div data-chart>
			<BarChart label="Bar chart of each job's time from first to last page, as in the table" bars={d.rows.map((j) => ({ label: j.job_number, value: j.seconds, text: span(j.seconds) }))} />
		</div>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex (a table wider than the screen scrolls in its box, which the keyboard reaches too: WCAG 2.1.1) -->
		<div class="scroll" tabindex="0" role="region" aria-label="Report table">
			<table data-testid="report-table">
				<caption>The {d.rows.length} longest of {d.jobs} {d.jobs === 1 ? 'job' : 'jobs'}, {longDay(d.from)} to {longDay(d.to)}{d.agency ? `, ${d.agency}` : ''}</caption>
				<thead>
					<tr><th scope="col">Job</th><th scope="col">First and last page</th><th scope="col" class="num">Time</th><th scope="col" class="num">Pages</th></tr>
				</thead>
				<tbody>
					{#each d.rows as j (j.job_number)}
						<tr>
							<th scope="row"><a href="/jobs/{encodeURIComponent(j.job_number)}">{j.job_number}</a><br /><span class="muted">{eventTypeName(j.event_type)}{j.locality ? `, ${j.locality}` : ''}</span></th>
							<td><time datetime={isoAttr(j.first)}>{dateTime(j.first, meta.time_zone)}</time><br /><time datetime={isoAttr(j.last)}>{dateTime(j.last, meta.time_zone)}</time></td>
							<td class="num">{span(j.seconds)}</td>
							<td class="num">{j.pages}</td>
						</tr>
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
