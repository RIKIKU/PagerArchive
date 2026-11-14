<script lang="ts">
	// R38b: the aliases and capcodes with the most jobs, with each one's share
	// of the jobs and its jobs in the period of the same length just before.
	import BarChart from '#lib/BarChart.svelte';
	import ReportPage from '#lib/ReportPage.svelte';
	import { longDay, percent, type BusiestReport, type BusiestRow } from '#lib/reports.ts';

	const who = (r: BusiestRow) => (r.alias ? `${r.alias} (${r.pattern})` : `Capcode ${r.capcode}`);
	const change = (r: BusiestRow) => {
		if (r.previous_jobs === 0) return r.jobs ? 'new' : 'no change';
		const d = r.jobs - r.previous_jobs;
		return d === 0 ? 'no change' : `${d > 0 ? '+' : ''}${d} (${d > 0 ? '+' : ''}${Math.round((d / r.previous_jobs) * 100)}%)`;
	};
</script>

<ReportPage
	title="Busiest capcodes"
	intro="The aliases and capcodes with no alias that the most jobs paged, and their jobs in the period of the same length just before. A job counts once for each alias or capcode it paged, however many pages it had, so the shares can add up to more than 100%."
	name="busiest"
	slug="busiest-capcodes"
	empty={(d: BusiestReport) => d.rows.length === 0}
	csv={(d: BusiestReport) => ({
		headers: ['Rank', 'Alias', 'Capcode or pattern', 'Agency', 'Jobs', 'Share of jobs', `Jobs ${d.previous_from} to ${d.previous_to}`],
		rows: d.rows.map((r) => [r.rank, r.alias ?? '', r.pattern ?? r.capcode ?? '', r.agency ?? '', r.jobs, percent(r.share), r.previous_jobs])
	})}
>
	{#snippet controls(q: URLSearchParams)}
		<label class="field">
			<span>Show</span>
			<select name="limit" value={q.get('limit') ?? '10'}>
				{#each ['10', '25', '50'] as n (n)}<option value={n}>Top {n}</option>{/each}
			</select>
		</label>
	{/snippet}
	{#snippet body(d: BusiestReport)}
		<div data-chart>
			<BarChart
				label="Bar chart of jobs per alias or capcode, this period and the period before, as in the table"
				legend={{ now: `This period`, previous: `The ${d.previous_from === d.previous_to ? "day" : "period"} before` }}
				bars={d.rows.map((r) => ({ label: r.alias ?? r.capcode ?? '', value: r.jobs, text: `${r.jobs} (was ${r.previous_jobs})`, previous: { value: r.previous_jobs, text: String(r.previous_jobs) } }))}
			/>
		</div>
		<!-- svelte-ignore a11y_no_noninteractive_tabindex (a table wider than the screen scrolls in its box, which the keyboard reaches too: WCAG 2.1.1) -->
		<div class="scroll" tabindex="0" role="region" aria-label="Report table">
			<table data-testid="report-table">
				<caption>Top {d.limit}, {longDay(d.from)} to {longDay(d.to)}: {d.jobs} {d.jobs === 1 ? 'job' : 'jobs'}{d.agency ? ` of ${d.agency}` : ''}</caption>
				<thead>
					<tr>
						<th scope="col" class="num">Rank</th>
						<th scope="col">Alias or capcode</th>
						<th scope="col" class="num">Jobs</th>
						<th scope="col" class="num">Share</th>
						<th scope="col" class="num">Period before</th>
					</tr>
				</thead>
				<tbody>
					{#each d.rows as r (r.alias_id ?? r.capcode)}
						<tr>
							<td class="num">{r.rank}</td>
							<th scope="row">
								{#if r.alias_id !== null}<a href="/aliases/{r.alias_id}">{who(r)}</a>{:else}<a href="/capcodes/{r.capcode}">{who(r)}</a>{/if}
								{#if r.agency}<br /><span class="muted">{r.agency}</span>{/if}
							</th>
							<td class="num">{r.jobs}</td>
							<td class="num">{percent(r.share)}</td>
							<td class="num">{r.previous_jobs}<br /><span class="muted">{change(r)}</span></td>
						</tr>
					{/each}
				</tbody>
				<tfoot>
					<tr><th scope="row" colspan="2">Every job, this period and the one before ({longDay(d.previous_from)} to {longDay(d.previous_to)})</th><td class="num">{d.jobs}</td><td></td><td class="num">{d.previous_jobs}</td></tr>
				</tfoot>
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
