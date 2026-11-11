<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, type Meta, type Report } from '#lib/api.ts';
	import { rangeProblem } from '#lib/filters.ts';
	import { isoDate } from '#lib/format.ts';
	import { reportBox, reportPick, reportSuggestions } from '#lib/search.ts';
	import { searchValues } from '#lib/searchValues.ts';
	import DateRange from '#lib/DateRange.svelte';
	import Suggest from '#lib/Suggest.svelte';

	// R38: Emergency messages to one capcode, or to one alias (every capcode
	// it covers), per event category, for a date range (inclusive, deployment
	// time zone). The alias or capcode is picked with the search box's
	// suggestion control. The report's address names what was picked
	// (?alias=… or ?capcode=…), so a report can be linked to (the alias view
	// does) and bookmarked.
	let meta: Meta | undefined = $state();
	let report: Report | undefined = $state();
	let error = $state('');
	let problem = $state('');
	let text = $state('');
	let aliases: string[] = $state([]);
	let box: Suggest | undefined = $state();

	const capcode = $derived(page.url.searchParams.get('capcode') ?? '');
	const alias = $derived(page.url.searchParams.get('alias') ?? '');
	const from = $derived(page.url.searchParams.get('from') ?? '');
	const to = $derived(page.url.searchParams.get('to') ?? '');

	api.meta().then((m) => (meta = m));

	// The box shows what the address asks for.
	$effect(() => {
		text = reportBox(page.url.searchParams);
		problem = '';
	});

	function load() {
		searchValues()
			.then((v) => {
				aliases = v.alias;
				box?.refresh();
			})
			.catch(() => {}); // capcodes can still be typed
	}
	load();

	// By default the last 30 days, in the deployment time zone.
	const defaultTo = $derived(meta ? isoDate(new Date().toISOString(), meta.time_zone) : '');
	const defaultFrom = $derived(meta ? isoDate(new Date(Date.now() - 29 * 864e5).toISOString(), meta.time_zone) : '');

	$effect(() => {
		report = undefined;
		error = '';
		if ((!capcode && !alias) || !from || !to) return;
		// A reversed range in the URL: say so, without asking the API.
		const problem = rangeProblem(from, to);
		if (problem) {
			error = problem;
			return;
		}
		api
			.report(alias ? { alias } : { capcode }, from, to)
			.then((r) => (report = r))
			.catch((e) => (error = e.message));
	});

	function submit(ev: SubmitEvent) {
		ev.preventDefault();
		box?.close();
		const picked = reportPick(text, aliases);
		problem = picked.problem;
		if (problem) return;
		const form = new FormData(ev.currentTarget as HTMLFormElement);
		const params = new URLSearchParams();
		if (picked.alias) params.set('alias', picked.alias);
		else params.set('capcode', picked.capcode ?? '');
		params.set('from', String(form.get('from') ?? ''));
		params.set('to', String(form.get('to') ?? ''));
		goto(`/reports/capcode?${params}`, { reset: false });
	}

	function long(d: string): string {
		const [y, m, day] = d.split('-').map(Number);
		return new Intl.DateTimeFormat('en-AU', { day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC' }).format(
			new Date(Date.UTC(y, m - 1, day))
		);
	}
</script>

<svelte:head><title>Reports · PagerArchive</title></svelte:head>

<div class="page">
	<h1>Capcode report</h1>
	<p class="note">Emergency messages to one alias or capcode, by event category. Heartbeats aren't counted.</p>

	{#if meta}
		<form method="GET" action="/reports/capcode" class="form" onsubmit={submit}>
			<Suggest
				bind:this={box}
				bind:text
				suggest={(t) => reportSuggestions(t, aliases)}
				onfocus={load}
				oninput={() => (problem = '')}
				label="Alias or capcode"
				showLabel
				placeholder="Part of an alias name, or a capcode"
				listLabel="Aliases and capcodes"
				describedby={problem ? 'report-problem' : undefined}
			/>
			<DateRange from={from || defaultFrom} to={to || defaultTo} timeZone={meta.time_zone} firstPage={meta.first_page} lastPage={meta.last_page} />
			<button type="submit">Run report</button>
			{#if problem}<p class="problem" id="report-problem" role="alert">{problem}</p>{/if}
		</form>
	{/if}

	{#if error}<p class="error" role="alert">{error}</p>{/if}
	{#if report}
		<table data-testid="report">
			<caption>
				{#if report.alias}
					Alias {report.alias} ({#each report.aliases ?? [] as a, i (a.id)}{i ? ', ' : ''}<a href="/aliases/{a.id}">{a.pattern}</a>{/each}),
				{:else}
					Capcode <a href="/capcodes/{report.capcode}">{report.capcode}</a>,
				{/if}
				{long(report.from)} to {long(report.to)}
			</caption>
			<thead><tr><th scope="col">Event category</th><th scope="col" class="num">Messages</th></tr></thead>
			<tbody>
				{#each report.categories as c (c.category)}
					<tr><th scope="row">{c.category}</th><td class="num">{c.count}</td></tr>
					{#each c.codes ?? [] as code, i (i)}
						<tr class="code">
							<th scope="row">{code.code ?? 'No event type (e.g. not parsed)'}</th>
							<td class="num">{code.count}</td>
						</tr>
					{/each}
				{/each}
				{#if report.categories.length === 0}
					<tr><td colspan="2">No Emergency messages in this range.</td></tr>
				{/if}
			</tbody>
			<tfoot><tr><th scope="row">Total</th><td class="num">{report.total}</td></tr></tfoot>
		</table>
	{/if}
</div>

<style>
	.page {
		padding: 0 0 var(--space-lg);
		max-width: var(--measure-wide);
	}
	h1 {
		font-size: var(--text-xl);
		margin: var(--space-md) 0 var(--space-2xs);
	}
	.note {
		color: var(--muted);
		font-size: var(--text-sm);
		margin-top: 0;
	}
	.form {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm) var(--space-md);
		align-items: flex-end;
		margin: var(--space-md) 0 var(--space-lg);
	}
	.form > :global(.suggest) {
		flex: 1 1 var(--field-wide);
	}
	.problem {
		flex-basis: 100%;
		margin: 0;
		color: var(--danger-text);
		font-size: var(--text-sm);
	}
	table {
		border-collapse: collapse;
		width: 100%;
	}
	caption {
		text-align: left;
		font-weight: var(--weight-strong);
		padding-bottom: var(--space-sm);
	}
	th,
	td {
		text-align: left;
		padding: var(--space-xs) var(--space-sm) var(--space-xs) 0;
		border-bottom: var(--border-width) solid var(--border);
		font-weight: var(--weight-normal);
	}
	thead th,
	tfoot th,
	tfoot td {
		font-weight: var(--weight-strong);
	}
	.num {
		text-align: right;
	}
	.code th {
		padding-left: var(--space-xl);
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.code td {
		color: var(--muted);
		font-size: var(--text-sm);
	}
</style>
