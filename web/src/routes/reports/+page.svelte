<script lang="ts">
	import { page } from '$app/state';
	import { api, type Meta, type Report } from '#lib/api.ts';
	import { isoDate } from '#lib/format.ts';

	// R38: Emergency messages to one capcode per event category, for a date
	// range (inclusive, deployment time zone). The form is a GET, so a report
	// can be linked to (the alias view does).
	let meta: Meta | undefined = $state();
	let report: Report | undefined = $state();
	let error = $state('');

	const capcode = $derived(page.url.searchParams.get('capcode') ?? '');
	const from = $derived(page.url.searchParams.get('from') ?? '');
	const to = $derived(page.url.searchParams.get('to') ?? '');

	api.meta().then((m) => (meta = m));

	// By default the last 30 days, in the deployment time zone.
	const defaultTo = $derived(meta ? isoDate(new Date().toISOString(), meta.time_zone) : '');
	const defaultFrom = $derived(meta ? isoDate(new Date(Date.now() - 29 * 864e5).toISOString(), meta.time_zone) : '');

	$effect(() => {
		report = undefined;
		error = '';
		if (!capcode || !from || !to) return;
		api
			.report(capcode, from, to)
			.then((r) => (report = r))
			.catch((e) => (error = e.message));
	});

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
	<p class="note">Emergency messages to one capcode, by event category. Heartbeats aren't counted.</p>

	{#if meta}
		<form method="GET" action="/reports" class="form">
			<label>Capcode<input name="capcode" required inputmode="numeric" pattern="[0-9]{'{1,7}'}" value={capcode} /></label>
			<label>From<input name="from" type="date" required value={from || defaultFrom} /></label>
			<label>To<input name="to" type="date" required value={to || defaultTo} /></label>
			<button type="submit">Run report</button>
		</form>
	{/if}

	{#if error}<p class="error" role="alert">{error}</p>{/if}
	{#if report}
		<table data-testid="report">
			<caption>
				Capcode <a href="/capcodes/{report.capcode}">{report.capcode}</a>, {long(report.from)} to {long(report.to)}
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
		padding: 0 0.75rem 1rem;
		max-width: 48rem;
	}
	h1 {
		font-size: var(--text-xl);
		margin: 0.75rem 0 0.25rem;
	}
	.note {
		color: var(--muted);
		font-size: var(--text-sm);
		margin-top: 0;
	}
	.form {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 0.75rem;
		align-items: flex-end;
		margin: 0.75rem 0 1rem;
	}
	label {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		font-size: var(--text-sm);
	}
	table {
		border-collapse: collapse;
		width: 100%;
	}
	caption {
		text-align: left;
		font-weight: 600;
		padding-bottom: 0.5rem;
	}
	th,
	td {
		text-align: left;
		padding: 0.4rem 0.5rem 0.4rem 0;
		border-bottom: 1px solid var(--border);
		font-weight: normal;
	}
	thead th,
	tfoot th,
	tfoot td {
		font-weight: 600;
	}
	.num {
		text-align: right;
	}
	.code th {
		padding-left: 1.25rem;
		color: var(--muted);
		font-size: var(--text-sm);
	}
	.code td {
		color: var(--muted);
		font-size: var(--text-sm);
	}
</style>
