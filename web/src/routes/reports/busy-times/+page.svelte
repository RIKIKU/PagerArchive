<script lang="ts">
	// R38d: jobs by the hour they started and the day of the week, as a grid
	// whose cells are shaded by their count and show it, for an agency, an
	// event type, or one alias or capcode (picked as the capcode report's,
	// R38).
	import { page } from '$app/state';
	import ReportPage from '#lib/ReportPage.svelte';
	import Suggest from '#lib/Suggest.svelte';
	import { longDay, type BusyTimesReport } from '#lib/reports.ts';
	import { reportBox, reportPick, reportSuggestions } from '#lib/search.ts';
	import { searchValues } from '#lib/searchValues.ts';

	const days = ['Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday', 'Sunday'];
	const short = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
	const hour = (h: number) => `${String(h).padStart(2, '0')}:00`;

	let text = $state('');
	let aliases: string[] = $state([]);
	let box: Suggest | undefined = $state();
	// The box shows what the address asks for.
	$effect(() => {
		text = reportBox(page.url.searchParams);
	});
	function load() {
		searchValues()
			.then((v) => {
				aliases = v.alias;
				box?.refresh();
			})
			.catch(() => {});
	}
	load();

	// The shade of a cell: five steps from none to the most (R82c's --heat-*).
	const level = (n: number, most: number) => (n === 0 ? 0 : 1 + Math.round((3 * (n - 1)) / Math.max(1, most - 1)));
	const most = (d: BusyTimesReport) => Math.max(0, ...d.cells.flat());
	function busiest(d: BusyTimesReport): string {
		const m = most(d);
		const at: string[] = [];
		d.cells.forEach((row, day) => row.forEach((n, h) => n === m && at.push(`${days[day]} ${hour(h)}`)));
		return `${at.slice(0, 3).join(', ')}${at.length > 3 ? ` and ${at.length - 3} more` : ''}: ${m} ${m === 1 ? 'job' : 'jobs'}`;
	}
	const whose = (d: BusyTimesReport) => (d.alias ? ` to ${d.alias}` : d.capcode ? ` to capcode ${d.capcode}` : d.agency ? ` of ${d.agency}` : '');
</script>

<ReportPage
	title="When it's busy"
	intro="Jobs by the hour their first page was sent and the day of the week, in the deployment's time zone. For an agency, an event type, or one alias or capcode."
	name="busy-times"
	slug="when-its-busy"
	empty={(d: BusyTimesReport) => d.jobs === 0}
	prepare={(_form, params) => {
		box?.close();
		params.delete('alias');
		params.delete('capcode');
		params.delete('who');
		if (!text.trim()) return '';
		const picked = reportPick(text, aliases);
		if (picked.problem) return picked.problem;
		if (picked.alias) params.set('alias', picked.alias);
		else params.set('capcode', picked.capcode ?? '');
		return '';
	}}
	csv={(d: BusyTimesReport) => ({
		headers: ['Hour', ...days],
		rows: Array.from({ length: 24 }, (_, h) => [hour(h), ...d.cells.map((row) => row[h])])
	})}
>
	{#snippet controls(_q: URLSearchParams)}
		<div class="who">
			<Suggest
				bind:this={box}
				bind:text
				suggest={(t) => reportSuggestions(t, aliases)}
				onfocus={load}
				label="Alias or capcode (optional)"
				showLabel
				placeholder="Every alias and capcode"
				listLabel="Aliases and capcodes"
			/>
		</div>
	{/snippet}
	{#snippet body(d: BusyTimesReport)}
		<div class="whole">
			<p class="summary" data-testid="report-busiest">Busiest: {busiest(d)}.</p>
			<!-- svelte-ignore a11y_no_noninteractive_tabindex (a table wider than the screen scrolls in its box, which the keyboard reaches too: WCAG 2.1.1) -->
			<div class="scroll" data-chart tabindex="0" role="region" aria-label="Jobs by hour and day, as a table">
				<table class="grid" data-testid="report-table">
					<caption>Jobs{whose(d)} by hour and day, {longDay(d.from)} to {longDay(d.to)}: {d.jobs} in all</caption>
					<thead>
						<tr>
							<th scope="col">Hour</th>
							{#each short as s, i (s)}<th scope="col" class="num"><abbr title={days[i]}>{s}</abbr></th>{/each}
						</tr>
					</thead>
					<tbody>
						{#each Array.from({ length: 24 }, (_, h) => h) as h (h)}
							<tr>
								<th scope="row">{hour(h)}</th>
								{#each d.cells as row, day (day)}
									<td class="num l{level(row[h], most(d))}">{row[h]}</td>
								{/each}
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
	{/snippet}
</ReportPage>

<style>
	.who {
		min-width: var(--field-narrow);
		flex: 1 1 var(--field-narrow);
		max-width: var(--field-wide);
	}
	.whole {
		grid-column: 1 / -1;
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		min-width: 0;
	}
	.summary {
		margin: 0;
		font-weight: var(--weight-strong);
	}
	.grid {
		max-width: var(--measure);
	}
	.grid td,
	.grid th {
		min-width: var(--heat-cell);
		padding: var(--space-3xs) var(--space-xs);
		text-align: center;
	}
	.grid tbody th {
		text-align: left;
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.grid abbr {
		text-decoration: none;
	}
	.l0 {
		background: var(--heat-0);
		color: var(--muted);
	}
	.l1 {
		background: var(--heat-1);
	}
	.l2 {
		background: var(--heat-2);
	}
	.l3 {
		background: var(--heat-3);
	}
	.l4 {
		background: var(--heat-4);
		font-weight: var(--weight-strong);
	}
</style>
